package core

import (
	"errors"
	"testing"
	"time"
)

func pausedQuestionState(session AgentSession) (*interactiveState, *pendingPermission) {
	pending := &pendingPermission{
		RequestID:       "form-call-paused",
		InteractionID:   "interaction-paused",
		InteractionKind: InteractionKindQuestion,
		ToolName:        "AskUserQuestion",
		ToolInput:       map[string]any{"form": map[string]any{"title": "Facts"}},
		Questions:       []UserQuestion{{ID: "audience", Question: "Audience", InputType: "text"}},
		Resolved:        make(chan struct{}),
	}
	return &interactiveState{agentSession: session, pending: pending}, pending
}

func TestAnUnansweredStructuredQuestionReleasesItsTurn(t *testing.T) {
	e := newTestEngine()
	e.SetPausedTurnRelease(20 * time.Millisecond)
	session := &recordingAgentSession{}
	state, pending := pausedQuestionState(session)
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()

	if !e.awaitAnswer(state, pending, true) {
		t.Fatal("an unanswered question must release its turn after the limit")
	}
	if state.pending != nil {
		t.Fatal("a released turn must no longer accept the answer in place")
	}
	err := e.RespondInteraction("test:chat:user1", "interaction-paused", "", map[string][]string{"audience": {"Developers"}})
	if !errors.Is(err, ErrInteractionNotPending) {
		t.Fatalf("answer after release = %v, want ErrInteractionNotPending so the control plane continues", err)
	}
	if session.calls != 0 {
		t.Fatal("a released turn must not receive the answer")
	}
}

func TestAnAnswerThatArrivesFirstKeepsTheTurn(t *testing.T) {
	e := newTestEngine()
	e.SetPausedTurnRelease(30 * time.Millisecond)
	session := &recordingAgentSession{}
	state, pending := pausedQuestionState(session)
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()

	if err := e.RespondInteraction("test:chat:user1", "interaction-paused", "", map[string][]string{"audience": {"Developers"}}); err != nil {
		t.Fatalf("RespondInteraction() error = %v", err)
	}
	if e.awaitAnswer(state, pending, true) {
		t.Fatal("an answered turn must resume, not be released")
	}
	if session.calls != 1 {
		t.Fatalf("answer deliveries = %d, want 1", session.calls)
	}
}

func TestAClaimedAnswerWinsARaceWithTheRelease(t *testing.T) {
	e := newTestEngine()
	e.SetPausedTurnRelease(10 * time.Millisecond)
	state, pending := pausedQuestionState(&recordingAgentSession{})
	pending.claim() // the answer is being delivered when the limit passes
	go func() {
		time.Sleep(40 * time.Millisecond)
		pending.resolve()
	}()
	if e.awaitAnswer(state, pending, true) {
		t.Fatal("a turn whose answer is in flight must not be released")
	}
}

func TestOnlyStructuredQuestionsAreReleased(t *testing.T) {
	e := newTestEngine()
	e.SetPausedTurnRelease(10 * time.Millisecond)
	state, pending := pausedQuestionState(&recordingAgentSession{})
	go func() {
		time.Sleep(60 * time.Millisecond)
		pending.resolve()
	}()
	start := time.Now()
	if e.awaitAnswer(state, pending, false) {
		t.Fatal("a chat-platform prompt has no control plane to continue it and must keep waiting")
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("a non-structured prompt must wait for its answer")
	}
	e.SetPausedTurnRelease(0)
	state, pending = pausedQuestionState(&recordingAgentSession{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		pending.resolve()
	}()
	if e.awaitAnswer(state, pending, true) {
		t.Fatal("a zero release limit keeps the turn until answered")
	}
}

func TestBridge_UnansweredQuestionReleasesTheTurnAndReportsIt(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")
	bp := bs.NewPlatform("release-proj")
	sess := newBridgeInteractionAgentSession("codex-release")
	e := NewEngine("release-proj", &controllableAgent{nextSession: sess}, []Platform{bp}, "", LangEnglish)
	e.SetPausedTurnRelease(50 * time.Millisecond)
	bs.RegisterEngine("release-proj", e, bp)

	conn := dialWS(t, wsURL, nil)
	register(t, conn, "bridge", []string{"text", "interactions"})
	mustWriteJSON(t, conn, map[string]any{
		"type":        "message",
		"msg_id":      "m-release-1",
		"session_key": "bridge:room-release:user-1",
		"user_id":     "user-1",
		"content":     "Ask me something",
		"reply_ctx":   "cmsg-release-1",
	})
	select {
	case <-sess.sendStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("agent prompt did not start")
	}
	close(sess.unblock)
	sess.events <- Event{
		Type:         EventPermissionRequest,
		RequestID:    `"rui-release-1"`,
		ToolName:     "AskUserQuestion",
		ToolInput:    `{"questions":[{"id":"audience","question":"Who is it for?"}]}`,
		ToolInputRaw: map[string]any{"questions": []any{map[string]any{"id": "audience", "question": "Who is it for?"}}},
		Questions:    []UserQuestion{{ID: "audience", Question: "Who is it for?"}},
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("the released turn was not reported to the control plane")
		default:
		}
		msg := readMsg(t, conn)
		if msg["type"] == "error" {
			if msg["code"] != TurnPausedTurnReleased || msg["reply_ctx"] != "cmsg-release-1" {
				t.Fatalf("release report = %v", msg)
			}
			break
		}
	}
	select {
	case <-sess.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the released turn must close its Agent session")
	}
}
