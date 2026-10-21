package core

import (
	"sync/atomic"
	"testing"
)

type queueSteerAgent struct {
	recordingAgentSession
	calls   atomic.Int32
	result  string
	entered chan struct{}
	release chan struct{}
}

func (s *queueSteerAgent) SteeringTurnID() string { return "turn-1" }
func (s *queueSteerAgent) SteerTurn(turn, id, text string) string {
	s.calls.Add(1)
	if s.entered != nil {
		close(s.entered)
		<-s.release
	}
	return s.result
}
func steeringFixture(t *testing.T) (*BridgeServer, *queueSteerAgent, bridgeSteeringRequest) {
	agent := &queueSteerAgent{result: "ACCEPTED"}
	e, state := detachedEngine(t, agent)
	state.replyCtx = &bridgeReplyCtx{SessionKey: "session", ReplyCtx: "task", TurnNo: 3}
	bs := &BridgeServer{instanceID: "process-1", engines: map[string]*bridgeEngineRef{"p": {engine: e}}}
	return bs, agent, bridgeSteeringRequest{RequestID: "request-1", InstanceID: "process-1", SessionKey: "session", ReplyCtx: "task", TurnNo: 3, Content: "add detail"}
}
func TestSteeringReconnectAndRepeatedClickDeliverOnce(t *testing.T) {
	bs, agent, r := steeringFixture(t)
	if bs.steer(r) != "ACCEPTED" || bs.steer(r) != "ACCEPTED" || agent.calls.Load() != 1 {
		t.Fatal("retry duplicated or lost receipt")
	}
	// Receipt remains even after the turn and engine go away.
	bs.engines = nil
	if bs.steer(r) != "ACCEPTED" {
		t.Fatal("completed turn lost receipt")
	}
}
func TestSteeringNeverTargetsAnotherTurnOrProcess(t *testing.T) {
	for _, change := range []func(*bridgeSteeringRequest){func(r *bridgeSteeringRequest) { r.TurnNo++ }, func(r *bridgeSteeringRequest) { r.ReplyCtx = "other" }, func(r *bridgeSteeringRequest) { r.SessionKey = "other" }, func(r *bridgeSteeringRequest) { r.InstanceID = "old" }} {
		bs, agent, r := steeringFixture(t)
		change(&r)
		if bs.steer(r) == "ACCEPTED" || agent.calls.Load() != 0 {
			t.Fatal("wrong target received input")
		}
	}
}
func TestSteeringConcurrentRetryAndUnknownDoNotResend(t *testing.T) {
	bs, agent, r := steeringFixture(t)
	agent.result = "UNKNOWN"
	agent.entered = make(chan struct{})
	agent.release = make(chan struct{})
	done := make(chan string, 1)
	go func() { done <- bs.steer(r) }()
	<-agent.entered
	if bs.steer(r) != "PENDING" {
		t.Fatal("in-flight retry should wait")
	}
	close(agent.release)
	if <-done != "UNKNOWN" || bs.steer(r) != "UNKNOWN" || agent.calls.Load() != 1 {
		t.Fatal("ambiguous delivery was replayed")
	}
}
