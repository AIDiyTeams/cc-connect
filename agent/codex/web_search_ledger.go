package codex

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// webSearchStatus is the outcome of one provider attempt as recorded in the
// search ledger.
type webSearchStatus string

const (
	webSearchStatusSucceeded webSearchStatus = "SUCCEEDED"
	webSearchStatusEmpty     webSearchStatus = "EMPTY"
	webSearchStatusFailed    webSearchStatus = "FAILED"
)

// webSearchUsageKind tells Portal which ledger a usage receipt belongs to.
const webSearchUsageKind = "web_search"

type webSearchAttemptRecord struct {
	searchID string
	index    int
	scenario webSearchScenario
	provider string
	query    string
	status   webSearchStatus
	results  int
	latency  time.Duration
	err      *webSearchAttemptError
	costUSD  *float64
	occurred time.Time
}

func newWebSearchID() string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "search-" + strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")
	}
	return "search-" + hex.EncodeToString(raw)
}

// recordWebSearchAttempt sends one usage receipt per provider attempt to the
// control plane, which owns the ledger (llm_inference_usage with
// endpoint_mode=web_search), and raises an alert for payment, credential and
// configuration failures. A cache hit makes no attempt and has no receipt.
func (s *appServerSession) recordWebSearchAttempt(record webSearchAttemptRecord) {
	s.runtimeMu.RLock()
	runtime := s.runtime
	s.runtimeMu.RUnlock()
	digest := sha256.Sum256([]byte(strings.ToLower(record.query)))
	receipt := map[string]any{
		"kind":            webSearchUsageKind,
		"search_id":       record.searchID,
		"attempt_index":   record.index,
		"scenario":        string(record.scenario),
		"provider":        record.provider,
		"status":          string(record.status),
		"result_count":    record.results,
		"latency_ms":      record.latency.Milliseconds(),
		"query_sha256":    hex.EncodeToString(digest[:]),
		"query_chars":     len([]rune(record.query)),
		"pricing_basis":   webSearchPricingBasis,
		"currency":        "USD",
		"occurred_at":     record.occurred.UTC().Format(time.RFC3339Nano),
		"workspace_id":    runtime.WorkspaceID,
		"brand_id":        runtime.BrandID,
		"user_id":         runtime.UserID,
		"chat_session_id": runtime.ChatSessionID,
		"task_id":         runtime.TaskID,
		"turn_no":         runtime.TurnNo,
	}
	if record.costUSD != nil {
		receipt["cost_usd"] = *record.costUSD
	}
	if record.err != nil {
		receipt["error_category"] = string(record.err.category)
		receipt["error"] = limitRunes(record.err.message, 300)
		if record.err.httpStatus > 0 {
			receipt["http_status"] = record.err.httpStatus
		}
		if record.err.category.alerts() {
			receipt["alert"] = true
			raiseWebSearchAlert(record.provider, record.err)
		}
	}
	s.emit(core.Event{Type: core.EventUsageReceipt, Metadata: map[string]any{"receipt": receipt}})
}

const webSearchAlertInterval = 10 * time.Minute

var webSearchAlerts = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// raiseWebSearchAlert logs one error per provider and category every ten
// minutes. Portal raises the owner-facing alert from the same receipt.
func raiseWebSearchAlert(provider string, err *webSearchAttemptError) {
	key := provider + "\x00" + string(err.category)
	now := time.Now()
	webSearchAlerts.Lock()
	last, seen := webSearchAlerts.last[key]
	if seen && now.Sub(last) < webSearchAlertInterval {
		webSearchAlerts.Unlock()
		return
	}
	webSearchAlerts.last[key] = now
	webSearchAlerts.Unlock()
	slog.Error("codex: web search provider needs attention",
		"alert", "WEB_SEARCH_PROVIDER_ALERT", "provider", provider,
		"category", string(err.category), "http_status", err.httpStatus, "error", err.message)
}

// classifyAttemptError turns any provider error into a classified one.
func classifyAttemptError(provider string, err error) *webSearchAttemptError {
	var attempt *webSearchAttemptError
	if errors.As(err, &attempt) {
		return attempt
	}
	return attemptError(provider, webSearchUnavailable, 0, "%s", err.Error())
}
