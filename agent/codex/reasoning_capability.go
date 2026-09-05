package codex

import "strings"

// Older Codex catalogs do not recognize this routed model and otherwise omit
// the requested effort from Responses entirely. Bind the capability only to
// this known reasoning model; an arbitrary provider alias is not evidence that
// its upstream accepts reasoning parameters.
func needsReasoningCapability(model, effort string) bool {
	model = strings.TrimSpace(model)
	if strings.TrimSpace(effort) == "" {
		return false
	}
	return model == "gpt-5.6-sol" || model == "tomako/gpt-5.6-sol"
}
