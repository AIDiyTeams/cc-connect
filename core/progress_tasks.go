package core

type ProgressTaskStatus string

const (
	ProgressTaskPending    ProgressTaskStatus = "pending"
	ProgressTaskInProgress ProgressTaskStatus = "in_progress"
	ProgressTaskCompleted  ProgressTaskStatus = "completed"
	ProgressTaskFailed     ProgressTaskStatus = "failed"
)

// ProgressTask is a user-facing, high-level stage authored by the Agent for
// the current request. It intentionally excludes commands, paths, tool output,
// and chain-of-thought.
type ProgressTask struct {
	ID     string             `json:"id"`
	Title  string             `json:"title"`
	Status ProgressTaskStatus `json:"status"`
}

// progressTaskTracker deliberately starts empty. A task list is only shown
// after the Agent publishes turn/plan/updated; cc-connect must never infer a
// semantic checklist from keywords in the user's request.
type progressTaskTracker struct {
	tasks []ProgressTask
}

func newProgressTaskTracker(_ string, _ Language) *progressTaskTracker {
	return &progressTaskTracker{}
}

func (t *progressTaskTracker) Tasks() []ProgressTask {
	if t == nil {
		return nil
	}
	out := make([]ProgressTask, len(t.tasks))
	copy(out, t.tasks)
	return out
}

// Replace accepts the latest authoritative Agent plan. Titles may change when
// the Agent refines its approach, while index-based IDs keep UI rows stable.
func (t *progressTaskTracker) Replace(tasks []ProgressTask) []ProgressTask {
	if t == nil {
		return nil
	}
	t.tasks = cleanProgressTasks(tasks)
	return t.Tasks()
}

// Observe no longer invents stages from thinking or tool names. Status changes
// arrive through subsequent Agent plan updates.
func (t *progressTaskTracker) Observe(_ ProgressCardEntry) []ProgressTask {
	return t.Tasks()
}

func (t *progressTaskTracker) Finalize(state ProgressCardState) []ProgressTask {
	if t == nil || len(t.tasks) == 0 {
		return nil
	}
	if state == ProgressCardStateFailed {
		failureIndex := -1
		for index := range t.tasks {
			if t.tasks[index].Status == ProgressTaskInProgress {
				failureIndex = index
				break
			}
		}
		if failureIndex < 0 {
			failureIndex = len(t.tasks) - 1
		}
		t.tasks[failureIndex].Status = ProgressTaskFailed
		return t.Tasks()
	}
	for index := range t.tasks {
		if t.tasks[index].Status != ProgressTaskFailed {
			t.tasks[index].Status = ProgressTaskCompleted
		}
	}
	return t.Tasks()
}
