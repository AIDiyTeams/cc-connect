package core

import (
	"context"
	"testing"
	"time"
)

type turnFailureCapturePlatform struct {
	failure *TurnFailure
}

func (p *turnFailureCapturePlatform) Name() string                             { return "turn-failure-capture" }
func (p *turnFailureCapturePlatform) Start(MessageHandler) error               { return nil }
func (p *turnFailureCapturePlatform) Stop() error                              { return nil }
func (p *turnFailureCapturePlatform) Send(context.Context, any, string) error  { return nil }
func (p *turnFailureCapturePlatform) Reply(context.Context, any, string) error { return nil }
func (p *turnFailureCapturePlatform) ReportTurnFailure(
	_ context.Context,
	_ any,
	failure TurnFailure,
) error {
	p.failure = &failure
	return nil
}

func TestEngineFailTurnPrefersTypedFailure(t *testing.T) {
	p := &turnFailureCapturePlatform{}
	e := &Engine{ctx: context.Background()}

	e.failTurn(p, "reply-1", "AGENT_RUNTIME_ERROR", "agent failed")

	if p.failure == nil {
		t.Fatal("expected typed turn failure")
	}
	if p.failure.Code != "AGENT_RUNTIME_ERROR" || p.failure.Message != "agent failed" {
		t.Fatalf("unexpected failure: %+v", *p.failure)
	}
}

func TestProcessInteractiveEvents_EmptyAgentResultIsTypedFailure(t *testing.T) {
	p := &turnFailureCapturePlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	sessionKey := "bridge:user-empty-agent-result"
	session := e.sessions.GetOrCreateActive(sessionKey)
	agentSession := newControllableSession("s-empty-agent-result")
	state := &interactiveState{
		agentSession: agentSession,
		platform:     p,
		replyCtx:     "cmsg-empty-agent-result",
	}
	e.interactiveStates[sessionKey] = state

	agentSession.events <- Event{Type: EventResult, Content: "", Done: true}
	e.processInteractiveEvents(
		state,
		session,
		e.sessions,
		sessionKey,
		"llm-empty-agent-result",
		time.Now(),
		nil,
		nil,
		state.replyCtx,
	)

	if p.failure == nil {
		t.Fatal("expected empty Agent result to be reported as a typed failure")
	}
	if p.failure.Code != "AGENT_EMPTY_RESPONSE" {
		t.Fatalf("failure code = %q, want AGENT_EMPTY_RESPONSE", p.failure.Code)
	}
	if p.failure.Message != e.i18n.T(MsgAgentEmptyResponse) {
		t.Fatalf("failure message = %q, want localized empty-result error", p.failure.Message)
	}
	if got := session.GetHistory(0); len(got) != 0 {
		t.Fatalf("history = %#v, want no fake assistant reply", got)
	}
}
