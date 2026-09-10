package codex

import "strings"

// Older Codex catalogs do not recognize this routed model and otherwise omit
// the requested effort from Responses entirely. Bind the capability only to
// these known reasoning models; an arbitrary provider alias is not evidence that
// its upstream accepts reasoning parameters.
// DeepSeek Responses supports reasoning.effort and accepts/ignores summary:
// https://api-docs.deepseek.com/guides/responses_api/
func needsReasoningCapability(model, effort string) bool {
	model = strings.TrimSpace(model)
	if strings.TrimSpace(effort) == "" {
		return false
	}
	switch model {
	case "gpt-5.6-sol", "tomako/gpt-5.6-sol", "deepseek-v4-flash", "tomako/deepseek-v4-flash",
		"deepseek-v4-flash-vision-exp", "tomako/deepseek-v4-flash-vision-exp":
		return true
	default:
		return false
	}
}
