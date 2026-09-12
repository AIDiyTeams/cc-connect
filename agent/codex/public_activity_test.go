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

func TestReasoningCompletionMarksSummaryVersusRaw(t *testing.T) {
	if k := reasoningKind(map[string]any{"summary": []any{"short"}, "content": []any{"long"}}); k != "summary" {
		t.Fatalf("summary parts mark the block as summary: %q", k)
	}
	if k := reasoningKind(map[string]any{"summary": []any{}, "content": []any{map[string]any{"type": "reasoning_text", "text": "long"}}}); k != "raw" {
		t.Fatalf("content-only blocks are raw: %q", k)
	}
}

func TestCommandPublicActivity_URLArgumentsNeverClaimWebAccess(t *testing.T) {
	commands := []string{
		`curl -s -o ./_verify_out.png "https://test.tomako.ai/media/oss/generated-images/2026/09/12/result.png"; file ./_verify_out.png`,
		`curl -s "https://api.example.com/v1/images"`,
		`curl -s "https://example.com/download?token=secret"`,
		`curl -s "http://127.0.0.1:11446/v1/models"`,
		`curl -s "https://user:pass@example.com/private"`,
		`curl -s "https://html.duckduckgo.com/html/?q=private+search+text"`,
		`printf '%s' 'https://example.com/article' > sources.txt`,
		`python inspect_image.py --reference https://example.com/reference.png`,
	}
	for _, command := range commands {
		for _, status := range []string{"running", "returned"} {
			got := commandPublicActivity(command, status)
			if got == nil || got.Kind != "command" || got.Status != status || got.URL != "" || got.Query != "" || got.Label != "" {
				t.Fatalf("URL arguments must stay an anonymous command receipt: %+v", got)
			}
		}
	}
	if commandPublicActivity("  ", "running") != nil {
		t.Fatal("empty command must not produce a receipt")
	}
}

func TestCommandPublicActivity_URLDoesNotOverrideDeclaredCategory(t *testing.T) {
	got := commandPublicActivity(`node /home/ubuntu/Skills-OL-test/tomako-document.mjs --source https://example.com/article`, "returned")
	if got == nil || got.Kind != "command" || got.Label != "document" || got.URL != "" || got.Query != "" {
		t.Fatalf("a declared operation keeps its category without exposing its arguments: %+v", got)
	}
}

func TestAppServerImageDownload_PublicReceiptRetainsExecutionLifecycle(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4)}
	command := `curl -s -o result.png https://test.tomako.ai/media/oss/generated-images/result.png`
	item := map[string]any{"id": "download-1", "type": "commandExecution", "command": command}
	s.handleItemStarted(item)
	start := <-s.events
	item["status"], item["exitCode"] = "completed", 0
	s.handleItemCompleted(item)
	end := <-s.events
	for _, event := range []core.Event{start, end} {
		if event.PublicActivity == nil || event.PublicActivity.Kind != "command" || event.PublicActivity.URL != "" || event.PublicActivity.Label != "" {
			t.Fatalf("image download was misreported as webpage access: %+v", event)
		}
		if event.TraceID != "download-1" || event.ToolInput != command {
			t.Fatalf("private execution evidence must remain intact: %+v", event)
		}
	}
	if start.PublicActivity.Status != "running" || end.PublicActivity.Status != "returned" || end.ToolSuccess == nil || !*end.ToolSuccess {
		t.Fatalf("execution lifecycle lost: start=%+v end=%+v", start, end)
	}
}
