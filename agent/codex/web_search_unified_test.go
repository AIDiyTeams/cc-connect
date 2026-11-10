package codex

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// fakeAIsaScript installs a Skills external-data client that answers a Tavily
// search through AIsa only for the expected request id prefix.
func fakeAIsaScript(t *testing.T, s *appServerSession, requestPrefix string) {
	t.Helper()
	s.runtime.ImageCapabilityToken = "fake-image-token"
	path, err := updateTaskRuntimeEnv("", s.runtime)
	if err != nil {
		t.Fatal(err)
	}
	s.taskRuntimeEnvFile = path
	t.Cleanup(func() { removeTaskRuntimeEnv(path) })
	dir := t.TempDir()
	t.Setenv("SKILLS_OL_DIR", dir)
	source := `import {readFileSync} from 'node:fs';
const input=JSON.parse(readFileSync(0,'utf8')), action=process.argv[2];
if(action==='describe') console.log(JSON.stringify({price:{currency:'USD',amount_usd:0.016},contractRevision:'r'}));
else if(action==='execute' && input.requestId.startsWith('` + requestPrefix + `') && input.operationId==='post_tavily_search') console.log(JSON.stringify({status:'SUCCEEDED',resultId:'abc'}));
else if(action==='read') console.log(JSON.stringify({status:'SUCCEEDED',data:[{title:'Secondary',url:'https://example.com/secondary',content:'found'}]}));
else process.exit(3);`
	if err := os.WriteFile(filepath.Join(dir, "tomako-external-data.mjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResearchSearchSupplementFlagAllowsAIsaOnlyWhenRequested(t *testing.T) {
	useDeepSeekOnly(t)
	deepseek := fakeDeepSeekAPI(t, 200, `{"results":[{"url":"https://example.com/primary","title":"p","snippet":"p"}]}`)
	s := webSearchTestSession(t)
	s.runtime.ToolProfile = core.ToolProfilePublicResearch

	// Without the flag an ordinary research turn refuses a supplement.
	if _, err := s.searchWeb(map[string]any{"query": "gap", "supplement": true}); err == nil {
		t.Fatal("supplement accepted without research_search_supplement")
	}
	params := map[string]any{}
	s.applyPublicResearchProfile(params, map[string]any{})
	if supplementAdvertised(params) {
		t.Fatal("supplement advertised without the flag")
	}

	s.runtime.ResearchSearchSupplement = true
	fakeAIsaScript(t, s, "research-search-")
	params = map[string]any{}
	s.applyPublicResearchProfile(params, map[string]any{})
	if !supplementAdvertised(params) || !strings.HasSuffix(s.baseInstructionsOverrideForTest(), researchSupplementMarker) {
		t.Fatal("the flagged research turn did not advertise supplement or pin its profile")
	}
	out, err := s.searchWeb(map[string]any{"query": "gap"})
	if err != nil || !strings.Contains(out, "from deepseek") || len(*deepseek) != 1 {
		t.Fatalf("regular search left the research route: %q %v", out, err)
	}
	out, err = s.searchWeb(map[string]any{"query": "gap", "supplement": true})
	if err != nil || !strings.Contains(out, "aisa/tavily") || !strings.Contains(out, "https://example.com/secondary") || len(*deepseek) != 1 {
		t.Fatalf("supplement: %q %v", out, err)
	}
	// More than one supplement is allowed in the same turn.
	if out, err = s.searchWeb(map[string]any{"query": "another gap", "supplement": true}); err != nil || !strings.Contains(out, "aisa/tavily") {
		t.Fatalf("second supplement: %q %v", out, err)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 3 || receipts[0]["scenario"] != "research" || receipts[1]["scenario"] != "research_supplement" ||
		receipts[1]["provider"] != "aisa" || receipts[2]["scenario"] != "research_supplement" {
		t.Fatalf("receipts=%+v", receipts)
	}
	if _, priced := receipts[1]["cost_usd"]; priced {
		t.Fatal("the AIsa supplement was priced in the search ledger")
	}
}

func TestResearchSearchSupplementFlagIsIgnoredOutsideOrdinaryResearch(t *testing.T) {
	useDeepSeekOnly(t)
	s := webSearchTestSession(t)
	s.runtime.ResearchSearchSupplement = true
	if s.searchSupplementAllowed() {
		t.Fatal("the flag opened a supplement outside public research")
	}
	if _, err := s.searchWeb(map[string]any{"query": "chat", "supplement": true}); err == nil {
		t.Fatal("a conversation accepted a supplement")
	}
	// A first visit keeps its own scenario and request ids.
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	s.runtime.FirstVisitSearch = true
	if s.researchSearchSupplement() || s.webSearchScenario(true) != webSearchScenarioFirstVisitSupplement {
		t.Fatal("the research flag changed first-visit behaviour")
	}
}

func supplementAdvertised(params map[string]any) bool {
	tools, _ := params["dynamicTools"].([]map[string]any)
	for _, tool := range tools {
		if tool["name"] != webSearchToolName {
			continue
		}
		props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		_, ok := props["supplement"]
		return ok
	}
	return false
}

func (s *appServerSession) baseInstructionsOverrideForTest() string {
	s.runtimeMu.Lock()
	s.runtime.DeveloperInstructions = "research"
	s.runtimeMu.Unlock()
	return s.baseInstructionsOverride()
}

// Regression for R1 arm C: every supplement failed as "exit status 1" and the
// reason Portal returned (HTTP 503 from the AIsa quote) was lost.
func TestAIsaSupplementFailureKeepsThePortalReason(t *testing.T) {
	useDeepSeekOnly(t)
	s := webSearchTestSession(t)
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	s.runtime.ResearchSearchSupplement = true
	fakeAIsaScript(t, s, "research-search-")
	failing := `process.stderr.write("第三方数据请求失败 (503): AIsa MCP 工具执行错误\n"); process.exitCode = 1;`
	if err := os.WriteFile(filepath.Join(os.Getenv("SKILLS_OL_DIR"), "tomako-external-data.mjs"), []byte(failing), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.searchWeb(map[string]any{"query": "gap", "supplement": true})
	if err == nil || !strings.Contains(err.Error(), "AIsa describe failed through the task capability: 第三方数据请求失败 (503): AIsa MCP 工具执行错误") {
		t.Fatalf("err=%v", err)
	}
	receipts := drainUsageReceipts(s)
	if len(receipts) != 1 || receipts[0]["error_category"] != "unavailable" || receipts[0]["http_status"] != 503 {
		t.Fatalf("receipts=%+v", receipts)
	}

	refused := `process.stderr.write("第三方数据请求失败 (402): AIsa HTTP 402\n"); process.exitCode = 1;`
	if err := os.WriteFile(filepath.Join(os.Getenv("SKILLS_OL_DIR"), "tomako-external-data.mjs"), []byte(refused), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.searchWeb(map[string]any{"query": "gap two", "supplement": true}); err == nil {
		t.Fatal("a refused supplement succeeded")
	}
	receipts = drainUsageReceipts(s)
	if len(receipts) != 1 || receipts[0]["error_category"] != "payment_required" || receipts[0]["alert"] != true {
		t.Fatalf("an unpaid AIsa account did not raise the payment alert: %+v", receipts)
	}
}
