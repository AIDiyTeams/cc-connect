package codex

import (
	"context"
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

func writeWebSearchConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web-search.env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(webSearchConfigEnv, path)
}

func webSearchTestSession(t *testing.T) *appServerSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &appServerSession{ctx: ctx, cancel: cancel, workDir: t.TempDir(), events: make(chan core.Event, 4),
		runtime: core.SessionRuntime{TaskID: "cmsg-a", WorkspaceID: "ws-a", BrandID: "b-a", ChatSessionID: "csess-a"}}
	s.alive.Store(true)
	return s
}

type webSearchCall struct {
	Path          string
	Authorization string
	Body          map[string]any
}

// fakeWebSearchAPI answers each provider with the status and body given for it.
func fakeWebSearchAPI(t *testing.T, answers map[string]func(w http.ResponseWriter)) *[]webSearchCall {
	t.Helper()
	var mu sync.Mutex
	calls := []webSearchCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		calls = append(calls, webSearchCall{Path: r.Method + " " + r.URL.Path, Authorization: r.Header.Get("Authorization"), Body: body})
		mu.Unlock()
		provider, _ := body["provider"].(string)
		answer, ok := answers[provider]
		if !ok {
			t.Errorf("unexpected provider %q", provider)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		answer(w)
	}))
	t.Cleanup(server.Close)
	previous := webSearchAPIBase
	webSearchAPIBase = server.URL + "/client/v4"
	t.Cleanup(func() { webSearchAPIBase = previous })
	return &calls
}

func respond(status int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func advertisesWebSearch(s *appServerSession) bool {
	tools, _ := s.threadRequestParams()["dynamicTools"].([]map[string]any)
	for _, tool := range tools {
		if tool["name"] == webSearchToolName {
			return true
		}
	}
	return false
}

func TestWebSearchConfigurationReadsTheCredentialsFile(t *testing.T) {
	writeWebSearchConfig(t, "# Cloudflare Web Search\nexport CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=\"tok-1\"\n\n")
	config, err := loadWebSearchConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.AccountID != "acc-1" || config.Token != "tok-1" || config.GatewayID != "default" ||
		strings.Join(config.Providers, ",") != "ceramic,linkup" {
		t.Fatalf("config=%+v", config)
	}

	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nCLOUDFLARE_AI_GATEWAY_ID=search\nWEB_SEARCH_PROVIDERS= exa , ceramic\n")
	config, err = loadWebSearchConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.GatewayID != "search" || strings.Join(config.Providers, ",") != "exa,ceramic" {
		t.Fatalf("config=%+v", config)
	}

	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\n")
	if _, err := loadWebSearchConfig(); err == nil {
		t.Fatal("a configuration without a token was accepted")
	}
	t.Setenv(webSearchConfigEnv, "")
	if _, err := loadWebSearchConfig(); err == nil {
		t.Fatal("search was configured without a credentials file")
	}
}

func TestWebSearchToolOfferedToConversationsOnlyWhenConfigured(t *testing.T) {
	s := webSearchTestSession(t)
	t.Setenv(webSearchConfigEnv, "")
	if advertisesWebSearch(s) {
		t.Fatal("the tool was offered without credentials")
	}
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\n")
	if !advertisesWebSearch(s) {
		t.Fatal("a configured conversation did not get the tool")
	}
	s.runtime.ChatSessionID = ""
	if advertisesWebSearch(s) {
		t.Fatal("a background task without a conversation received the tool")
	}
	s.runtime.ChatSessionID = "csess-a"
	s.runtime.Scene = "brand_analysis"
	if advertisesWebSearch(s) {
		t.Fatal("the dedicated brand-analysis tool set changed")
	}
}

func TestWebSearchSendsTheDocumentedRequest(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\n")
	calls := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"ceramic": respond(200, `{"success":true,"errors":[],"result":{"items":[
			{"url":"https://buffer.com/pricing","title":"Buffer  Pricing","description":"Essentials $6 per month per channel."},
			{"url":"https://example.com/review","title":"","description":""}],
			"metadata":{"query":"buffer pricing","requestId":"r-1","latencyMs":412}}}`),
	})
	output, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "  buffer\n pricing ", "limit": float64(25)})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls=%+v", *calls)
	}
	call := (*calls)[0]
	if call.Path != "POST /client/v4/accounts/acc-1/ai/websearch/" || call.Authorization != "Bearer tok-1" {
		t.Fatalf("call=%+v", call)
	}
	gateway, _ := call.Body["options"].(map[string]any)["gateway"].(map[string]any)
	if call.Body["query"] != "buffer pricing" || call.Body["provider"] != "ceramic" || call.Body["limit"] != float64(webSearchMaxLimit) || gateway["id"] != "default" {
		t.Fatalf("body=%+v", call.Body)
	}
	for _, want := range []string{"untrusted content", "Query: buffer pricing", "Results: 2 from ceramic in 412 ms",
		"1. Buffer Pricing\n   https://buffer.com/pricing\n   Essentials $6 per month per channel.", "2. (untitled)", webReadToolName} {
		if !strings.Contains(output, want) {
			t.Fatalf("output lacks %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "tok-1") {
		t.Fatal("the token reached the Agent")
	}
}

func TestWebSearchTriesTheNextProviderWhenOneIsUnavailableOrEmpty(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nWEB_SEARCH_PROVIDERS=ceramic,exa,linkup\n")
	calls := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"ceramic": respond(503, `{"success":false,"errors":[{"code":7000,"message":"upstream unavailable"}]}`),
		"exa":     respond(200, `{"items":[],"metadata":{}}`),
		"linkup":  respond(200, `{"items":[{"url":"https://example.com/a","title":"A","description":"found"}],"metadata":{}}`),
	})
	output, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "niche query"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 || !strings.Contains(output, "Results: 1 from linkup") {
		t.Fatalf("calls=%d output=%s", len(*calls), output)
	}
	if (*calls)[0].Body["limit"] != float64(webSearchDefaultLimit) {
		t.Fatalf("limit=%v", (*calls)[0].Body["limit"])
	}
}

func TestWebSearchStopsOnCredentialErrors(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\n")
	calls := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"ceramic": respond(401, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`),
		"linkup":  respond(200, `{"items":[{"url":"https://example.com/","title":"x","description":"y"}]}`),
	})
	_, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "pricing"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401: Authentication error") {
		t.Fatalf("err=%v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("a credential failure was retried with another provider: %d calls", len(*calls))
	}
}

func TestWebSearchReportsNoResultsWithoutFailing(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nWEB_SEARCH_PROVIDERS=ceramic,exa,linkup\n")
	fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"ceramic": respond(200, `{"items":[]}`),
		"exa":     respond(502, `bad gateway`),
		"linkup":  respond(400, `{"success":false,"errors":[{"message":"provider is not enabled"}]}`),
	})
	output, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "zzqx unknown"})
	if err != nil || !strings.Contains(output, "No results") {
		t.Fatalf("output=%q err=%v", output, err)
	}
}

func TestWebSearchReportsFailureWhenNoProviderAnswers(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\n")
	fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"ceramic": respond(429, `{"success":false,"errors":[{"message":"rate limited"}]}`),
		"linkup":  respond(500, `not json`),
	})
	_, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "pricing"})
	if err == nil || !strings.Contains(err.Error(), "ceramic returned HTTP 429: rate limited") || !strings.Contains(err.Error(), "linkup returned HTTP 500") {
		t.Fatalf("err=%v", err)
	}
}

func TestWebSearchRejectsQueriesTheAPIWouldRefuse(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\n")
	calls := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){})
	s := webSearchTestSession(t)
	for _, query := range []any{"", "   ", nil, strings.Repeat("词", webSearchMaxQueryRunes+1)} {
		if _, err := s.searchWeb(map[string]any{"query": query}); err == nil {
			t.Fatalf("query %v was accepted", query)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("calls=%d", len(*calls))
	}
}

func TestWebSearchKeepsSnippetsWithinTheToolBudget(t *testing.T) {
	items := []webSearchItem{}
	for range webSearchMaxLimit {
		items = append(items, webSearchItem{URL: "https://example.com/", Title: "Title", Description: strings.Repeat("长", 5000)})
	}
	output := formatWebSearchResults("q", "ceramic", &webSearchResponse{Items: items})
	if !strings.Contains(output, strings.Repeat("长", webSearchDescriptionRunes)+"...") || strings.Contains(output, strings.Repeat("长", webSearchDescriptionRunes+1)) {
		t.Fatal("snippets were not cut at the per-result limit")
	}
	if !strings.Contains(output, "10. Title") || len([]rune(output)) > webSearchMaxLimit*(webSearchDescriptionRunes+100)+400 {
		t.Fatalf("output has %d runes", len([]rune(output)))
	}
}

func TestWebSearchShowsTheQueryInTheConversation(t *testing.T) {
	s := webSearchTestSession(t)
	s.handleItemStarted(map[string]any{"type": "dynamicToolCall", "id": "item-1", "tool": webSearchToolName,
		"arguments": map[string]any{"query": "buffer  pricing 2026"}})
	event := <-s.events
	if event.PublicActivity == nil || event.PublicActivity.Kind != "search" || event.PublicActivity.Status != "running" ||
		event.PublicActivity.Query != "buffer pricing 2026" {
		t.Fatalf("activity=%+v", event.PublicActivity)
	}
	s.handleItemCompleted(map[string]any{"type": "dynamicToolCall", "id": "item-1", "tool": webSearchToolName, "status": "completed",
		"arguments": `{"query":"buffer pricing 2026"}`, "contentItems": []any{}})
	event = <-s.events
	if event.PublicActivity == nil || event.PublicActivity.Query != "buffer pricing 2026" || event.PublicActivity.Status != "returned" {
		t.Fatalf("activity=%+v", event.PublicActivity)
	}
}
