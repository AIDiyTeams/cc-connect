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

// Agent startup must not hold interactiveMu: on Test a codex spawn takes
// ~0.7 s and a resume several seconds, and the bridge read loop takes
// interactiveMu for every conversation's incoming message. While conversation
// A's agent is starting, conversation B's message must still take its session
// and publish its queue state, and both agents must start in turn.
func TestAgentStartupDoesNotBlockOtherConversations(t *testing.T) {
	first := newLaunchFakeSession("agent-a")
	second := newLaunchFakeSession("agent-b")
	agent := &launchFakeAgent{
		sessions:     []*launchFakeSession{first, second},
		holdFirst:    make(chan struct{}),
		startEntered: make(chan struct{}, 1),
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	keyA, keyB := "test:startup-a", "test:startup-b"

	e.ReceiveMessage(p, launchTestMessage(keyA, "a1", "hello from a"))
	select {
	case <-agent.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent startup for A never began")
	}
	released := false
	defer func() {
		if !released {
			close(agent.holdFirst)
		}
	}()

	msgB := launchTestMessage(keyB, "b1", "hello from b")
	sessionB := e.sessions.GetOrCreateActive(keyB)
	accepted := make(chan *turnLaunch, 1)
	go func() { accepted <- e.tryLockSessionWithQueueState(sessionB, keyB, p, msgB.ReplyCtx, msgB.MessageID) }()
	var launchB *turnLaunch
	select {
	case launchB = <-accepted:
	case <-time.After(time.Second):
		t.Fatal("conversation B's message waited for conversation A's agent startup")
	}
	if launchB == nil {
		t.Fatal("conversation B's session was reported busy")
	}
	e.interactiveMu.Lock()
	_, queued := e.interactiveStates[keyB]
	e.interactiveMu.Unlock()
	if !queued {
		t.Fatal("conversation B's queue state was not published")
	}

	turnB := make(chan struct{})
	go func() {
		e.processInteractiveTurn(p, msgB, sessionB, agent, e.sessions, keyB, "", keyB, launchB)
		close(turnB)
	}()
	// B's startup shares A's agent instance, so it waits for A's startup
	// (the injected env and prompt belong to one startup at a time).
	if n := agent.startCount(); n != 1 {
		t.Fatalf("B started its agent during A's startup: %d starts", n)
	}

	close(agent.holdFirst)
	released = true
	expectSend(t, first, "hello from a")
	expectSend(t, second, "hello from b")
	first.events <- Event{Type: EventResult, Content: "done", Done: true}
	second.events <- Event{Type: EventResult, Content: "done", Done: true}
	select {
	case <-turnB:
	case <-time.After(3 * time.Second):
		t.Fatal("conversation B's turn did not finish")
	}
	waitSessionUnlocked(t, e.sessions.GetOrCreateActive(keyA))
	waitSessionUnlocked(t, sessionB)
}

// A reset of the conversation (/new, /model, provider switch) while its agent
// is starting no longer waits for that startup, so the agent configured before
// the reset must not be published: it is closed and the next startup is fresh.
func TestResetDuringAgentStartup_DiscardsStartedAgent(t *testing.T) {
	first := newLaunchFakeSession("")
	second := newLaunchFakeSession("")
	agent := &launchFakeAgent{
		sessions:     []*launchFakeSession{first, second},
		holdFirst:    make(chan struct{}),
		startEntered: make(chan struct{}, 1),
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	key := "test:reset-start"
	session := e.sessions.GetOrCreateActive(key)

	got := make(chan *interactiveState, 1)
	go func() {
		got <- e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")
	}()
	select {
	case <-agent.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent startup never began")
	}
	cleaned := make(chan struct{})
	go func() {
		e.cleanupInteractiveState(key)
		close(cleaned)
	}()
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("reset waited for the conversation's agent startup")
	}
	close(agent.holdFirst)

	var state *interactiveState
	select {
	case state = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("startup did not return")
	}
	if !state.isStopped() {
		t.Fatal("agent started before the reset was returned as live")
	}
	deadline := time.Now().Add(3 * time.Second)
	for first.Alive() {
		if time.Now().After(deadline) {
			t.Fatal("agent started before the reset was left running")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.interactiveMu.Lock()
	_, published := e.interactiveStates[key]
	inFlight := len(e.interactiveStartups)
	e.interactiveMu.Unlock()
	if published {
		t.Fatal("agent started before the reset was published")
	}
	if inFlight != 0 {
		t.Fatalf("%d startups still registered", inFlight)
	}
	e.startupMuGuard.Lock()
	locks := len(e.agentStartupLocks)
	e.startupMuGuard.Unlock()
	if locks != 0 {
		t.Fatalf("%d agent startup locks left behind", locks)
	}

	if next := e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, ""); next.agentSession != AgentSession(second) {
		t.Fatal("next turn did not start a fresh agent")
	}
}

// fastPath is how long an interactiveMu-only operation may take while another
// conversation's agent is starting; the startup itself is held indefinitely.
const fastPath = 100 * time.Millisecond

// While conversation A's agent is starting, conversation B's card /stop must
// return at once and cancel B's turn before its agent starts.
func TestAgentStartupDoesNotBlockOtherConversationsStop(t *testing.T) {
	first := newLaunchFakeSession("agent-a")
	agent := &launchFakeAgent{
		sessions:     []*launchFakeSession{first},
		holdFirst:    make(chan struct{}),
		startEntered: make(chan struct{}, 1),
	}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	keyA, keyB := "test:stop-a", "test:stop-b"

	e.ReceiveMessage(p, launchTestMessage(keyA, "a1", "hello from a"))
	select {
	case <-agent.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent startup for A never began")
	}
	released := false
	defer func() {
		if !released {
			close(agent.holdFirst)
		}
	}()

	msgB := launchTestMessage(keyB, "b1", "hello from b")
	sessionB := e.sessions.GetOrCreateActive(keyB)
	start := time.Now()
	launchB := e.tryLockSessionWithQueueState(sessionB, keyB, p, msgB.ReplyCtx, msgB.MessageID)
	acceptElapsed := time.Since(start)
	if launchB == nil {
		t.Fatal("conversation B's session was reported busy")
	}

	stopped := make(chan struct{})
	start = time.Now()
	go func() {
		e.executeCardAction("/stop", "", keyB)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(fastPath):
		t.Fatal("conversation B's card /stop waited for conversation A's agent startup")
	}
	stopElapsed := time.Since(start)
	t.Logf("during A's startup: B accepted in %v, B's /stop returned in %v", acceptElapsed, stopElapsed)
	if acceptElapsed > fastPath {
		t.Fatalf("conversation B's message took %v during A's startup", acceptElapsed)
	}
	if !e.turnLaunchCancelled(launchB) {
		t.Fatal("B's /stop did not cancel B's starting turn")
	}

	turnB := make(chan struct{})
	go func() {
		e.processInteractiveTurn(p, msgB, sessionB, agent, e.sessions, keyB, "", keyB, launchB)
		close(turnB)
	}()
	select {
	case <-turnB:
	case <-time.After(3 * time.Second):
		t.Fatal("B's stopped turn did not unwind")
	}

	close(agent.holdFirst)
	released = true
	expectSend(t, first, "hello from a")
	if n := agent.startCount(); n != 1 {
		t.Fatalf("stopped turn B started an agent: %d starts", n)
	}
	first.events <- Event{Type: EventResult, Content: "done", Done: true}
	waitSessionUnlocked(t, e.sessions.GetOrCreateActive(keyA))
	waitSessionUnlocked(t, sessionB)
}

// Startups racing for one conversation must spawn a single agent process.
func TestConcurrentStartupSameKey_SpawnsOneAgent(t *testing.T) {
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
	key := "test:same-key"
	session := e.sessions.GetOrCreateActive(key)

	const callers = 4
	got := make(chan *interactiveState, callers)
	for i := 0; i < callers; i++ {
		go func() { got <- e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "") }()
	}
	select {
	case <-agent.startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("agent startup never began")
	}
	// Let the other callers reach the startup lock before releasing.
	time.Sleep(50 * time.Millisecond)
	close(agent.holdFirst)

	var states []*interactiveState
	for i := 0; i < callers; i++ {
		select {
		case s := <-got:
			states = append(states, s)
		case <-time.After(3 * time.Second):
			t.Fatal("a concurrent startup did not return")
		}
	}
	if n := agent.startCount(); n != 1 {
		t.Fatalf("concurrent startups spawned %d agents", n)
	}
	for _, s := range states {
		if s != states[0] || s.agentSession != AgentSession(first) {
			t.Fatal("concurrent startups returned different states")
		}
	}
}

// recycleFakeAgent records whether the previous agent was already closed when
// the replacement started.
type recycleFakeAgent struct {
	launchFakeAgent
	old           *launchFakeSession
	oldAliveAtNew atomic.Bool
}

func (a *recycleFakeAgent) StartSession(ctx context.Context, id string) (AgentSession, error) {
	a.oldAliveAtNew.Store(a.old.Alive())
	return a.launchFakeAgent.StartSession(ctx, id)
}

// A live agent bound to another agent session is closed before its
// replacement starts, and the replacement is published.
func TestRecycleClosesStaleAgentBeforeStart(t *testing.T) {
	old := newLaunchFakeSession("stale-id")
	fresh := newLaunchFakeSession("fresh-id")
	agent := &recycleFakeAgent{launchFakeAgent: launchFakeAgent{sessions: []*launchFakeSession{fresh}}, old: old}
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
	defer e.Stop()
	key := "test:recycle"
	session := e.sessions.GetOrCreateActive(key)
	stale := &interactiveState{agentSession: old, platform: p, replyCtx: "ctx", agent: agent}
	e.interactiveMu.Lock()
	e.interactiveStates[key] = stale
	e.interactiveMu.Unlock()

	// /new cleared the binding, so the live process must not be reused.
	state := e.getOrCreateInteractiveStateWith(key, p, "ctx", session, e.sessions, nil, "")
	if state == stale || state.agentSession != AgentSession(fresh) {
		t.Fatal("stale agent was reused instead of recycled")
	}
	if old.Alive() || !stale.isStopped() {
		t.Fatal("stale agent was left running")
	}
	if agent.oldAliveAtNew.Load() {
		t.Fatal("replacement started while the stale agent was still running")
	}
	e.interactiveMu.Lock()
	published := e.interactiveStates[key]
	e.interactiveMu.Unlock()
	if published != state {
		t.Fatal("replacement agent was not published")
	}
	if got := session.GetAgentSessionID(); got != "fresh-id" {
		t.Fatalf("session bound to %q, want fresh-id", got)
	}
}
