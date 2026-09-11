package codex

import (
	"net"
	"net/url"
	"regexp"
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

// searchQueryParams lists, per host suffix, the query parameter that carries a
// human search string. Anything else on the URL is dropped.
var searchQueryParams = map[string]string{
	"duckduckgo.com":          "q",
	"google.com":              "q",
	"bing.com":                "q",
	"search.brave.com":        "q",
	"eutils.ncbi.nlm.nih.gov": "term",
	"reddit.com":              "q",
}

var commandURLPattern = regexp.MustCompile(`https?://[^\s"'<>\)\]]+`)

// commandPublicActivity turns a shell command into an allowlisted receipt for
// the conversation view: the first web address it fetches becomes a search
// (decoded query only) or a page read (scheme and host, path kept, query and
// fragment dropped). Commands without a web address are reported only as an
// anonymous step so the user still sees that work is happening. The command
// text itself never leaves the bridge.
func commandPublicActivity(command, status string) *core.PublicActivity {
	for _, raw := range commandURLPattern.FindAllString(command, 8) {
		raw = strings.TrimRight(raw, `.,;:"'`)
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if host == "localhost" || strings.HasSuffix(host, ".local") || net.ParseIP(host) != nil || !strings.Contains(host, ".") {
			continue
		}
		if param := searchParamFor(host); param != "" {
			if query := strings.TrimSpace(parsed.Query().Get(param)); query != "" {
				return &core.PublicActivity{Kind: "search", Status: status, Query: truncate(strings.Join(strings.Fields(query), " "), 120)}
			}
		}
		parsed.RawQuery, parsed.Fragment, parsed.RawFragment = "", "", ""
		return &core.PublicActivity{Kind: "open_page", Status: status, URL: parsed.String()}
	}
	if strings.TrimSpace(command) == "" {
		return nil
	}
	return &core.PublicActivity{Kind: "command", Status: status}
}

func searchParamFor(host string) string {
	for suffix, param := range searchQueryParams {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return param
		}
	}
	if strings.Contains(host, "redlib") || strings.Contains(host, "libreddit") {
		return "q"
	}
	return ""
}
