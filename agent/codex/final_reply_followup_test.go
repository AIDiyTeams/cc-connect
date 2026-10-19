package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func finalReplyTestSession(t *testing.T) (*appServerSession, *lockedWriteCloser) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		events:      make(chan core.Event, 32),
		currentTurn: "turn-1",
		stdin:       stdin,
		ctx:         ctx,
		cancel:      cancel,
		pending:     make(map[int64]chan rpcResponseEnvelope),
		model:       "tomako/deepseek-v4-flash-vision-exp",
	}
	s.alive.Store(true)
	s.threadID.Store("thread-1")
	return s, stdin
}

// endTurnWithReasoningOnly reproduces cmsg-c5ae85f942fd4388: the model searched,
// reasoned and stopped; Codex reported a completed turn with no final_answer.
func endTurnWithReasoningOnly(s *appServerSession, turnID string) {
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": turnID + "-progress", "phase": "commentary"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": turnID + "-progress", "phase": "commentary", "text": "Checking the latest release notes."})
	s.handleItemCompleted(map[string]any{"type": "reasoning", "id": turnID + "-reason", "summary": []any{map[string]any{"text": "Should I ask which feature?"}}})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"`+turnID+`","status":"completed"}}`))
}

type turnStartLine struct {
	ID     int64          `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

func turnStartLines(t *testing.T, w *lockedWriteCloser) []turnStartLine {
	t.Helper()
	var lines []turnStartLine
	for _, raw := range strings.Split(w.String(), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var line turnStartLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		if line.Method == "turn/start" {
			lines = append(lines, line)
		}
	}
	return lines
}

func waitForCurrentTurn(t *testing.T, s *appServerSession, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.stateMu.Lock()
		got := s.currentTurn
		s.stateMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("current turn never became %q", want)
}

func drainReplyEvents(s *appServerSession) (final string, sources []string, results, errors int) {
	for len(s.events) > 0 {
		e := <-s.events
		switch e.Type {
		case core.EventText:
			final += e.Content
			sources = append(sources, e.ResponseSource)
		case core.EventResult:
			results++
		case core.EventError:
			errors++
		}
	}
	return
}

func waitForResult(t *testing.T, s *appServerSession) (final string, results int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case e := <-s.events:
			switch e.Type {
			case core.EventText:
				final += e.Content
			case core.EventResult:
				return final, 1
			}
		case <-deadline:
			t.Fatal("reply never completed")
		}
	}
}

func startFollowUpTurn(t *testing.T, s *appServerSession, stdin *lockedWriteCloser, turnID string) turnStartLine {
	t.Helper()
	waitForWrittenJSONLine(t, stdin)
	starts := turnStartLines(t, stdin)
	if len(starts) != 1 {
		t.Fatalf("turn/start requests = %d, want exactly one follow-up", len(starts))
	}
	s.handleResponse(rpcResponseEnvelope{ID: float64(starts[0].ID), Result: json.RawMessage(`{"turn":{"id":"` + turnID + `"}}`)})
	waitForCurrentTurn(t, s, turnID)
	return starts[0]
}

func TestFinalReplyFollowUp_EmptyEndingAsksOnceAndDeliversTheAnswer(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	endTurnWithReasoningOnly(s, "turn-1")
	if _, _, results, errors := drainReplyEvents(s); results != 0 || errors != 0 {
		t.Fatalf("an empty ending must keep the reply open for the follow-up, results=%d errors=%d", results, errors)
	}

	start := startFollowUpTurn(t, s, stdin, "turn-2")
	if start.Params["threadId"] != "thread-1" || start.Params["model"] != "tomako/deepseek-v4-flash-vision-exp" {
		t.Fatalf("follow-up must continue the same thread and model: %#v", start.Params)
	}
	input := start.Params["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["text"] != finalReplyPrompt {
		t.Fatalf("follow-up input = %#v", input)
	}

	s.handleNotification("turn/started", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2"}}`))
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "Here is the product update post."})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2","status":"completed"}}`))

	final, sources, results, errors := drainReplyEvents(s)
	if final != "Here is the product update post." || len(sources) != 1 || sources[0] != "native_final" {
		t.Fatalf("final=%q sources=%v, want the follow-up's native final answer", final, sources)
	}
	if results != 1 || errors != 0 {
		t.Fatalf("results=%d errors=%d, want one completed reply", results, errors)
	}
	if got := len(turnStartLines(t, stdin)); got != 1 {
		t.Fatalf("turn/start requests = %d, want one", got)
	}
}

func TestFinalReplyFollowUp_StillEmptyIsReportedWithoutAnotherTry(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	endTurnWithReasoningOnly(s, "turn-1")
	startFollowUpTurn(t, s, stdin, "turn-2")

	endTurnWithReasoningOnly(s, "turn-2")
	final, _, results, errors := drainReplyEvents(s)
	if final != "" || results != 1 || errors != 0 {
		t.Fatalf("final=%q results=%d errors=%d, want the empty reply reported as before", final, results, errors)
	}
	time.Sleep(50 * time.Millisecond)
	if got := len(turnStartLines(t, stdin)); got != 1 {
		t.Fatalf("turn/start requests = %d, want no second follow-up", got)
	}
}

func TestFinalReplyFollowUp_NextMessageMayAskAgain(t *testing.T) {
	s, _ := finalReplyTestSession(t)
	s.stateMu.Lock()
	s.finalReplyRequested = true
	s.stateMu.Unlock()
	go func() { _ = s.Send("next message", nil, nil) }()
	stdin := s.stdin.(*lockedWriteCloser)
	waitForWrittenJSONLine(t, stdin)
	start := turnStartLines(t, stdin)[0]
	s.handleResponse(rpcResponseEnvelope{ID: float64(start.ID), Result: json.RawMessage(`{"turn":{"id":"turn-3"}}`)})
	waitForCurrentTurn(t, s, "turn-3")
	s.stateMu.Lock()
	requested := s.finalReplyRequested
	s.stateMu.Unlock()
	if requested {
		t.Fatal("a new user message must reset the one-follow-up budget")
	}
}

func TestFinalReplyFollowUp_InterruptedOrFailedTurnsAreNotContinued(t *testing.T) {
	for _, outcome := range []string{
		`"status":"interrupted"`,
		`"status":"failed"`,
		`"status":"failed","error":{"message":"gateway unavailable"}`,
	} {
		t.Run(outcome, func(t *testing.T) {
			s, stdin := finalReplyTestSession(t)
			s.handleItemCompleted(map[string]any{"type": "reasoning", "id": "reason", "summary": []any{map[string]any{"text": "thinking"}}})
			s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1",`+outcome+`}}`))
			_, _, results, errors := drainReplyEvents(s)
			if errors != 1 || results != 0 {
				t.Fatalf("errors=%d results=%d, want the failure reported", errors, results)
			}
			time.Sleep(50 * time.Millisecond)
			if stdin.String() != "" {
				t.Fatalf("an interrupted or failed turn must not be continued: %q", stdin.String())
			}
		})
	}
}

func TestFinalReplyFollowUp_ClosedOrCancelledSessionIsNotContinued(t *testing.T) {
	for name, stop := range map[string]func(*appServerSession){
		"closed":    func(s *appServerSession) { s.alive.Store(false) },
		"cancelled": func(s *appServerSession) { s.cancel() },
	} {
		t.Run(name, func(t *testing.T) {
			s, stdin := finalReplyTestSession(t)
			stop(s)
			endTurnWithReasoningOnly(s, "turn-1")
			_, _, results, _ := drainReplyEvents(s)
			if results != 1 {
				t.Fatalf("results=%d, want the turn completed as before", results)
			}
			time.Sleep(50 * time.Millisecond)
			if stdin.String() != "" {
				t.Fatalf("a stopped session must not be continued: %q", stdin.String())
			}
		})
	}
}

func TestFinalReplyFollowUp_AnsweredTurnsAreNotContinued(t *testing.T) {
	for name, answer := range map[string]map[string]any{
		"final_answer":     {"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": "Done."},
		"phase-less prose": {"type": "agentMessage", "id": "answer", "text": "Done."},
	} {
		t.Run(name, func(t *testing.T) {
			s, stdin := finalReplyTestSession(t)
			s.handleItemCompleted(answer)
			s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
			final, _, results, _ := drainReplyEvents(s)
			if final != "Done." || results != 1 {
				t.Fatalf("final=%q results=%d", final, results)
			}
			time.Sleep(50 * time.Millisecond)
			if stdin.String() != "" {
				t.Fatalf("an answered turn must not be continued: %q", stdin.String())
			}
		})
	}
}

func TestFinalReplyFollowUp_ToolDeliveredRuntimesAreNotContinued(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	s.runtime.Scene = "brand_analysis"
	s.brandFlow.corePublished = true
	endTurnWithReasoningOnly(s, "turn-1")
	_, _, results, _ := drainReplyEvents(s)
	if results != 1 {
		t.Fatalf("results=%d, want a tool-delivered runtime completed as before", results)
	}
	time.Sleep(50 * time.Millisecond)
	if stdin.String() != "" {
		t.Fatalf("a tool-delivered runtime must not be asked for prose: %q", stdin.String())
	}
}

func TestFinalReplyFollowUp_UnstartedFollowUpCompletesTheReplyAsBefore(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	endTurnWithReasoningOnly(s, "turn-1")
	waitForWrittenJSONLine(t, stdin)
	start := turnStartLines(t, stdin)[0]
	s.handleResponse(rpcResponseEnvelope{ID: float64(start.ID), Error: &rpcError{Message: "thread busy"}})
	final, results := waitForResult(t, s)
	if final != "" || results != 1 {
		t.Fatalf("final=%q results=%d, want the empty reply reported as before", final, results)
	}
	s.stateMu.Lock()
	current := s.currentTurn
	s.stateMu.Unlock()
	if current != "" {
		t.Fatalf("current turn = %q, want none after the reply completed", current)
	}
}
