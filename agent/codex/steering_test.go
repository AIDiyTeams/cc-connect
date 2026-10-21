package codex

import (
	"encoding/json"
	"testing"
)

func TestSteerExactTurnAndReceipt(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	done := make(chan string, 1)
	go func() { done <- s.SteerTurn("turn-1", "client-1", "extra context") }()
	req := waitForWrittenMethod(t, stdin, "turn/steer")
	if req.Params["expectedTurnId"] != "turn-1" || req.Params["clientUserMessageId"] != "client-1" {
		t.Fatalf("params: %#v", req.Params)
	}
	s.handleResponse(rpcResponseEnvelope{ID: float64(req.ID), Result: json.RawMessage(`{"turnId":"turn-1"}`)})
	if <-done != "ACCEPTED" {
		t.Fatal("receipt missing")
	}
	if s.SteerTurn("ended-turn", "client-2", "late") != "REJECTED" {
		t.Fatal("stale turn must not receive input")
	}
}
