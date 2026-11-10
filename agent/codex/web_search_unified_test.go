package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type deepSeekCall struct {
	Authorization string
	Body          map[string]any
}

// fakeDeepSeekAPI answers every DeepSeek search with status and body.
func fakeDeepSeekAPI(t *testing.T, status int, body string) *[]deepSeekCall {
	t.Helper()
	var mu sync.Mutex
	calls := []deepSeekCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		calls = append(calls, deepSeekCall{Authorization: r.Header.Get("Authorization"), Body: payload})
		mu.Unlock()
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tools/search" {
			t.Errorf("unexpected DeepSeek request %s %s", r.Method, r.URL.Path)
		}
		respond(status, body)(w)
	}))
	t.Cleanup(server.Close)
	previous := deepSeekSearchURL
	deepSeekSearchURL = server.URL + "/v1/tools/search"
	t.Cleanup(func() { deepSeekSearchURL = previous })
	return &calls
}

// drainUsageReceipts returns the ledger receipts the session emitted so far.
func drainUsageReceipts(s *appServerSession) []map[string]any {
	var receipts []map[string]any
	for {
		select {
		case event := <-s.events:
			if event.Type == core.EventUsageReceipt {
				receipt, _ := event.Metadata["receipt"].(map[string]any)
				receipts = append(receipts, receipt)
			}
		default:
			return receipts
		}
	}
}

func useDeepSeekOnly(t *testing.T) {
	t.Helper()
	t.Setenv(webSearchConfigEnv, "")
	t.Setenv("DEEPSEEK_API_KEY", "ds-env")
}

func TestWebSearchDefaultsToDeepSeekAndRecordsTheLedgerReceipt(t *testing.T) {
	useDeepSeekOnly(t)
	calls := fakeDeepSeekAPI(t, 200, `{"results":[
		{"url":"https://buffer.com/pricing","title":"Buffer Pricing","snippet":"","summary":"Essentials $6"},
		{"url":"","title":"no url"}]}`)
	s := webSearchTestSession(t)
	s.runtime.UserID, s.runtime.TurnNo = "42", 3
	output, err := s.searchWeb(map[string]any{"query": " buffer  pricing ", "limit": float64(5)})
	if err != nil || !strings.Contains(output, "Results: 1 from deepseek") || !strings.Contains(output, "Essentials $6") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	call := (*calls)[0]
	if call.Authorization != "Bearer ds-env" || call.Body["query"] != "buffer pricing" || call.Body["count"] != float64(5) {
		t.Fatalf("call=%+v", call)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 1 {
		t.Fatalf("receipts=%+v", receipts)
	}
	r := receipts[0]
	for key, want := range map[string]any{
		"kind": "web_search", "provider": "deepseek", "scenario": "conversation", "status": "SUCCEEDED",
		"result_count": 1, "cost_usd": 0.00075, "currency": "USD", "workspace_id": "ws-a", "brand_id": "b-a",
		"chat_session_id": "csess-a", "task_id": "cmsg-a", "user_id": "42", "turn_no": 3, "attempt_index": 0,
	} {
		if r[key] != want {
			t.Fatalf("receipt[%s]=%v, want %v (%+v)", key, r[key], want, r)
		}
	}
	if !strings.HasPrefix(r["search_id"].(string), "search-") || strings.Contains(strings.ToLower(r["query_sha256"].(string)), "buffer") {
		t.Fatalf("receipt=%+v", r)
	}

	// A cached answer makes no provider call and records no new cost.
	if _, err := s.searchWeb(map[string]any{"query": "Buffer pricing", "limit": float64(5)}); err != nil || len(*calls) != 1 {
		t.Fatalf("calls=%d err=%v", len(*calls), err)
	}
	if extra := drainUsageReceipts(s); len(extra) != 0 {
		t.Fatalf("a cache hit recorded a receipt: %+v", extra)
	}
}

func TestWebSearchEmptyDeepSeekAnswerCostsNothing(t *testing.T) {
	useDeepSeekOnly(t)
	fakeDeepSeekAPI(t, 200, `{"results":[]}`)
	s := webSearchTestSession(t)
	output, err := s.searchWeb(map[string]any{"query": "zzqx"})
	if err != nil || !strings.Contains(output, "No results") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 1 || receipts[0]["status"] != "EMPTY" || receipts[0]["cost_usd"] != 0.0 {
		t.Fatalf("receipts=%+v", receipts)
	}
}

func TestWebSearchDeepSeekInsufficientBalanceAlertsAndUsesTheConfiguredFallback(t *testing.T) {
	writeWebSearchConfig(t, "DEEPSEEK_API_KEY=ds\nCLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nSEARCH_PROVIDERS=deepseek,exa\n")
	fakeDeepSeekAPI(t, 402, `{"error":{"message":"Insufficient Balance","type":"payment_required"}}`)
	cloudflare := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"exa": respond(200, `{"items":[{"url":"https://example.com/","title":"Source","description":"found"}]}`),
	})
	s := webSearchTestSession(t)
	output, err := s.searchWeb(map[string]any{"query": "fallback"})
	if err != nil || !strings.Contains(output, "from exa") || len(*cloudflare) != 1 {
		t.Fatalf("output=%q err=%v", output, err)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 2 || receipts[0]["error_category"] != "payment_required" || receipts[0]["alert"] != true ||
		receipts[0]["http_status"] != 402 || receipts[0]["cost_usd"] != 0.0 || !strings.Contains(receipts[0]["error"].(string), "Insufficient Balance") {
		t.Fatalf("receipts=%+v", receipts)
	}

	// Alone, an unpaid DeepSeek account is reported as a failure, not as "no results".
	writeWebSearchConfig(t, "DEEPSEEK_API_KEY=ds\n")
	_, err = webSearchTestSession(t).searchWeb(map[string]any{"query": "fallback alone"})
	if err == nil || !strings.Contains(err.Error(), "deepseek returned HTTP 402: Insufficient Balance") {
		t.Fatalf("err=%v", err)
	}
}

func TestWebSearchQueryLimitFollowsTheAnsweringProvider(t *testing.T) {
	useDeepSeekOnly(t)
	calls := fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://example.com/","title":"t","snippet":"s"}]}`)
	s := webSearchTestSession(t)
	long := strings.Repeat("词", 129)
	if _, err := s.searchWeb(map[string]any{"query": long}); err == nil || !strings.Contains(err.Error(), "at most 128 characters") || len(*calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(*calls))
	}
	if _, err := s.searchWeb(map[string]any{"query": strings.Repeat("词", 128)}); err != nil || len(*calls) != 1 {
		t.Fatalf("a 128-character query was refused: %v", err)
	}
	props := s.webSearchDynamicTool()["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if props["query"].(map[string]any)["maxLength"] != 128 {
		t.Fatalf("schema=%+v", props["query"])
	}

	// A provider that takes longer queries answers what DeepSeek cannot.
	writeWebSearchConfig(t, "DEEPSEEK_API_KEY=ds\nCLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nSEARCH_PROVIDERS=deepseek,exa\n")
	cloudflare := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"exa": respond(200, `{"items":[{"url":"https://example.com/long","title":"Long","description":"ok"}]}`),
	})
	output, err := webSearchTestSession(t).searchWeb(map[string]any{"query": long})
	if err != nil || !strings.Contains(output, "from exa") || len(*calls) != 1 || len(*cloudflare) != 1 {
		t.Fatalf("output=%q err=%v deepseek=%d", output, err, len(*calls))
	}
}

func TestWebSearchScenarioOverridesPickTheirOwnChain(t *testing.T) {
	writeWebSearchConfig(t, "DEEPSEEK_API_KEY=ds\nCLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nSEARCH_PROVIDERS_RESEARCH=exa\n")
	deepseek := fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://example.com/d","title":"d","snippet":"d"}]}`)
	cloudflare := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"exa": respond(200, `{"items":[{"url":"https://example.com/e","title":"e","description":"e"}]}`),
	})
	s := webSearchTestSession(t)
	if out, err := s.searchWeb(map[string]any{"query": "conversation query"}); err != nil || !strings.Contains(out, "from deepseek") {
		t.Fatalf("conversation: %q %v", out, err)
	}
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	out, err := s.searchWeb(map[string]any{"query": "research query"})
	if err != nil || !strings.Contains(out, "from exa") || len(*deepseek) != 1 || len(*cloudflare) != 1 {
		t.Fatalf("research: %q %v", out, err)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 2 || receipts[0]["scenario"] != "conversation" || receipts[1]["scenario"] != "research" {
		t.Fatalf("receipts=%+v", receipts)
	}
}

func TestWebSearchFirstVisitUsesDeepSeekFromTheUnifiedEntry(t *testing.T) {
	useDeepSeekOnly(t)
	calls := fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://neustadt.fr/","title":"Parimal","snippet":"UX designer"}]}`)
	s := webSearchTestSession(t)
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	s.runtime.FirstVisitSearch = true
	if !s.webSearchToolAvailable() {
		t.Fatal("first visit did not get the search tool")
	}
	out, err := s.searchWeb(map[string]any{"query": "Parimal Satyal designer"})
	if err != nil || !strings.Contains(out, "from deepseek") || len(*calls) != 1 {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if receipts := drainUsageReceipts(s); len(receipts) != 1 || receipts[0]["scenario"] != "first_visit" {
		t.Fatalf("receipts=%+v", receipts)
	}
}
