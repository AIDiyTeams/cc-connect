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
