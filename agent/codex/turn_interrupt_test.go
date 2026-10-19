package codex

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type rpcRequestLine struct {
	ID     int64          `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func startInterrupt(t *testing.T, s *appServerSession, stdin *lockedWriteCloser) (rpcRequestLine, <-chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- s.Interrupt(context.Background()) }()
	var line rpcRequestLine
	if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, stdin)), &line); err != nil {
		t.Fatalf("decode interrupt request: %v", err)
	}
	return line, done
}

func interruptResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("Interrupt never returned")
		return nil
	}
}

// The app-server answers turn/interrupt when the turn aborts and then reports
// turn/completed with status "interrupted". An interrupt the engine asked for
// ends the reply with a Done result, and the thread takes the next turn.
func TestInterruptEndsTheTurnInPlace(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	s.stateMu.Lock()
	s.pendingMsgs = append(s.pendingMsgs, "half an answer")
	s.stateMu.Unlock()

	req, done := startInterrupt(t, s, stdin)
	if req.Method != "turn/interrupt" || req.Params["threadId"] != "thread-1" || req.Params["turnId"] != "turn-1" {
		t.Fatalf("interrupt request = %+v, want turn/interrupt for thread-1 turn-1", req)
	}
	s.handleResponse(rpcResponseEnvelope{ID: float64(req.ID), Result: json.RawMessage(`{}`)})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted"}}`))
	if err := interruptResult(t, done); err != nil {
		t.Fatalf("Interrupt = %v", err)
	}

	final, _, results, errors := drainReplyEvents(s)
	if results != 1 || errors != 0 || final != "" {
		t.Fatalf("interrupted turn emitted text %q, %d results, %d errors; want one Done result and nothing else", final, results, errors)
	}
	waitForCurrentTurn(t, s, "")
	if !s.Alive() {
		t.Fatal("interrupt closed the session")
	}

	// The next turn on the same thread completes as usual.
	s.stateMu.Lock()
	s.currentTurn = "turn-2"
	s.stateMu.Unlock()
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "Here is the update."})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2","status":"completed"}}`))
	if answer, results := waitForResult(t, s); results != 1 || answer != "Here is the update." {
		t.Fatalf("next turn answer = %q, results = %d", answer, results)
	}
}

// A turn interrupted by anything other than the engine still fails the reply.
func TestUnrequestedInterruptionStillFailsTheReply(t *testing.T) {
	s, _ := finalReplyTestSession(t)
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted"}}`))
	if _, _, results, errors := drainReplyEvents(s); results != 0 || errors != 1 {
		t.Fatalf("results = %d, errors = %d; want the interruption reported as a failure", results, errors)
	}
}

// Without a running turn there is nothing to interrupt; the engine then
// closes the session instead.
func TestInterruptWithoutARunningTurnFails(t *testing.T) {
	for _, turn := range []string{"", finalReplyPendingTurn} {
		s, stdin := finalReplyTestSession(t)
		s.stateMu.Lock()
		s.currentTurn = turn
		s.stateMu.Unlock()
		if err := s.Interrupt(context.Background()); err == nil {
			t.Fatalf("Interrupt with current turn %q succeeded", turn)
		}
		if written := stdin.String(); written != "" {
			t.Fatalf("Interrupt with current turn %q wrote %q", turn, written)
		}
	}
}

// When the turn completes on its own before the interrupt lands, the
// app-server rejects the interrupt; the turn's own ending already went out.
func TestInterruptAfterTheTurnFinishedIsNotAnError(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	req, done := startInterrupt(t, s, stdin)
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "Finished."})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	s.handleResponse(rpcResponseEnvelope{ID: float64(req.ID), Error: &rpcError{Message: "no active turn to interrupt"}})
	if err := interruptResult(t, done); err != nil {
		t.Fatalf("Interrupt = %v, want nil once the turn has ended", err)
	}
	if answer, results := waitForResult(t, s); results != 1 || answer != "Finished." {
		t.Fatalf("answer = %q, results = %d", answer, results)
	}
}

// A rejected interrupt of a turn that is still running is an error, so the
// engine falls back to closing the session.
func TestRejectedInterruptOfARunningTurnFails(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	req, done := startInterrupt(t, s, stdin)
	s.handleResponse(rpcResponseEnvelope{ID: float64(req.ID), Error: &rpcError{Message: "failed to interrupt turn"}})
	if err := interruptResult(t, done); err == nil {
		t.Fatal("Interrupt succeeded although the app-server rejected it")
	}
	s.stateMu.Lock()
	requested := s.interruptedTurn
	s.stateMu.Unlock()
	if requested != "" {
		t.Fatalf("interruptedTurn = %q after a rejected interrupt, want it cleared", requested)
	}
}
