package codex

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerWebActivityReachesBothExecutionEvents(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4)}
	item := map[string]any{"id": "web-1", "type": "webSearch", "action": map[string]any{"type": "openPage", "url": "https://example.com/article"}}
	s.handleItemStarted(item)
	start := <-s.events
	s.handleItemCompleted(item)
	end := <-s.events
	if start.PublicActivity == nil || end.PublicActivity == nil || start.PublicActivity.Status != "running" || end.PublicActivity.Status != "returned" || start.TraceID != end.TraceID {
		t.Fatalf("public activity dropped at runtime: start=%#v end=%#v", start, end)
	}
	for _, recorded := range []string{start.ToolInput, end.ToolResult} {
		if !strings.Contains(recorded, "openPage") || !strings.Contains(recorded, "https://example.com/article") {
			t.Fatalf("native action lost when adding public progress: %q", recorded)
		}
	}
}

func TestWebPublicActivityUsesTypedActionOnly(t *testing.T) {
	activity := webPublicActivity(map[string]any{
		"query": "internal query must not be copied", "action": map[string]any{
			"type": "openPage", "url": "https://example.com/article",
		},
	}, "running")
	if activity.Kind != "open_page" || activity.URL != "https://example.com/article" || activity.Status != "running" {
		t.Fatalf("unexpected activity: %#v", activity)
	}
	if webPublicActivity(map[string]any{"action": map[string]any{"type": "unknown"}}, "returned") != nil {
		t.Fatal("unknown actions must not be guessed")
	}
	for _, raw := range []string{"javascript:alert(1)", "file:///etc/passwd", "https://user:secret@example.com/a"} {
		got := webPublicActivity(map[string]any{"action": map[string]any{"type": "openPage", "url": raw}}, "returned")
		if got.URL != "" || got.Status != "returned" {
			t.Fatalf("unsafe URL or false success: %#v", got)
		}
	}
}

func TestCommandPublicActivityExposesOnlySearchQueriesAndPageHosts(t *testing.T) {
	search := commandPublicActivity(`curl -s -m 20 "https://html.duckduckgo.com/html/?q=rPPG+remote+photoplethysmography&kl=us-en" | head -c 4000`, "running")
	if search == nil || search.Kind != "search" || search.Query != "rPPG remote photoplethysmography" || search.URL != "" {
		t.Fatalf("search receipt = %+v", search)
	}
	pubmed := commandPublicActivity(`curl -s "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi?db=pubmed&term=facial+video+heart+rate&retmax=5"`, "returned")
	if pubmed == nil || pubmed.Kind != "search" || pubmed.Query != "facial video heart rate" || pubmed.Status != "returned" {
		t.Fatalf("pubmed receipt = %+v", pubmed)
	}
	page := commandPublicActivity(`cd /tmp && curl -sL -A "Mozilla/5.0" "https://www.fda.gov/medical-devices/general-wellness?token=secret#top" -o page.html`, "running")
	if page == nil || page.Kind != "open_page" || page.URL != "https://www.fda.gov/medical-devices/general-wellness" || page.Query != "" {
		t.Fatalf("page receipt = %+v", page)
	}
	if got := commandPublicActivity(`curl -s http://127.0.0.1:11446/v1/models`, "running"); got == nil || got.Kind != "command" || got.URL != "" {
		t.Fatalf("loopback address must not leak: %+v", got)
	}
	if got := commandPublicActivity(`curl -s "https://user:pass@example.com/private?x=1"`, "running"); got == nil || got.Kind != "command" || got.URL != "" {
		t.Fatalf("credentialed URL must not leak: %+v", got)
	}
	step := commandPublicActivity(`cd /home/ubuntu/workspaces/test && node /home/ubuntu/Skills-OL-test/tomako-document.mjs --title x`, "running")
	if step == nil || step.Kind != "command" || step.URL != "" || step.Query != "" || step.Label != "" {
		t.Fatalf("plain command receipt must carry no details: %+v", step)
	}
	if commandPublicActivity("   ", "running") != nil {
		t.Fatal("empty command produces no receipt")
	}
}

func TestReasoningTextAcceptsRawContentParts(t *testing.T) {
	item := map[string]any{"type": "reasoning", "summary": []any{},
		"content": []any{map[string]any{"type": "reasoning_text", "text": "比较两家定价"}, map[string]any{"type": "reasoning_text", "text": "核对席位规则"}}}
	if got := appServerReasoningText(item); got != "比较两家定价\n核对席位规则" {
		t.Fatalf("raw reasoning parts must be read: %q", got)
	}
	if got := appServerReasoningText(map[string]any{"summary": []any{"summary line"}, "content": []any{"raw"}}); got != "summary line" {
		t.Fatalf("summaries take precedence: %q", got)
	}
	if got := appServerReasoningText(map[string]any{"summary": []any{}, "content": nil}); got != "" {
		t.Fatalf("no text yields empty: %q", got)
	}
}

func TestCommandPublicActivityKeepsParenthesesAndDropsShellContinuations(t *testing.T) {
	search := commandPublicActivity(`curl -s "https://eutils.ncbi.nlm.nih.gov/entrez/eutils/esearch.fcgi?db=pubmed&term=(remote+photoplethysmography)+AND+(heart+rate)" \`, "running")
	if search == nil || search.Query != "(remote photoplethysmography) AND (heart rate)" {
		t.Fatalf("query must survive parentheses and lose the continuation: %+v", search)
	}
	page := commandPublicActivity("for u in https://www.who.int/publications/i/item/9789240029200 ; do curl -s \"$u\"; done", "returned")
	if page == nil || page.URL != "https://www.who.int/publications/i/item/9789240029200" {
		t.Fatalf("page receipt = %+v", page)
	}
}
