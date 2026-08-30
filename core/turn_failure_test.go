package core

import (
	"context"
	"testing"
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
