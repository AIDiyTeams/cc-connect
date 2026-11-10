package codex

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"strings"
	"sync"
)

// The Signals and growth Skills run their search batches from a script in the
// Agent shell, which never sees a search credential. This broker route runs
// such a batch through lookupWeb, the single platform search entry, so it gets
// the same provider chain, cache, slots, ledger receipts and alerts as the
// Agent's own tomako_web_search. See docs/architecture/web-search.md.
const (
	brokerWebSearchMaxQueries   = 12
	brokerWebSearchConcurrency  = 4
	brokerWebSearchTitleRunes   = 300
	brokerWebSearchSnippetRunes = 1000
)

type brokerWebSearchRequest struct {
	Revision string   `json:"revision"`
	Queries  []string `json:"queries"`
	Limit    int      `json:"limit"`
}

type brokerWebSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type brokerWebSearchBatch struct {
	Query    string                  `json:"query"`
	Provider string                  `json:"provider,omitempty"`
	Cached   bool                    `json:"cached,omitempty"`
	Results  []brokerWebSearchResult `json:"results"`
	Error    string                  `json:"error,omitempty"`
}

// brokerWebSearchError carries a code the script can act on, without any
// provider, credential or configuration detail.
func brokerWebSearchError(w http.ResponseWriter, status int, code, message string, extra map[string]any) {
	body := map[string]any{"code": code, "message": message}
	for key, value := range extra {
		body[key] = value
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (b *capabilityBroker) serveWebSearch(w http.ResponseWriter, r *http.Request) {
	var input brokerWebSearchRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.Queries) == 0 ||
		len(input.Queries) > brokerWebSearchMaxQueries || input.Limit < 1 || input.Limit > webSearchMaxLimit {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	queries := make([]string, len(input.Queries))
	for index, raw := range input.Queries {
		queries[index] = strings.Join(strings.Fields(raw), " ")
		if queries[index] == "" || len([]rune(queries[index])) > webSearchMaxQueryRunes {
			brokerReject(w, http.StatusBadRequest)
			return
		}
	}
	b.session.runtimeMu.RLock()
	runtime, revision, alive := b.session.runtime, b.revision, b.session.alive.Load()
	b.session.runtimeMu.RUnlock()
	// Every receipt must be attributable to the current task and product.
	if !alive || revision == "" || input.Revision != revision || !taskRuntimeIDPattern.MatchString(runtime.TaskID) ||
		!taskRuntimeIDPattern.MatchString(runtime.WorkspaceID) || !taskRuntimeIDPattern.MatchString(runtime.BrandID) {
		brokerReject(w, http.StatusForbidden)
		return
	}
	config, err := loadWebSearchConfig()
	var chain []string
	if err == nil {
		// A script batch never reaches the AIsa supplement: that is billed through a
		// task capability only the Agent's own tool may decide to use.
		chain = config.usableChain(b.session.webSearchScenario(false), false)
	}
	if len(chain) == 0 {
		brokerWebSearchError(w, http.StatusServiceUnavailable, "WEB_SEARCH_UNAVAILABLE",
			"平台网页搜索当前不可用，这批查询没有执行。请如实告诉用户研究缺少联网搜索结果。", nil)
		return
	}
	// Validate the whole batch before any billed call, so a query the providers
	// refuse costs nothing and the script can let the Agent rewrite it.
	maxChars := 0
	for _, provider := range chain {
		maxChars = max(maxChars, webSearchQueryLimit(provider))
	}
	var tooLong []int
	for index, query := range queries {
		if len([]rune(query)) > maxChars {
			tooLong = append(tooLong, index)
		}
	}
	if len(tooLong) > 0 {
		brokerWebSearchError(w, http.StatusBadRequest, "QUERY_TOO_LONG",
			"有查询超过平台搜索的长度上限，这批查询没有执行；请把它们改写成更短、更聚焦的查询后重试。",
			map[string]any{"maxChars": maxChars, "queryIndexes": tooLong})
		return
	}

	batches := make([]brokerWebSearchBatch, len(queries))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(brokerWebSearchConcurrency, len(queries)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range next {
				batches[index] = b.session.brokerWebSearchBatch(queries[index], input.Limit)
			}
		}()
	}
	for index := range queries {
		next <- index
	}
	close(next)
	wg.Wait()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"batches": batches})
}

func (s *appServerSession) brokerWebSearchBatch(query string, limit int) brokerWebSearchBatch {
	batch := brokerWebSearchBatch{Query: query, Results: []brokerWebSearchResult{}}
	lookup, err := s.runWebSearch(webSearchEntryBroker, query, limit, false)
	if err != nil {
		batch.Error = err.Error()
		return batch
	}
	if lookup.response == nil {
		return batch
	}
	batch.Provider, batch.Cached = webSearchProviderLabel(lookup.provider), lookup.cached
	for _, item := range lookup.response.Items {
		link := strings.TrimSpace(item.URL)
		if !strings.HasPrefix(link, "https://") && !strings.HasPrefix(link, "http://") {
			continue
		}
		batch.Results = append(batch.Results, brokerWebSearchResult{
			Title:   limitRunes(strings.Join(strings.Fields(html.UnescapeString(item.Title)), " "), brokerWebSearchTitleRunes),
			URL:     link,
			Snippet: truncate(strings.Join(strings.Fields(html.UnescapeString(item.Description)), " "), brokerWebSearchSnippetRunes),
		})
	}
	return batch
}
