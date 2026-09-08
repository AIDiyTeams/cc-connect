package core

import (
	"context"
	"testing"
)

func TestBridgeChatPublicActivityExcludesRawTrace(t *testing.T) {
	bs, wsURL := startTestBridge(t, "")
	conn := dialWS(t, wsURL, nil)
	register(t, conn, "java-backend", []string{"text", "agent_trace"})
	rc := newBridgeReplyCtx(bs.getAdapter("java-backend"), "session", "cmsg-activity")
	rc.TurnNo = 2
	err := bs.NewPlatform("proj").ReportAgentTrace(context.Background(), rc, AgentTraceEvent{
		Type: EventToolUse, TraceID: "web-1", ToolName: "internal", Input: "secret-input", Output: "secret-output",
		PublicActivity: &PublicActivity{Kind: "open_page", Status: "running", URL: "https://example.com/article"},
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := readMsg(t, conn)
	if frame["trace_id"] != "web-1" || frame["turn_no"] != float64(2) || frame["public_activity"] == nil {
		t.Fatalf("missing identity or public activity: %#v", frame)
	}
	for _, key := range []string{"input", "output", "tool_name"} {
		if _, ok := frame[key]; ok {
			t.Fatalf("private field leaked: %s", key)
		}
	}
}
