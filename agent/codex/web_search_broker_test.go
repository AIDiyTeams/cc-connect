package codex

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func webSearchBrokerFixture(t *testing.T) (*capabilityBroker, *appServerSession) {
	t.Helper()
	s := webSearchTestSession(t)
	s.events = make(chan core.Event, 64)
	dir := t.TempDir()
	s.taskRuntimeEnvFile = filepath.Join(dir, "machine.env")
	b := &capabilityBroker{session: s, origin: "https://test.tomako.ai", socket: filepath.Join(dir, "cap.sock")}
	s.capabilityBroker = b
	if _, err := b.writeRuntime(s.runtime); err != nil {
		t.Fatal(err)
	}
	return b, s
}

func callBrokerWebSearch(b *capabilityBroker, input map[string]any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	b.serve(w, httptest.NewRequest(http.MethodPost, "/v1/web-search", bytes.NewReader(data)))
	return w
}

func TestBrokerWebSearchRunsTheBatchThroughTheUnifiedEntryAndLedger(t *testing.T) {
	useDeepSeekOnly(t)
	calls := fakeDeepSeekAPI(t, 200, `{"results":[
		{"url":"https://buffer.com/pricing","title":"Buffer &amp; Pricing","snippet":"","summary":"Essentials  $6"},
		{"url":"javascript:alert(1)","title":"not a public page"}]}`)
	b, s := webSearchBrokerFixture(t)
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	w := callBrokerWebSearch(b, map[string]any{"revision": b.revision, "queries": []string{" buffer  pricing ", "later pricing"}, "limit": 4})
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var output struct {
		Batches []brokerWebSearchBatch `json:"batches"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &output); err != nil || len(output.Batches) != 2 {
		t.Fatalf("body=%s err=%v", w.Body.String(), err)
	}
	first := output.Batches[0]
	if first.Query != "buffer pricing" || output.Batches[1].Query != "later pricing" || first.Provider != "deepseek" ||
		len(first.Results) != 1 || first.Results[0] != (brokerWebSearchResult{Title: "Buffer & Pricing", URL: "https://buffer.com/pricing", Snippet: "Essentials $6"}) {
		t.Fatalf("batches=%+v", output.Batches)
	}
	if len(*calls) != 2 || (*calls)[0].Body["count"] != float64(4) || strings.Contains(w.Body.String(), "ds-env") {
		t.Fatalf("calls=%+v", *calls)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 2 {
		t.Fatalf("receipts=%+v", receipts)
	}
	for _, receipt := range receipts {
		if receipt["entry"] != webSearchEntryBroker || receipt["scenario"] != "research" || receipt["kind"] != "web_search" ||
			receipt["task_id"] != "cmsg-a" || receipt["cost_usd"] != 0.00075 {
			t.Fatalf("receipt=%+v", receipt)
		}
	}
	// A repeated batch is served from the shared cache: no billed call, no receipt.
	if w := callBrokerWebSearch(b, map[string]any{"revision": b.revision, "queries": []string{"buffer pricing"}, "limit": 4}); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"cached":true`) || len(*calls) != 2 || len(drainUsageReceipts(s)) != 0 {
		t.Fatalf("cache: %d %s calls=%d", w.Code, w.Body.String(), len(*calls))
	}
}

func TestBrokerWebSearchToolReceiptsKeepTheirOwnEntry(t *testing.T) {
	useDeepSeekOnly(t)
	fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://example.com/","title":"Example","snippet":"x"}]}`)
	s := webSearchTestSession(t)
	if _, err := s.searchWeb(map[string]any{"query": "example"}); err != nil {
		t.Fatal(err)
	}
	if receipts := drainUsageReceipts(s); len(receipts) != 1 || receipts[0]["entry"] != webSearchEntryTool {
		t.Fatalf("receipts=%+v", receipts)
	}
}

func TestBrokerWebSearchRejectsALongQueryBeforeAnyBilledCall(t *testing.T) {
	useDeepSeekOnly(t)
	calls := fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://example.com/","title":"x","snippet":"y"}]}`)
	b, s := webSearchBrokerFixture(t)
	w := callBrokerWebSearch(b, map[string]any{"revision": b.revision, "queries": []string{"short", strings.Repeat("a", 129)}, "limit": 3})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"QUERY_TOO_LONG"`) || !strings.Contains(w.Body.String(), `"maxChars":128`) ||
		!strings.Contains(w.Body.String(), `"queryIndexes":[1]`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if len(*calls) != 0 || len(drainUsageReceipts(s)) != 0 {
		t.Fatal("a refused batch must not call a provider")
	}
}

func TestBrokerWebSearchReportsProviderFailuresPerQueryWithAlerts(t *testing.T) {
	useDeepSeekOnly(t)
	fakeDeepSeekAPI(t, 402, `{"error":{"message":"Insufficient Balance"}}`)
	b, s := webSearchBrokerFixture(t)
	w := callBrokerWebSearch(b, map[string]any{"revision": b.revision, "queries": []string{"one"}, "limit": 3})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "web search failed") || strings.Contains(w.Body.String(), "ds-env") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 1 || receipts[0]["error_category"] != "payment_required" || receipts[0]["alert"] != true {
		t.Fatalf("receipts=%+v", receipts)
	}
}

func TestBrokerWebSearchRefusesUnavailableSearchAndForeignAuthority(t *testing.T) {
	t.Setenv(webSearchConfigEnv, "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	b, s := webSearchBrokerFixture(t)
	if w := callBrokerWebSearch(b, map[string]any{"revision": b.revision, "queries": []string{"one"}, "limit": 3}); w.Code != 503 ||
		!strings.Contains(w.Body.String(), "WEB_SEARCH_UNAVAILABLE") {
		t.Fatalf("unavailable: %d %s", w.Code, w.Body.String())
	}
	useDeepSeekOnly(t)
	valid := func() map[string]any {
		return map[string]any{"revision": b.revision, "queries": []string{"one"}, "limit": 3}
	}
	for name, mutate := range map[string]func(map[string]any){
		"provider":    func(in map[string]any) { in["provider"] = "exa" },
		"supplement":  func(in map[string]any) { in["supplement"] = true },
		"no queries":  func(in map[string]any) { in["queries"] = []string{} },
		"blank query": func(in map[string]any) { in["queries"] = []string{"  "} },
		"too many":    func(in map[string]any) { in["queries"] = make([]string, 13) },
		"limit":       func(in map[string]any) { in["limit"] = 11 },
	} {
		input := valid()
		mutate(input)
		if w := callBrokerWebSearch(b, input); w.Code != 400 {
			t.Fatalf("%s accepted: %d", name, w.Code)
		}
	}
	input := valid()
	input["revision"] = "revoked"
	if w := callBrokerWebSearch(b, input); w.Code != 403 {
		t.Fatalf("revoked revision: %d", w.Code)
	}
	s.alive.Store(false)
	if w := callBrokerWebSearch(b, valid()); w.Code != 403 {
		t.Fatalf("closed session: %d", w.Code)
	}
}

// Run with CC_SIGNALS_WEB_SEARCH_SCRIPT pointing to Skills-OL's
// scripts/signals-web-search.py: the real script talks to the real broker over
// its Unix socket, and the search reaches only an in-process DeepSeek fake.
func TestBrokerWebSearchWithRealSignalsScriptIntegration(t *testing.T) {
	script := os.Getenv("CC_SIGNALS_WEB_SEARCH_SCRIPT")
	if script == "" {
		t.Skip("set CC_SIGNALS_WEB_SEARCH_SCRIPT for the real Signals script contract test")
	}
	useDeepSeekOnly(t)
	calls := fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://acme.com/blog/v2","title":"Acme ships v2","snippet":"Release notes"}]}`)
	b, s := webSearchBrokerFixture(t)
	// Short path: sockaddr_un is limited to about 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "sws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	b.socket = filepath.Join(dir, "cap.sock")
	listener, err := net.Listen("unix", b.socket)
	if err != nil {
		t.Fatal(err)
	}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve)}
	go func() { _ = b.server.Serve(listener) }()
	t.Cleanup(b.close)
	cmd := exec.Command("python3", script, "--queries-json", `["acme launch"]`, "--budget", "1", "--task-id", "cmsg-a1b2c3")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "TOMAKO_SIGNALS_AUDIT_DIR=" + t.TempDir(),
		"TOMAKO_CAPABILITY_SOCKET=" + b.socket, "TOMAKO_CAPABILITY_REVISION=" + b.revision}
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("script failed: %v %s", err, output)
	}
	var result struct {
		SearchEntry string `json:"searchEntry"`
		Batches     []struct {
			Engine  string
			Results []map[string]any
		}
	}
	if err := json.Unmarshal(output, &result); err != nil || result.SearchEntry != "platform" || len(result.Batches) != 1 ||
		result.Batches[0].Engine != "platform:deepseek" || len(result.Batches[0].Results) != 1 ||
		result.Batches[0].Results[0]["url"] != "https://acme.com/blog/v2" {
		t.Fatalf("output=%s err=%v", output, err)
	}
	if len(*calls) != 1 || strings.Contains(string(output), "ds-env") {
		t.Fatalf("calls=%+v", *calls)
	}
	if receipts := drainUsageReceipts(s); len(receipts) != 1 || receipts[0]["entry"] != webSearchEntryBroker {
		t.Fatalf("receipts=%+v", receipts)
	}
}
