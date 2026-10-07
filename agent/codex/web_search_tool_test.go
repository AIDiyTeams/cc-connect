package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func resetWebSearchCache() {
	webSearchCache.Lock()
	webSearchCache.entries = map[string]webSearchCacheEntry{}
	webSearchCache.Unlock()
}

func webSearchTestSession(t *testing.T) *appServerSession {
	t.Helper()
	resetWebSearchCache()
	t.Cleanup(resetWebSearchCache)
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
		strings.Join(config.Providers, ",") != "exa,linkup" {
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

	writeWebSearchConfig(t, "BRAVE_API_KEY=brave-1\nWEB_SEARCH_PROVIDERS=brave\n")
	config, err = loadWebSearchConfig()
	if err != nil || config.BraveKey != "brave-1" || strings.Join(config.Providers, ",") != "brave" {
		t.Fatalf("Brave alone did not configure search: %+v %v", config, err)
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
		"exa": respond(200, `{"success":true,"errors":[],"result":{"items":[
			{"url":"https://buffer.com/pricing","title":"Buffer  Pricing &amp; Plans","description":"Essentials $6 per month per channel &#8211; billed yearly."},
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
	if call.Body["query"] != "buffer pricing" || call.Body["provider"] != "exa" || call.Body["limit"] != float64(webSearchMaxLimit) || gateway["id"] != "default" {
		t.Fatalf("body=%+v", call.Body)
	}
	for _, want := range []string{"untrusted content", "Query: buffer pricing", "Results: 2 from exa in 412 ms",
		"1. Buffer Pricing & Plans\n   https://buffer.com/pricing\n   Essentials $6 per month per channel \u2013 billed yearly.", "2. (untitled)", webReadToolName} {
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
		"exa":    respond(401, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`),
		"linkup": respond(200, `{"items":[{"url":"https://example.com/","title":"x","description":"y"}]}`),
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
		"exa":    respond(429, `{"success":false,"errors":[{"message":"rate limited"}]}`),
		"linkup": respond(500, `not json`),
	})
	_, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "pricing"})
	if err == nil || !strings.Contains(err.Error(), "exa returned HTTP 429: rate limited") || !strings.Contains(err.Error(), "linkup returned HTTP 500") {
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

type braveCall struct {
	Path, Query, Count, Token, Authorization string
}

// fakeBraveAPI answers every Brave search with status and body.
func fakeBraveAPI(t *testing.T, status int, body string) *[]braveCall {
	t.Helper()
	var mu sync.Mutex
	calls := []braveCall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, braveCall{Path: r.Method + " " + r.URL.Path, Query: r.URL.Query().Get("q"), Count: r.URL.Query().Get("count"),
			Token: r.Header.Get("X-Subscription-Token"), Authorization: r.Header.Get("Authorization")})
		mu.Unlock()
		respond(status, body)(w)
	}))
	t.Cleanup(server.Close)
	previous := braveSearchAPIBase
	braveSearchAPIBase = server.URL + "/res/v1"
	t.Cleanup(func() { braveSearchAPIBase = previous })
	return &calls
}

func TestWebSearchCallsBraveDirectly(t *testing.T) {
	writeWebSearchConfig(t, "BRAVE_API_KEY=brave-key\nWEB_SEARCH_PROVIDERS=brave\n")
	calls := fakeBraveAPI(t, 200, `{"web":{"results":[
		{"title":"Buffer <strong>Pricing</strong> &amp; Plans","url":"https://buffer.com/pricing","description":"Start for <strong>free</strong>, Essentials $6 per channel."},
		{"title":"Buffer review","url":"https://example.com/review","description":""}]}}`)
	output, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "buffer pricing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls=%+v", *calls)
	}
	call := (*calls)[0]
	if call.Path != "GET /res/v1/web/search" || call.Query != "buffer pricing" || call.Count != "8" || call.Token != "brave-key" || call.Authorization != "" {
		t.Fatalf("call=%+v", call)
	}
	for _, want := range []string{"Results: 2 from brave", "1. Buffer Pricing & Plans\n   https://buffer.com/pricing\n   Start for free, Essentials $6 per channel."} {
		if !strings.Contains(output, want) {
			t.Fatalf("output lacks %q:\n%s", want, output)
		}
	}
}

func TestWebSearchMovesFromBraveToCloudflareWhenBraveCannotAnswer(t *testing.T) {
	writeWebSearchConfig(t, "BRAVE_API_KEY=brave-key\nCLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nWEB_SEARCH_PROVIDERS=brave,exa\n")
	braveCalls := fakeBraveAPI(t, 429, `{"type":"ErrorResponse","error":{"code":"RATE_LIMITED","detail":"Request rate limit exceeded"}}`)
	cloudflareCalls := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"exa": respond(200, `{"items":[{"url":"https://buffer.com/pricing","title":"Pricing","description":"plans"}]}`),
	})
	output, err := webSearchTestSession(t).searchWeb(map[string]any{"query": "buffer pricing"})
	if err != nil || !strings.Contains(output, "Results: 1 from exa") {
		t.Fatalf("output=%q err=%v", output, err)
	}
	if len(*braveCalls) != 1 || len(*cloudflareCalls) != 1 {
		t.Fatalf("brave=%d cloudflare=%d", len(*braveCalls), len(*cloudflareCalls))
	}

	// Without a Brave key the provider is skipped rather than failing the search.
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\nWEB_SEARCH_PROVIDERS=brave,exa\n")
	output, err = webSearchTestSession(t).searchWeb(map[string]any{"query": "buffer pricing"})
	if err != nil || !strings.Contains(output, "from exa") || len(*braveCalls) != 1 {
		t.Fatalf("output=%q err=%v brave=%d", output, err, len(*braveCalls))
	}
}

func TestWebSearchReusesARecentSearch(t *testing.T) {
	writeWebSearchConfig(t, "CLOUDFLARE_ACCOUNT_ID=acc-1\nCLOUDFLARE_API_TOKEN=tok-1\n")
	calls := fakeWebSearchAPI(t, map[string]func(http.ResponseWriter){
		"exa": respond(200, `{"items":[{"url":"https://buffer.com/pricing","title":"Pricing","description":"plans"}],"metadata":{"latencyMs":300}}`),
	})
	s := webSearchTestSession(t)
	if _, err := s.searchWeb(map[string]any{"query": "Buffer pricing"}); err != nil {
		t.Fatal(err)
	}
	output, err := s.searchWeb(map[string]any{"query": "buffer   PRICING"})
	if err != nil || len(*calls) != 1 || !strings.Contains(output, "Results: 1 from exa, searched 0s ago") || strings.Contains(output, "300 ms") {
		t.Fatalf("calls=%d output=%q err=%v", len(*calls), output, err)
	}
	if _, err := s.searchWeb(map[string]any{"query": "Buffer pricing", "limit": float64(3)}); err != nil || len(*calls) != 2 {
		t.Fatalf("a different result count reused the cache: calls=%d err=%v", len(*calls), err)
	}

	key := webSearchCacheKey("buffer pricing", webSearchDefaultLimit)
	webSearchCache.Lock()
	entry := webSearchCache.entries[key]
	entry.at = time.Now().Add(-webSearchCacheTTL - time.Minute)
	webSearchCache.entries[key] = entry
	webSearchCache.Unlock()
	if _, err := s.searchWeb(map[string]any{"query": "Buffer pricing"}); err != nil || len(*calls) != 3 {
		t.Fatalf("an expired search was reused: calls=%d err=%v", len(*calls), err)
	}
}

func TestWebSearchCacheStaysBounded(t *testing.T) {
	resetWebSearchCache()
	t.Cleanup(resetWebSearchCache)
	start := time.Now()
	for i := 0; i < webSearchCacheEntries+20; i++ {
		rememberWebSearch(fmt.Sprintf("q%d", i), webSearchCacheEntry{provider: "exa", at: start.Add(time.Duration(i) * time.Second)})
	}
	webSearchCache.Lock()
	size := len(webSearchCache.entries)
	_, oldestKept := webSearchCache.entries["q0"]
	_, newestKept := webSearchCache.entries[fmt.Sprintf("q%d", webSearchCacheEntries+19)]
	webSearchCache.Unlock()
	if size != webSearchCacheEntries || oldestKept || !newestKept {
		t.Fatalf("size=%d oldestKept=%v newestKept=%v", size, oldestKept, newestKept)
	}
}
