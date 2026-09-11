package codex

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestNoteRightBeforeAnActionBecomesAStepCaption(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	s.emitCommentarySnapshot("note-0", "你想知道三款应用的定价和功能差异，我先核实再对比。", true, false)
	<-s.events
	s.handleItemStarted(map[string]any{"type": "commandExecution", "id": "cmd-0", "command": "ls"})
	if ev := <-s.events; ev.Type != core.EventToolUse {
		t.Fatalf("the opening sentence of a turn stays prose: %+v", ev)
	}
	s.emitCommentarySnapshot("note-1", "核实近三年心率测量的公开证据", true, false)
	first := <-s.events
	if first.Type != core.EventCommentary || first.ContentKind != "" || first.ContentVersion != 1 {
		t.Fatalf("plain note first: %+v", first)
	}
	s.handleItemStarted(map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "curl -s https://example.com/a"})
	retag := <-s.events
	if retag.Type != core.EventCommentary || retag.TraceID != "note-1" || retag.ContentKind != "step" ||
		retag.ContentVersion != 2 || !retag.ContentDone || retag.Content != "核实近三年心率测量的公开证据" {
		t.Fatalf("note must be re-sent as a step caption: %+v", retag)
	}
	if tool := <-s.events; tool.Type != core.EventToolUse {
		t.Fatalf("tool event follows: %+v", tool)
	}
	s.handleItemStarted(map[string]any{"type": "commandExecution", "id": "cmd-2", "command": "ls"})
	if again := <-s.events; again.Type != core.EventToolUse {
		t.Fatalf("a caption is tagged once: %+v", again)
	}
}

func TestLongOrStaleNotesStayNotes(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	long := ""
	for i := 0; i < 90; i++ {
		long += "字"
	}
	s.emitCommentarySnapshot("note-long", long, true, false)
	<-s.events
	s.handleItemStarted(map[string]any{"type": "commandExecution", "id": "cmd-1", "command": "ls"})
	if ev := <-s.events; ev.Type != core.EventToolUse {
		t.Fatalf("a paragraph is never a step caption: %+v", ev)
	}
	s.emitCommentarySnapshot("note-old", "旧的判断", true, false)
	<-s.events
	s.stateMu.Lock()
	s.lastCommentary.at = time.Now().Add(-10 * time.Second)
	s.stateMu.Unlock()
	s.handleItemStarted(map[string]any{"type": "commandExecution", "id": "cmd-2", "command": "ls"})
	if ev := <-s.events; ev.Type != core.EventToolUse {
		t.Fatalf("a note long before the action stays a note: %+v", ev)
	}
}
