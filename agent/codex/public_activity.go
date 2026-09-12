package codex

import (
	"net/url"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// A completed web tool is not proof that the page was accessible or that its
// content is reliable. "returned" deliberately makes neither claim.
func webPublicActivity(item map[string]any, status string) *core.PublicActivity {
	action, _ := item["action"].(map[string]any)
	kind := "search"
	switch action["type"] {
	case "openPage":
		kind = "open_page"
	case "findInPage":
		kind = "find_in_page"
	case "search", nil:
	default:
		return nil
	}
	activity := &core.PublicActivity{Kind: kind, Status: status}
	if raw, ok := action["url"].(string); ok && len(raw) <= 2048 {
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" &&
			(parsed.Scheme == "https" || parsed.Scheme == "http") {
			// Query-bearing links can contain signed tokens; omit them rather
			// than silently linking to a different resource. Fragments are local.
			parsed.Fragment = ""
			activity.URL = strings.TrimSpace(parsed.String())
		}
	}
	return activity
}

// commandPublicActivity reports only the execution type and a declared script or
// skill category. A URL in command arguments is not evidence of a page read:
// it may be an image download, an API request, or text being written to a file.
// Web receipts come from the structured web tool action in webPublicActivity.
func commandPublicActivity(command, status string) *core.PublicActivity {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return &core.PublicActivity{Kind: "command", Status: status, Label: commandActivityLabel(command)}
}
