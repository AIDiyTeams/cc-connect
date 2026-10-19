package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// replyLedgerPlatform records, per reply context, what a machine channel such
// as the bridge receives: ordinary replies and typed turn failures.
type replyLedgerPlatform struct {
	mu       sync.Mutex
	replies  map[any][]string
	failures map[any][]TurnFailure
}

func newReplyLedgerPlatform() *replyLedgerPlatform {
	return &replyLedgerPlatform{replies: map[any][]string{}, failures: map[any][]TurnFailure{}}
}

func (p *replyLedgerPlatform) Name() string               { return "ledger" }
func (p *replyLedgerPlatform) Start(MessageHandler) error { return nil }
func (p *replyLedgerPlatform) Stop() error                { return nil }
func (p *replyLedgerPlatform) Send(_ context.Context, replyCtx any, content string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies[replyCtx] = append(p.replies[replyCtx], content)
	return nil
}
func (p *replyLedgerPlatform) Reply(ctx context.Context, replyCtx any, content string) error {
	return p.Send(ctx, replyCtx, content)
}
func (p *replyLedgerPlatform) ReportTurnFailure(_ context.Context, replyCtx any, failure TurnFailure) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures[replyCtx] = append(p.failures[replyCtx], failure)
	return nil
}

// Like the bridge, queue admission is a status, not a reply.
func (p *replyLedgerPlatform) ReportTurnDispatchStatus(context.Context, any, TurnDispatchStatus) error {
	return nil
}

func (p *replyLedgerPlatform) of(replyCtx any) ([]string, []TurnFailure) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.replies[replyCtx]...), append([]TurnFailure(nil), p.failures[replyCtx]...)
}

func waitUntil(t *testing.T, reason string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", reason)
}

// A turn cut by its time limit is torn down after a short grace. A message
// that arrived in that window never reached the Agent: it must run on the
// next Agent, not come back as an error text the channel takes for the answer
// to that message (Test 2026-10-18, cmsg-8119090d20524ef3).
func TestMessageQueuedBehindATimedOutTurnRunsOnTheNextAgent(t *testing.T) {
	p := newReplyLedgerPlatform()
	agent := &cujAgent{}
	agent.delayNextSession(10 * time.Second)
	e := NewEngine("test", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	defer e.Stop()
	e.SetMaxTurnTime(150 * time.Millisecond)
	e.turnStopGrace = 300 * time.Millisecond

	send := func(id, content string) {
		e.ReceiveMessage(p, &Message{SessionKey: "ledger:room", Platform: "ledger", MessageID: id,
			UserID: "owner", UserName: "owner", Content: content, ReplyCtx: id})
	}
	send("cmsg-timed-out", "hand this to Tato and wait")
	waitUntil(t, "the first turn's time limit", 3*time.Second, func() bool {
		_, failures := p.of("cmsg-timed-out")
		return len(failures) > 0
	})
	if _, failures := p.of("cmsg-timed-out"); failures[0].Code != "AGENT_TURN_TIMEOUT" {
		t.Fatalf("first turn failure = %+v, want AGENT_TURN_TIMEOUT", failures[0])
	}
	// The team update arrives while the timed-out turn is still being torn down.
	send("cmsg-team-update", "Team update")

	waitUntil(t, "the team update's own answer", 5*time.Second, func() bool {
		replies, failures := p.of("cmsg-team-update")
		return len(replies) > 0 || len(failures) > 0
	})
	replies, failures := p.of("cmsg-team-update")
	if len(failures) > 0 || len(replies) != 1 || replies[0] != "ok" {
		t.Fatalf("team update got replies %q and failures %+v, want only the Agent's answer", replies, failures)
	}
	agent.mu.Lock()
	sessions := append([]*cujAgentSession(nil), agent.sessions...)
	agent.mu.Unlock()
	if len(sessions) != 2 {
		t.Fatalf("agent sessions = %d, want the timed-out one and a fresh one", len(sessions))
	}
	if got := sessions[0].getSentPrompts(); len(got) != 1 {
		t.Fatalf("timed-out agent received %q, want only its own prompt", got)
	}
	if got := sessions[1].getSentPrompts(); len(got) != 1 || got[0] != "Team update" {
		t.Fatalf("fresh agent received %q, want the team update", got)
	}
}

// A queued message dropped by a stop never reached the Agent. A channel that
// takes typed failures must get one, not an error text posing as its answer.
func TestDroppedQueuedMessageIsATypedFailure(t *testing.T) {
	p := newReplyLedgerPlatform()
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	state := &interactiveState{
		platform: p,
		replyCtx: "cmsg-running",
		pendingMessages: []queuedMessage{
			{platform: p, replyCtx: "cmsg-queued-1"},
			{platform: p, replyCtx: "cmsg-queued-2"},
		},
	}

	e.notifyDroppedQueuedMessages(state, fmt.Errorf("session reset"))

	for _, id := range []string{"cmsg-queued-1", "cmsg-queued-2"} {
		replies, failures := p.of(id)
		if len(replies) != 0 {
			t.Fatalf("%s got replies %q, want none", id, replies)
		}
		if len(failures) != 1 || failures[0].Code != TurnQueuedMessageDropped {
			t.Fatalf("%s got failures %+v, want one %s", id, failures, TurnQueuedMessageDropped)
		}
	}
}
