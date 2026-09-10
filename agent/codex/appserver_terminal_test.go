package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func terminalTestSession() *appServerSession {
	s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "turn-1"}
	s.threadID.Store("thread-1")
	return s
}

func TestAppServerSession_StructuredTurnOnlyReturnsNativeFinalAnswer(t *testing.T) {
	s := terminalTestSession()
	s.runtime.OutputSchema = json.RawMessage(`{"type":"object"}`)
	// Progress can itself be valid schema-shaped JSON. Never pick a JSON object
	// by shape: the native message phase identifies the actual final answer.
	for _, item := range []struct{ id, phase, text string }{
		{"progress-1", "commentary", `{"coverageStrategy":"Reading the skill"}`},
		{"progress-2", "commentary", `{"coverageStrategy":"Preparing the plan"}`},
		{"answer", "final_answer", `{"coverageStrategy":"Three complementary research routes"}`},
	} {
		s.handleAgentMessageDelta(item.id, item.text[:10])
		s.handleAgentMessageDelta(item.id, item.text[10:])
		s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": item.id, "phase": item.phase, "text": item.text})
	}
	s.completeTurn("turn-1", nil)
	var output strings.Builder
	var thinking, results int
	for len(s.events) > 0 {
		event := <-s.events
		switch event.Type {
		case core.EventText:
			output.WriteString(event.Content)
		case core.EventCommentary:
			thinking++
		case core.EventResult:
			results++
		}
	}
	if got, want := output.String(), `{"coverageStrategy":"Three complementary research routes"}`; got != want {
		t.Fatalf("structured terminal output = %q, want %q", got, want)
	}
	if thinking != 2 || results != 1 {
		t.Fatalf("thinking=%d results=%d, want 2 and 1", thinking, results)
	}
}

func TestAppServerSession_PublicCommentaryDoesNotLeakReasoningOrEnterFinal(t *testing.T) {
	s := terminalTestSession()
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "progress", "phase": "commentary"})
	s.handleAgentMessageDelta("progress", "我会先核对近期公开资料。")
	first := <-s.events
	if first.Type != core.EventCommentary || first.ContentVersion != 1 || first.ContentDone {
		t.Fatalf("expected immediate public snapshot, got %#v", first)
	}
	s.handleItemCompleted(map[string]any{"type": "reasoning", "id": "reason", "summary": []any{map[string]any{"text": "private reasoning"}}})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "progress", "phase": "commentary", "text": "我会先核对近期公开资料。"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "final", "phase": "final_answer", "text": "最终建议"})
	s.completeTurn("turn-1", nil)
	var public, final string
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventCommentary && !e.ContentProvisional {
			public += e.Content
		}
		if e.Type == core.EventText {
			final += e.Content
		}
	}
	if public != "我会先核对近期公开资料。" || final != "最终建议" {
		t.Fatalf("public=%q final=%q", public, final)
	}
}

func TestAppServerSession_LatePhaseDoesNotStreamCommentaryIntoFinal(t *testing.T) {
	s := terminalTestSession()
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "progress"})
	s.handleAgentMessageDelta("progress", "正在生成封面。")
	if len(s.events) != 0 {
		t.Fatal("unclassified delta entered final response before phase was known")
	}
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "progress", "phase": "commentary", "text": "正在生成封面。"})
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer"})
	s.handleAgentMessageDelta("answer", "封面已保存。")
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "封面已保存。"})
	s.completeTurn("turn-1", nil)
	var progress, final string
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventCommentary && !e.ContentProvisional {
			progress += e.Content
		}
		if e.Type == core.EventText {
			final += e.Content
		}
	}
	if progress != "正在生成封面。" || final != "封面已保存。" {
		t.Fatalf("progress=%q final=%q", progress, final)
	}
}

func TestAppServerSession_PhaseLessProviderUsesToolBoundaryWithoutFinalLeak(t *testing.T) {
	s := terminalTestSession()
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "progress"})
	s.handleAgentMessageDelta("progress", "正在核对资料。")
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "progress", "text": "正在核对资料。"})
	s.handleItemStarted(map[string]any{"type": "commandExecution", "id": "tool", "command": "read"})
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer"})
	s.handleAgentMessageDelta("answer", "核对结果。")
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "text": "核对结果。"})
	s.completeTurn("turn-1", nil)
	var progress, final string
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventCommentary && !e.ContentProvisional {
			progress += e.Content
		}
		if e.Type == core.EventText {
			final += e.Content
		}
	}
	if progress != "正在核对资料。" || final != "核对结果。" {
		t.Fatalf("progress=%q final=%q", progress, final)
	}
}

func TestAppServerSession_FailedTurnIsNotAnEmptySuccess(t *testing.T) {
	for _, tc := range []struct{ status, detail, want string }{
		{"failed", `,"error":{"message":"connection refused"}`, "connection refused"},
		{"failed", "", "failed"},
		{"interrupted", "", "interrupted"},
	} {
		t.Run(tc.status+tc.want, func(t *testing.T) {
			s := terminalTestSession()
			s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"`+tc.status+`"`+tc.detail+`}}`))
			select {
			case event := <-s.events:
				if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), tc.want) {
					t.Fatalf("terminal event = %#v, want error containing %q", event, tc.want)
				}
			default:
				t.Fatal("failed turn did not emit an error")
			}
		})
	}
}

func TestAppServerSession_NestedErrorIsTerminalAndNotDuplicated(t *testing.T) {
	s := terminalTestSession()
	s.handleNotification("error", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","willRetry":false,"error":{"message":"gateway unavailable"}}`))
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"gateway unavailable"}}}`))
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"thread-1","status":{"type":"idle"}}`))
	select {
	case event := <-s.events:
		if event.Type != core.EventError || event.Error == nil || !strings.Contains(event.Error.Error(), "gateway unavailable") {
			t.Fatalf("event = %#v, want gateway error", event)
		}
	default:
		t.Fatal("nested terminal error was swallowed")
	}
	if len(s.events) != 0 {
		t.Fatal("a failed turn must not also emit success or duplicate failure")
	}
}

func TestAppServerSession_IdleDoesNotCompleteBeforeTurnOutcome(t *testing.T) {
	s := terminalTestSession()
	s.handleNotification("thread/status/changed", json.RawMessage(`{"threadId":"thread-1","status":{"type":"idle"}}`))
	if len(s.events) != 0 || s.currentTurn != "turn-1" {
		t.Fatal("thread idle must not discard the authoritative turn outcome")
	}
}

func TestAppServerSession_RetryableErrorCanStillCompleteSuccessfully(t *testing.T) {
	s := terminalTestSession()
	s.handleNotification("error", json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","willRetry":true,"error":{"message":"retrying"}}`))
	if len(s.events) != 0 || s.currentTurn != "turn-1" {
		t.Fatal("retryable error prematurely terminated the turn")
	}
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "msg-1", "text": "Recovered"})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	if event := <-s.events; event.Type != core.EventText || event.Content != "Recovered" {
		t.Fatalf("text event = %#v", event)
	}
	if event := <-s.events; event.Type != core.EventResult || !event.Done {
		t.Fatalf("result event = %#v", event)
	}
}

func TestAppServerSession_StaleTurnOutcomeCannotEndCurrentTurn(t *testing.T) {
	s := terminalTestSession()
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"old-turn","status":"failed","error":{"message":"old failure"}}}`))
	s.handleNotification("error", json.RawMessage(`{"threadId":"thread-1","turnId":"old-turn","willRetry":false,"error":{"message":"old failure"}}`))
	if len(s.events) != 0 || s.currentTurn != "turn-1" {
		t.Fatal("stale completion/error ended the current turn")
	}
}

func TestAppServerSession_FinalProvenanceSurvivesDeltaAndCompletionTail(t *testing.T) {
	s := terminalTestSession()
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer"})
	s.handleAgentMessageDelta("answer", "However, ")
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "text": "However, here is the comparison."})
	s.completeTurn("turn-1", nil)
	var final string
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventText {
			if e.ResponseSource != "native_final" {
				t.Fatalf("lost native phase: %#v", e)
			}
			final += e.Content
		}
	}
	if final != "However, here is the comparison." {
		t.Fatalf("duplicated or lost text: %q", final)
	}
	s = terminalTestSession()
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "legacy", "text": "Unclassified final"})
	s.completeTurn("turn-1", nil)
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventText && e.ResponseSource != "" {
			t.Fatal("phase-less text was certified")
		}
	}
}
