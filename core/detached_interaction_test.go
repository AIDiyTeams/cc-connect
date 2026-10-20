package core

import (
	"context"
	"errors"
	"testing"
)

type interactionCapturingPlatform struct {
	stubPlatformEngine
	requests []InteractionRequest
}

func (p *interactionCapturingPlatform) SendInteraction(_ context.Context, _ any, request InteractionRequest) error {
	p.requests = append(p.requests, request)
	return nil
}

type steeringAgentSession struct {
	recordingAgentSession
	steered []PermissionResult
	fail    error
}

func (s *steeringAgentSession) AnswerDetached(_ string, result PermissionResult) error {
	if s.fail != nil {
		return s.fail
	}
	s.steered = append(s.steered, result)
	return nil
}

func detachedEngine(t *testing.T, agent AgentSession) (*Engine, *interactiveState) {
	t.Helper()
	e := newTestEngine()
	state := &interactiveState{agentSession: agent}
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()
	return e, state
}

func browserHelpEvent() Event {
	return Event{
		Type: EventPermissionRequest, RequestID: "browser-help-call-1", ToolName: "AskUserQuestion", NonBlocking: true,
		Questions: []UserQuestion{{ID: "browser_help", Question: "Sign in to these sites.", Options: []UserQuestionOption{
			{ID: "browser_help-done", Label: "Done, continue"}, {ID: "browser_help-decline", Label: "Not now"}}}},
		UserAction: &InteractionUserAction{Kind: "sign_in", Site: "producthunt.com", Sites: []string{"producthunt.com", "startupbase.io"}, Browser: "local-chrome"},
	}
}

// A card that does not pause the turn reaches the app marked so, and leaves the turn running.
func TestNonBlockingQuestionIsShownWithoutPausingTheTurn(t *testing.T) {
	agent := &steeringAgentSession{}
	e, state := detachedEngine(t, agent)
	platform := &interactionCapturingPlatform{}
	e.showDetachedInteraction(platform, nil, state, browserHelpEvent())
	if len(platform.requests) != 1 || platform.requests[0].Blocking == nil || *platform.requests[0].Blocking ||
		len(platform.requests[0].UserAction.Sites) != 2 {
		t.Fatalf("requests = %#v", platform.requests)
	}
	if state.pending != nil {
		t.Fatal("the turn must not be paused on the card")
	}
	id := platform.requests[0].InteractionID
	if err := e.RespondInteraction("test:chat:user1", id, "", map[string][]string{"browser_help": {"browser_help-done"}}); err != nil {
		t.Fatal(err)
	}
	if len(agent.steered) != 1 || agent.calls != 0 {
		t.Fatalf("the answer goes into the running turn, not a paused tool: steered=%d permission=%d", len(agent.steered), agent.calls)
	}
	if err := e.RespondInteraction("test:chat:user1", id, "", map[string][]string{"browser_help": {"browser_help-done"}}); !errors.Is(err, ErrInteractionNotPending) {
		t.Fatalf("a card is answered once, got %v", err)
	}
}

// With no turn running to take it, the answer is reported as not pending so the app continues from it.
func TestNonBlockingAnswerWithoutARunningTurnIsNotPending(t *testing.T) {
	agent := &steeringAgentSession{fail: errors.New("no running turn")}
	e, state := detachedEngine(t, agent)
	platform := &interactionCapturingPlatform{}
	e.showDetachedInteraction(platform, nil, state, browserHelpEvent())
	err := e.RespondInteraction("test:chat:user1", platform.requests[0].InteractionID, "", map[string][]string{"browser_help": {"browser_help-done"}})
	if !errors.Is(err, ErrInteractionNotPending) {
		t.Fatalf("err = %v", err)
	}
}
