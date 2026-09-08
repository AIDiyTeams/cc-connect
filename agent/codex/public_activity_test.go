package codex

import (
	"github.com/chenhg5/cc-connect/core"
	"testing"
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
