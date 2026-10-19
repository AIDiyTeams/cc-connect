package core

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// launchFakeSession records every prompt the agent receives. It is safe for
// the concurrent Close/Alive calls a stop makes while a turn unwinds.
type launchFakeSession struct {
	id        string
	alive     atomic.Bool
	events    chan Event
	sends     chan string
	closeOnce sync.Once
}

func newLaunchFakeSession(id string) *launchFakeSession {
	s := &launchFakeSession{id: id, events: make(chan Event, 16), sends: make(chan string, 16)}
	s.alive.Store(true)
	return s
}

func (s *launchFakeSession) Send(prompt string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sends <- prompt
	return nil
}
func (s *launchFakeSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *launchFakeSession) Events() <-chan Event                             { return s.events }
func (s *launchFakeSession) CurrentSessionID() string                         { return s.id }
func (s *launchFakeSession) Alive() bool                                      { return s.alive.Load() }
func (s *launchFakeSession) Close() error {
	s.closeOnce.Do(func() { s.alive.Store(false) })
	return nil
}

// launchFakeAgent hands out sessions in order; a startup can be held open to
// model a slow resume.
type launchFakeAgent struct {
	mu       sync.Mutex
	sessions []*launchFakeSession
	starts   int
	// holdFirst, when set, blocks the first StartSession until released and
	// reports entry on startEntered.
	holdFirst    chan struct{}
	startEntered chan struct{}
}

func (a *launchFakeAgent) Name() string { return "launch-fake" }
func (a *launchFakeAgent) StartSession(_ context.Context, _ string) (AgentSession, error) {
	a.mu.Lock()
	n := a.starts
	a.starts++
	s := a.sessions[n]
	a.mu.Unlock()
	if n == 0 && a.holdFirst != nil {
		a.startEntered <- struct{}{}
		<-a.holdFirst
	}
	return s, nil
}
func (a *launchFakeAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) { return nil, nil }
func (a *launchFakeAgent) Stop() error                                              { return nil }

func (a *launchFakeAgent) startCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.starts
}

func launchTestMessage(key, id, content string) *Message {
	return &Message{SessionKey: key, Platform: "test", MessageID: id, UserID: "u1", UserName: "u1", Content: content, ReplyCtx: "ctx-" + id}
}

func expectSend(t *testing.T, s *launchFakeSession, want string) {
	t.Helper()
	select {
	case got := <-s.sends:
		if got != want {
			t.Fatalf("agent received %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("agent did not receive %q", want)
	}
}

func expectNoSend(t *testing.T, s *launchFakeSession) {
	t.Helper()
	select {
	case got := <-s.sends:
		t.Fatalf("stopped turn reached the agent: %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func waitSessionUnlocked(t *testing.T, session *Session) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !session.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("stopped turn kept the session lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	session.Unlock()
}

func launchPending(e *Engine, key string) bool {
	e.launchMu.Lock()
	defer e.launchMu.Unlock()
	return len(e.turnLaunches[key]) > 0
}

// Regression for Test csess-6ae0172cb8a44ce6 (2026-10-19): the user pressed stop
// 0.6 s after sending, while the turn held the session lock and only a startup
// placeholder existed. The stop removed the placeholder, but the turn went on to
// start the agent and run the cancelled prompt, and the resend queued behind it.
func TestStopOnStartupPlaceholder_CancelsTurnBeforeAgentStarts(t *testing.T) {
	first := newLaunchFakeSession("agent-1")
	agent := &launchFakeAgent{sessions: []*launchFakeSession{first}}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	key := "test:placeholder-stop"

	msg := launchTestMessage(key, "m1", "deep research")
	session := e.sessions.GetOrCreateActive(key)
	launch := e.tryLockSessionWithQueueState(session, key, p, msg.ReplyCtx, msg.MessageID)
	if launch == nil {
		t.Fatal("first message failed to take the session")
	}
	// The card stop lands before the processing goroutine reaches the agent.
	e.executeCardAction("/stop", "", key)
	turnDone := make(chan struct{})
	go func() {
		e.processInteractiveTurn(p, msg, session, agent, e.sessions, key, "", key, launch)
		close(turnDone)
	}()
	select {
	case <-turnDone:
	case <-time.After(3 * time.Second):
		t.Fatal("stopped turn kept running instead of unwinding")
	}

	if n := agent.startCount(); n != 0 {
		t.Fatalf("stopped turn started %d agent sessions", n)
	}
	waitSessionUnlocked(t, session)
	if len(session.GetHistory(0)) != 0 {
		t.Fatal("cancelled message was recorded as conversation history")
	}

	e.ReceiveMessage(p, launchTestMessage(key, "m2", "resend"))
	expectSend(t, first, "resend")
	first.events <- Event{Type: EventResult, Content: "done", Done: true}
	waitSessionUnlocked(t, session)
}

// A stop during a slow agent startup must cancel the turn even though startup
// holds interactiveMu; the agent must never receive the cancelled prompt and
// the next message must start at once.
func TestStopDuringSlowAgentStartup_AgentNeverReceivesPrompt(t *testing.T) {
	first := newLaunchFakeSession("agent-1")
	second := newLaunchFakeSession("agent-2")
	agent := &launchFakeAgent{
		sessions:     []*launchFakeSession{first, second},
		holdFirst:    make(chan struct{}),
		startEntered: make(chan struct{}, 1),
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	key := "test:slow-start-stop"

	e.ReceiveMessage(p, launchTestMessage(key, "m1", "deep research"))
	select {
	case <-agent.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent startup never began")
	}

	stopped := make(chan bool, 1)
	go func() { stopped <- e.stopInteractiveSession(key, p, nil) }()
	deadline := time.Now().Add(3 * time.Second)
	for launchPending(e, key) {
		if time.Now().After(deadline) {
			t.Fatal("stop did not cancel the launch while startup was in progress")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(agent.holdFirst)
	select {
	case ok := <-stopped:
		if !ok {
			t.Fatal("stop reported no execution for a starting turn")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not return after startup finished")
	}

	expectNoSend(t, first)
	session := e.sessions.GetOrCreateActive(key)
	waitSessionUnlocked(t, session)
	if first.Alive() {
		t.Fatal("agent started for the stopped turn was left running")
	}

	start := time.Now()
	e.ReceiveMessage(p, launchTestMessage(key, "m2", "resend"))
	expectSend(t, second, "resend")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("next message waited %v behind the stopped turn", elapsed)
	}
	second.events <- Event{Type: EventResult, Content: "done", Done: true}
	waitSessionUnlocked(t, session)
	for _, sent := range p.getSent() {
		if sent == e.i18n.T(MsgMessageQueued) {
			t.Fatal("resend was queued behind the stopped turn")
		}
	}
}

// A recall during startup cancels only that message: the agent that was
// starting stays usable and serves the next message.
func TestRecallDuringAgentStartup_CancelsOnlyThatMessage(t *testing.T) {
	first := newLaunchFakeSession("agent-1")
	agent := &launchFakeAgent{
		sessions:     []*launchFakeSession{first},
		holdFirst:    make(chan struct{}),
		startEntered: make(chan struct{}, 1),
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	key := "test:recall-start"

	e.ReceiveMessage(p, launchTestMessage(key, "m1", "deep research"))
	select {
	case <-agent.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent startup never began")
	}
	recall := launchTestMessage(key, "m1", "")
	recall.Recalled = true
	e.ReceiveMessage(p, recall)
	if launchPending(e, key) {
		t.Fatal("recall did not cancel the starting turn")
	}
	close(agent.holdFirst)

	expectNoSend(t, first)
	session := e.sessions.GetOrCreateActive(key)
	waitSessionUnlocked(t, session)
	if !first.Alive() {
		t.Fatal("recall closed an agent that never ran the recalled message")
	}

	e.ReceiveMessage(p, launchTestMessage(key, "m2", "next"))
	expectSend(t, first, "next")
	first.events <- Event{Type: EventResult, Content: "done", Done: true}
	waitSessionUnlocked(t, session)
}

// A turn locked without a startup placeholder (cron, skills, custom commands)
// is still stoppable before its agent starts.
func TestStopFindsLaunchWithoutPlaceholder(t *testing.T) {
	e := newTestEngine()
	defer e.Stop()
	launch := e.registerTurnLaunch("ws:cron-key", "")
	if got := e.findInteractiveKeyForSession("cron-key"); got != "ws:cron-key" {
		t.Fatalf("findInteractiveKeyForSession = %q, want launch key", got)
	}
	if !e.stopInteractiveSession("ws:cron-key", nil, nil) {
		t.Fatal("stop reported no execution for a launching turn")
	}
	if !e.turnLaunchCancelled(launch) {
		t.Fatal("launch not cancelled")
	}
	if e.commitTurnLaunch("ws:cron-key", launch) {
		t.Fatal("cancelled launch committed")
	}
}
