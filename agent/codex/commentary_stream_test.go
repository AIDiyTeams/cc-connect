package codex

import (
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestCommentaryStreamsOneNoteBeforeCompletionAndFlushesThrottledTail(t *testing.T) {
	s := terminalTestSession()
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "note", "phase": "commentary"})
	s.handleAgentMessageDelta("note", "已找到官方资料，")
	first := <-s.events
	if first.Type != core.EventCommentary || first.ContentDone || first.Content != "已找到官方资料，" {
		t.Fatalf("first public text waited for completion: %#v", first)
	}
	s.handleAgentMessageDelta("note", "正在比较")
	if len(s.events) != 0 {
		t.Fatal("unthrottled commentary checkpoint")
	}
	s.commentaryStreams["note"].lastSentAt = time.Now().Add(-2 * time.Second)
	s.handleAgentMessageDelta("note", "价格。")
	second := <-s.events
	if second.ContentVersion != 2 || second.TraceID != first.TraceID || second.Content != "已找到官方资料，正在比较价格。" {
		t.Fatalf("snapshot failed to accumulate: %#v", second)
	}
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "note", "phase": "commentary", "text": "已找到官方资料，正在比较价格与限制。"})
	final := <-s.events
	if !final.ContentDone || final.ContentVersion != 3 || final.Content != "已找到官方资料，正在比较价格与限制。" {
		t.Fatalf("completion lost the final snapshot: %#v", final)
	}
	s.handleAgentMessageDelta("unknown", "unclassified text")
	s.handleAgentMessageDelta("note", "late delta")
	if len(s.events) != 0 {
		t.Fatal("unclassified or late text leaked")
	}
}
