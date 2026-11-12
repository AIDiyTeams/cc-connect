package codex

import (
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// webReadUsageKind marks a receipt that carries the page text a research turn
// actually read. Portal checks an independent reviewer's quotations against
// these texts for pages its own server fetch cannot render. Older Portals ignore
// unknown receipt kinds.
const webReadUsageKind = "page_read"

// recordPageRead reports what the reader returned to the model in a growth
// research or review turn. The text is produced by the reader, never by the
// model, so it can stand as the turn's record of what the page said.
func (s *appServerSession) recordPageRead(requested []string, output string) {
	if !s.publicResearch() || output == "" || len(requested) == 0 {
		return
	}
	s.runtimeMu.RLock()
	runtime := s.runtime
	s.runtimeMu.RUnlock()
	s.emit(core.Event{Type: core.EventUsageReceipt, Metadata: map[string]any{"receipt": map[string]any{
		"kind":            webReadUsageKind,
		"requested_urls":  requested,
		"output":          output,
		"occurred_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"workspace_id":    runtime.WorkspaceID,
		"brand_id":        runtime.BrandID,
		"user_id":         runtime.UserID,
		"chat_session_id": runtime.ChatSessionID,
		"task_id":         runtime.TaskID,
		"turn_no":         runtime.TurnNo,
	}}})
}
