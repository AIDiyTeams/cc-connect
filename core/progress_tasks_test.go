package core

import (
	"strings"
	"testing"
)

func TestProgressTaskTrackerBuildsXiaohongshuPlanFromRequest(t *testing.T) {
	request := `You are Tomako Studio.

User request:
生成一篇小红书的热点文章，包含图片和配文、标题：封面、子页面配图、正文和标签都要完备

When producing content: ready-to-use.`
	tracker := newProgressTaskTracker(request, LangChinese)
	tasks := tracker.Tasks()
	want := []string{
		"梳理小红书主题与品牌信息",
		"规划内容结构与表达重点",
		"生成并检查封面与配图",
		"撰写标题、正文与标签",
		"整理并核对完整发布内容",
	}
	if len(tasks) != len(want) {
		t.Fatalf("tasks = %#v, want %d stages", tasks, len(want))
	}
	for i := range want {
		if tasks[i].Title != want[i] {
			t.Fatalf("tasks[%d].Title = %q, want %q", i, tasks[i].Title, want[i])
		}
		wantStatus := ProgressTaskPending
		if i == 0 {
			wantStatus = ProgressTaskInProgress
		}
		if tasks[i].Status != wantStatus {
			t.Fatalf("tasks[%d].Status = %q, want %q", i, tasks[i].Status, wantStatus)
		}
	}
}

func TestProgressTaskTrackerReordersWithoutLeakingRawEvents(t *testing.T) {
	tracker := newProgressTaskTracker("生成一篇包含封面、配图、正文和标签的小红书文章", LangChinese)
	raw := `curl -H "Authorization: Bearer secret-token" https://tomako.ai/api/image/generate`
	tasks := tracker.Observe(ProgressCardEntry{
		Kind: ProgressEntryToolUse,
		Tool: "Bash",
		Text: raw,
	})
	if len(tasks) < 2 || tasks[0].Status != ProgressTaskCompleted || tasks[1].ID != string(progressTaskVisuals) || tasks[1].Status != ProgressTaskInProgress {
		t.Fatalf("tasks after visual work = %#v", tasks)
	}
	for _, task := range tasks {
		if strings.Contains(task.Title, "curl") || strings.Contains(task.Title, "secret-token") || strings.Contains(task.Title, "Bash") {
			t.Fatalf("raw system detail leaked into task title: %#v", task)
		}
	}
}

func TestProgressTaskTrackerCanAddHighValueStage(t *testing.T) {
	tracker := newProgressTaskTracker("请回答这个产品问题", LangChinese)
	tasks := tracker.Observe(ProgressCardEntry{
		Kind: ProgressEntryToolUse,
		Tool: "WebSearch",
		Text: "search current product documentation",
	})
	found := false
	for _, task := range tasks {
		if task.ID == string(progressTaskResearch) {
			found = true
			if task.Status != ProgressTaskInProgress {
				t.Fatalf("research status = %q, want in_progress", task.Status)
			}
		}
	}
	if !found {
		t.Fatalf("dynamic research stage missing: %#v", tasks)
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

func TestProgressTaskTrackerRespectsNegativeImageIntent(t *testing.T) {
	tracker := newProgressTaskTracker("分析 Tomako 当前品牌信息并给出 3 条具体改进建议，不需要生成图片", LangChinese)
	for _, task := range tracker.Tasks() {
		if task.ID == string(progressTaskVisuals) || task.ID == string(progressTaskBuild) {
			t.Fatalf("negative image/analysis request produced unrelated task: %#v", tracker.Tasks())
		}
	}

	// Free-form thinking can advance a planned stage but cannot invent a new
	// one merely because it mentions implementation or images.
	tasks := tracker.Observe(ProgressCardEntry{
		Kind: ProgressEntryThinking,
		Text: "No images are needed; I will implement the recommendations in the answer",
	})
	for _, task := range tasks {
		if task.ID == string(progressTaskVisuals) || task.ID == string(progressTaskBuild) {
			t.Fatalf("thinking invented unrelated task: %#v", tasks)
		}
	}
}
