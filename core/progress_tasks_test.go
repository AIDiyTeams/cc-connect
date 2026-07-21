package core

import (
	"testing"
)

func TestProgressTaskTrackerStartsEmptyInsteadOfInferringFromKeywords(t *testing.T) {
	tracker := newProgressTaskTracker("写 Reddit 文章，同时生成小红书封面", LangChinese)
	if len(tracker.Tasks()) != 0 {
		t.Fatalf("keyword-inferred tasks must not be shown: %#v", tracker.Tasks())
	}
}

func TestProgressTaskTrackerUsesAndUpdatesAgentPlan(t *testing.T) {
	tracker := newProgressTaskTracker("ignored", LangChinese)
	tasks := tracker.Replace([]ProgressTask{
		{ID: "task-1", Title: "明确 Reddit 帖子角度", Status: ProgressTaskInProgress},
		{ID: "task-2", Title: "生成对应视觉素材", Status: ProgressTaskPending},
	})
	if len(tasks) != 2 || tasks[0].Title != "明确 Reddit 帖子角度" {
		t.Fatalf("agent plan was not preserved: %#v", tasks)
	}

	tasks = tracker.Replace([]ProgressTask{
		{ID: "task-1", Title: "明确 Reddit 帖子角度", Status: ProgressTaskCompleted},
		{ID: "task-2", Title: "生成对应视觉素材", Status: ProgressTaskInProgress},
	})
	if tasks[0].Status != ProgressTaskCompleted || tasks[1].Status != ProgressTaskInProgress {
		t.Fatalf("agent statuses were not updated: %#v", tasks)
	}
}

func TestProgressTaskTrackerDoesNotCreateTasksFromToolEvents(t *testing.T) {
	tracker := newProgressTaskTracker("请回答这个产品问题", LangChinese)
	tasks := tracker.Observe(ProgressCardEntry{
		Kind: ProgressEntryToolUse,
		Tool: "WebSearch",
		Text: "search current product documentation",
	})
	if len(tasks) != 0 {
		t.Fatalf("tool event invented a task list: %#v", tasks)
	}
}

func TestBuildAndParseProgressCardPayloadV3(t *testing.T) {
	tasks := []ProgressTask{
		{ID: "context", Title: " Review brand context ", Status: ProgressTaskCompleted},
		{ID: "visuals", Title: "Create cover visuals", Status: ProgressTaskInProgress},
	}
	payload := BuildProgressCardPayloadV3(tasks, "Codex", LangEnglish, ProgressCardStateRunning)
	parsed, ok := ParseProgressCardPayload(payload)
	if !ok {
		t.Fatalf("ParseProgressCardPayload(%q) failed", payload)
	}
	if parsed.Version != 3 || len(parsed.Tasks) != 2 {
		t.Fatalf("parsed payload = %#v", parsed)
	}
	if len(parsed.Items) != 0 || len(parsed.Entries) != 0 {
		t.Fatalf("v3 payload must not contain raw events: %#v", parsed)
	}
	if parsed.Tasks[0].Title != "Review brand context" {
		t.Fatalf("task title = %q", parsed.Tasks[0].Title)
	}
}
