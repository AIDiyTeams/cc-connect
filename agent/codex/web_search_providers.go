package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Provider API roots; tests point them at a local server.
var (
	webSearchAPIBase   = "https://api.cloudflare.com/client/v4"
	braveSearchAPIBase = "https://api.search.brave.com/res/v1"
	deepSeekSearchURL  = "https://api.deepseek.com/v1/tools/search"
)

// webSearchQueryLimit is the longest query, in characters, a provider accepts.
// A longer query skips that provider instead of being cut; the Agent is told
// the limit so it can write a shorter, focused query.
func webSearchQueryLimit(provider string) int {
	switch provider {
	case webSearchProviderDeepSeek:
		return 128 // https://api-docs.deepseek.com/guides/search/
	case webSearchProviderAIsa:
		return 400 // Tavily's documented query limit
	default:
		return webSearchMaxQueryRunes
	}
}

// webSearchListPrices are the published prices per billed search in USD.
// DeepSeek bills only HTTP 200 responses with at least one result
// ($0.75 per 1,000, checked 2026-11-10). Providers without a verified price
// here record an unknown cost unless SEARCH_PRICE_USD_<PROVIDER> sets one.
var webSearchListPrices = map[string]float64{
	webSearchProviderDeepSeek: 0.00075,
}

const webSearchPricingBasis = "web-search-pricing/2026-11-10"

// webSearchAttemptCost returns the procurement cost of one attempt, or nil
// when it is not known. AIsa's actual charge is settled in Portal's
// external-data execution ledger, so the search ledger never repeats it.
func webSearchAttemptCost(config webSearchConfig, provider string, status webSearchStatus) *float64 {
	if provider == webSearchProviderAIsa {
		return nil
	}
	zero := 0.0
	price, configured := config.Prices[provider]
	if !configured {
		price, configured = webSearchListPrices[provider]
	}
	switch {
	case status == webSearchStatusSucceeded && configured:
		return &price
	case provider == webSearchProviderDeepSeek && status != webSearchStatusSucceeded:
		return &zero // DeepSeek does not bill failed or empty searches
	default:
		return nil
	}
}

// webSearchCategory classifies an attempt failure. Payment, credential and
// configuration failures raise an alert; every category except a bad request
// lets the next configured provider try.
type webSearchCategory string

const (
	webSearchPaymentRequired    webSearchCategory = "payment_required"
	webSearchCredentialsInvalid webSearchCategory = "credentials_invalid"
	webSearchNotConfigured      webSearchCategory = "not_configured"
	webSearchRateLimited        webSearchCategory = "rate_limited"
	webSearchUnavailable        webSearchCategory = "unavailable"
	webSearchBadRequest         webSearchCategory = "bad_request"
	webSearchQueryTooLong       webSearchCategory = "query_too_long"
	webSearchUnreadable         webSearchCategory = "unreadable"
)

func (c webSearchCategory) alerts() bool {
	return c == webSearchPaymentRequired || c == webSearchCredentialsInvalid || c == webSearchNotConfigured
}

type webSearchAttemptError struct {
	provider   string
	category   webSearchCategory
	httpStatus int
	message    string
}

func (e *webSearchAttemptError) Error() string { return e.message }

// retryable reports whether another provider may succeed where this one failed.
func (e *webSearchAttemptError) retryable() bool { return e.category != webSearchBadRequest }

func attemptError(provider string, category webSearchCategory, status int, format string, args ...any) *webSearchAttemptError {
	return &webSearchAttemptError{provider: provider, category: category, httpStatus: status, message: fmt.Sprintf(format, args...)}
}

func webSearchHTTPCategory(status int) webSearchCategory {
	switch {
	case status == http.StatusPaymentRequired:
		return webSearchPaymentRequired
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return webSearchCredentialsInvalid
	case status == http.StatusTooManyRequests:
		return webSearchRateLimited
	case status == http.StatusRequestTimeout || status >= 500:
		return webSearchUnavailable
	default:
		return webSearchBadRequest
	}
}

func httpAttemptError(provider string, status int, reason string) *webSearchAttemptError {
	if strings.TrimSpace(reason) == "" {
		reason = http.StatusText(status)
	}
	return attemptError(provider, webSearchHTTPCategory(status), status, "%s returned HTTP %d: %s", provider, status, limitRunes(reason, 200))
}

// webSearchAttempt calls one provider. Every provider returns the same
// response shape and a classified *webSearchAttemptError.
func (s *appServerSession) webSearchAttempt(config webSearchConfig, provider, query string, limit int) (*webSearchResponse, error) {
	if max := webSearchQueryLimit(provider); len([]rune(query)) > max {
		return nil, attemptError(provider, webSearchQueryTooLong, 0, "%s accepts queries of at most %d characters", provider, max)
	}
	if !config.hasCredentials(provider) {
		return nil, attemptError(provider, webSearchNotConfigured, 0, "%s has no credentials configured", provider)
	}
	switch provider {
	case webSearchProviderDeepSeek:
		return deepSeekSearchAttempt(s.ctx, config, query, limit)
	case webSearchProviderBrave:
		return braveSearchAttempt(s.ctx, config, query, limit)
	case webSearchProviderAIsa:
		return s.supplementFirstVisitSearch(query, limit)
	default:
		return cloudflareSearchAttempt(s.ctx, config, provider, query, limit)
	}
}

// sendWebSearchRequest returns the response body and status, or a classified
// attempt error that lets the caller try the next provider.
func sendWebSearchRequest(ctx, parent context.Context, request *http.Request, provider string) ([]byte, int, error) {
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx.Err() != nil && parent.Err() == nil {
			return nil, 0, attemptError(provider, webSearchUnavailable, 0, "%s timed out", provider)
		}
		return nil, 0, attemptError(provider, webSearchUnavailable, 0, "%s was unreachable", provider)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, webSearchResponseLimit))
	if err != nil {
		return nil, 0, attemptError(provider, webSearchUnavailable, 0, "%s response was cut off", provider)
	}
	return raw, response.StatusCode, nil
}

func deepSeekSearchAttempt(parent context.Context, config webSearchConfig, query string, limit int) (*webSearchResponse, error) {
	body, err := json.Marshal(map[string]any{"query": query, "count": limit})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, webSearchAttemptTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, deepSeekSearchURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+config.DeepSeekKey)
	request.Header.Set("Content-Type", "application/json")
	raw, status, err := sendWebSearchRequest(ctx, parent, request, webSearchProviderDeepSeek)
	if err != nil {
		return nil, err
	}
	var result struct {
		Results *[]struct{ URL, Title, Snippet, Summary string }
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	parseErr := json.Unmarshal(raw, &result)
	if status != http.StatusOK {
		return nil, httpAttemptError(webSearchProviderDeepSeek, status, result.Error.Message)
	}
	if parseErr != nil || result.Results == nil {
		return nil, attemptError(webSearchProviderDeepSeek, webSearchUnreadable, status, "deepseek returned unreadable results")
	}
	response := &webSearchResponse{}
	for _, item := range *result.Results {
		snippet := item.Snippet
		if snippet == "" {
			snippet = item.Summary
		}
		if item.URL != "" {
			response.Items = append(response.Items, webSearchItem{item.URL, item.Title, snippet})
		}
		if len(response.Items) >= limit {
			break
		}
	}
	return response, nil
}

func braveSearchAttempt(parent context.Context, config webSearchConfig, query string, limit int) (*webSearchResponse, error) {
	ctx, cancel := context.WithTimeout(parent, webSearchAttemptTimeout)
	defer cancel()
	endpoint := braveSearchAPIBase + "/web/search?" + url.Values{"q": {query}, "count": {strconv.Itoa(limit)}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Subscription-Token", config.BraveKey)
	raw, status, err := sendWebSearchRequest(ctx, parent, request, webSearchProviderBrave)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Web struct {
			Results []webSearchItem `json:"results"`
		} `json:"web"`
		Error struct {
			Detail string `json:"detail"`
		} `json:"error"`
	}
	parseErr := json.Unmarshal(raw, &payload)
	if status < 200 || status > 299 {
		return nil, httpAttemptError(webSearchProviderBrave, status, payload.Error.Detail)
	}
	if parseErr != nil {
		return nil, attemptError(webSearchProviderBrave, webSearchUnreadable, status, "brave returned an unreadable response")
	}
	// Brave marks the matched words with <strong> in titles and snippets.
	response := &webSearchResponse{}
	for _, item := range payload.Web.Results {
		item.Title = htmlTags.ReplaceAllString(item.Title, "")
		item.Description = htmlTags.ReplaceAllString(item.Description, "")
		response.Items = append(response.Items, item)
	}
	return response, nil
}

func cloudflareSearchAttempt(parent context.Context, config webSearchConfig, provider, query string, limit int) (*webSearchResponse, error) {
	body, err := json.Marshal(map[string]any{
		"query":    query,
		"provider": provider,
		"limit":    limit,
		"options":  map[string]any{"gateway": map[string]any{"id": config.GatewayID}},
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, webSearchAttemptTimeout)
	defer cancel()
	endpoint := webSearchAPIBase + "/accounts/" + url.PathEscape(config.AccountID) + "/ai/websearch/"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+config.Token)
	request.Header.Set("Content-Type", "application/json")
	raw, status, err := sendWebSearchRequest(ctx, parent, request, provider)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if status < 200 || status > 299 {
		reason := ""
		if len(envelope.Errors) > 0 {
			reason = envelope.Errors[0].Message
		}
		return nil, httpAttemptError(provider, status, reason)
	}
	payload := raw
	if len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		payload = envelope.Result
	}
	var parsed webSearchResponse
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, attemptError(provider, webSearchUnreadable, status, "%s returned an unreadable response", provider)
	}
	return &parsed, nil
}
