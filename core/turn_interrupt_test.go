package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// interruptibleAgent starts sessions that can end a turn in place, like the
// Codex app-server. A prompt starting with "hang" runs until interrupted.
type interruptibleAgent struct {
	mu       sync.Mutex
	sessions []*interruptibleSession
	// refuseInterrupt makes every session fail Interrupt.
	refuseInterrupt bool
}

func (a *interruptibleAgent) Name() string { return "interruptible" }
func (a *interruptibleAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := &interruptibleSession{events: make(chan Event, 16), refuse: a.refuseInterrupt}
	a.sessions = append(a.sessions, s)
	return s, nil
}
func (a *interruptibleAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return nil, nil
}
func (a *interruptibleAgent) Stop() error { return nil }

func (a *interruptibleAgent) started() []*interruptibleSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*interruptibleSession(nil), a.sessions...)
}

type interruptibleSession struct {
	events     chan Event
	mu         sync.Mutex
	prompts    []string
	turn       chan struct{} // open while a "hang" turn runs
	interrupts int
	closed     bool
	refuse     bool
}

func (s *interruptibleSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, prompt)
	if strings.HasPrefix(prompt, "hang") {
		turn := make(chan struct{})
		s.turn = turn
		go func() {
			<-turn
			s.events <- Event{Type: EventText, Content: "half an answer"}
			s.events <- Event{Type: EventResult, Done: true}
		}()
		return nil
	}
	s.events <- Event{Type: EventResult, Content: "ok", Done: true}
	return nil
}

func (s *interruptibleSession) Interrupt(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse || s.turn == nil {
		return errors.New("no interruptible turn")
	}
	close(s.turn)
	s.turn = nil
	s.interrupts++
	return nil
}

func (s *interruptibleSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *interruptibleSession) Events() <-chan Event                             { return s.events }
func (s *interruptibleSession) CurrentSessionID() string                         { return "interruptible-thread" }
func (s *interruptibleSession) Alive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}
func (s *interruptibleSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *interruptibleSession) snapshot() (prompts []string, interrupts int, closed bool, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...), s.interrupts, s.closed, s.turn != nil
}

func newInterruptTestEngine(t *testing.T, agent Agent) (*Engine, *replyLedgerPlatform, func(id, content string)) {
	t.Helper()
	p := newReplyLedgerPlatform()
	e := NewEngine("test", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	send := func(id, content string) {
		e.ReceiveMessage(p, &Message{SessionKey: "ledger:room", Platform: "ledger", MessageID: id,
			UserID: "owner", UserName: "owner", Content: content, ReplyCtx: id})
	}
	return e, p, send
}

// A turn cut by its time limit is interrupted in place: the same agent process
// serves the next message, so its history stays whole and nothing restarts.
func TestTimedOutTurnEndsInPlaceAndTheSameAgentServesTheNextMessage(t *testing.T) {
	agent := &interruptibleAgent{}
	e, p, send := newInterruptTestEngine(t, agent)
	e.SetMaxTurnTime(150 * time.Millisecond)
	e.turnStopGrace = 5 * time.Second

	send("cmsg-timed-out", "hang on this long task")
	waitUntil(t, "the first turn's time limit", 3*time.Second, func() bool {
		_, failures := p.of("cmsg-timed-out")
		return len(failures) > 0
	})
	replies, failures := p.of("cmsg-timed-out")
	if len(failures) != 1 || failures[0].Code != "AGENT_TURN_TIMEOUT" || len(replies) != 0 {
		t.Fatalf("timed-out turn got replies %q and failures %+v, want only AGENT_TURN_TIMEOUT", replies, failures)
	}

	send("cmsg-next", "Team update")
	waitUntil(t, "the next message's answer", 3*time.Second, func() bool {
		replies, failures := p.of("cmsg-next")
		return len(replies) > 0 || len(failures) > 0
	})
	if replies, failures := p.of("cmsg-next"); len(failures) > 0 || len(replies) != 1 || replies[0] != "ok" {
		t.Fatalf("next message got replies %q and failures %+v, want only the Agent's answer", replies, failures)
	}

	sessions := agent.started()
	if len(sessions) != 1 {
		t.Fatalf("agent sessions = %d, want the interrupted one reused", len(sessions))
	}
	prompts, interrupts, closed, _ := sessions[0].snapshot()
	if interrupts != 1 || closed {
		t.Fatalf("interrupts = %d, closed = %v; want one interrupt and the process kept", interrupts, closed)
	}
	if len(prompts) != 2 || prompts[1] != "Team update" {
		t.Fatalf("agent received %q, want the timed-out prompt then the next message", prompts)
	}
}

// /stop ends the running turn in place: the queued message from before the
// stop is dropped as before, and the next message reuses the same agent.
func TestStopEndsTheTurnInPlaceAndKeepsTheAgent(t *testing.T) {
	agent := &interruptibleAgent{}
	e, p, send := newInterruptTestEngine(t, agent)
	e.turnStopGrace = 5 * time.Second

	send("cmsg-running", "hang until stopped")
	waitUntil(t, "the turn to reach the agent", 3*time.Second, func() bool {
		sessions := agent.started()
		if len(sessions) == 0 {
			return false
		}
		_, _, _, running := sessions[0].snapshot()
		return running
	})
	send("cmsg-queued", "queued before the stop")
	waitUntil(t, "the message to queue", 3*time.Second, func() bool {
		e.interactiveMu.Lock()
		state := e.interactiveStates[e.interactiveKeyForSessionKey("ledger:room")]
		e.interactiveMu.Unlock()
		if state == nil {
			return false
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.pendingMessages) == 1
	})

	send("cmsg-stop", "/stop")
	waitUntil(t, "the stop acknowledgement", 3*time.Second, func() bool {
		replies, _ := p.of("cmsg-stop")
		return len(replies) > 0
	})
	if replies, _ := p.of("cmsg-stop"); !strings.Contains(replies[0], "stopped") {
		t.Fatalf("stop reply = %q", replies)
	}
	if replies, failures := p.of("cmsg-queued"); len(replies) != 0 || len(failures) != 1 || failures[0].Code != TurnQueuedMessageDropped {
		t.Fatalf("queued message got replies %q and failures %+v, want one %s", replies, failures, TurnQueuedMessageDropped)
	}

	send("cmsg-next", "continue with the correction")
	waitUntil(t, "the next message's answer", 3*time.Second, func() bool {
		replies, failures := p.of("cmsg-next")
		return len(replies) > 0 || len(failures) > 0
	})
	if replies, failures := p.of("cmsg-next"); len(failures) > 0 || len(replies) != 1 || replies[0] != "ok" {
		t.Fatalf("next message got replies %q and failures %+v, want only the Agent's answer", replies, failures)
	}
	if replies, failures := p.of("cmsg-running"); len(replies) != 0 || len(failures) != 0 {
		t.Fatalf("stopped turn got replies %q and failures %+v, want nothing after the stop", replies, failures)
	}

	sessions := agent.started()
	if len(sessions) != 1 {
		t.Fatalf("agent sessions = %d, want the interrupted one reused", len(sessions))
	}
	prompts, interrupts, closed, _ := sessions[0].snapshot()
	if interrupts != 1 || closed {
		t.Fatalf("interrupts = %d, closed = %v; want one interrupt and the process kept", interrupts, closed)
	}
	if len(prompts) != 2 || prompts[1] != "continue with the correction" {
		t.Fatalf("agent received %q, want the stopped prompt then the next message", prompts)
	}
}

// An agent that cannot interrupt the turn is still closed, and the message
// queued behind the timed-out turn runs on a fresh one.
func TestTimedOutTurnThatCannotBeInterruptedClosesTheAgent(t *testing.T) {
	agent := &interruptibleAgent{refuseInterrupt: true}
	e, p, send := newInterruptTestEngine(t, agent)
	e.SetMaxTurnTime(150 * time.Millisecond)
	e.turnStopGrace = 5 * time.Second

	send("cmsg-timed-out", "hang on this long task")
	waitUntil(t, "the first turn's time limit", 3*time.Second, func() bool {
		_, failures := p.of("cmsg-timed-out")
		return len(failures) > 0
	})
	send("cmsg-next", "Team update")
	waitUntil(t, "the next message's answer", 3*time.Second, func() bool {
		replies, failures := p.of("cmsg-next")
		return len(replies) > 0 || len(failures) > 0
	})
	if replies, failures := p.of("cmsg-next"); len(failures) > 0 || len(replies) != 1 || replies[0] != "ok" {
		t.Fatalf("next message got replies %q and failures %+v, want only the Agent's answer", replies, failures)
	}
	sessions := agent.started()
	if len(sessions) != 2 {
		t.Fatalf("agent sessions = %d, want the refused one closed and a fresh one", len(sessions))
	}
	if _, _, closed, _ := sessions[0].snapshot(); !closed {
		t.Fatal("agent that could not interrupt its turn was not closed")
	}
}
