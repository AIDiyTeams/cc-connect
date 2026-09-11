package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBridgeFinalStreamSurvivesBackendReconnect(t *testing.T) {
	for _, preview := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-first-preview", true: "degraded-preview"}[preview], func(t *testing.T) {
			bs, url := startTestBridge(t, "")
			old := dialWS(t, url, nil)
			register(t, old, "test-backend", []string{"text", "preview", "token_stream"})
			rc := newBridgeReplyCtx(bs.getAdapter("test-backend"), "test-backend:own-task", "llm-reconnect")
			rc.SetUsage(123, 45)
			rc.ObserveResponseSource("native_final")
			rc.ObserveFinalResponseItem("answer-1")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			sp := newStreamPreview(DefaultStreamPreviewCfg(), bs.NewPlatform("project"), rc, ctx, nil)
			if preview {
				sp.previewMsgID = cloneBridgeReplyCtx(rc)
				sp.degraded = true
			}
			_ = old.Close()
			deadline := time.Now().Add(time.Second)
			for bs.getAdapter("test-backend") != nil && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if bs.getAdapter("test-backend") != nil {
				t.Fatal("adapter did not disconnect")
			}
			body := `{"result":"` + strings.Repeat("complete evidence ", 2000) + `"}`
			done := make(chan bool, 1)
			go func() { done <- sp.finish(body, "[ctx: ~6%] · deepseek · high") }()
			select {
			case <-done:
				t.Fatal("offline final was silently discarded")
			case <-time.After(30 * time.Millisecond):
			}
			other := dialWS(t, url, nil)
			register(t, other, "different-backend", []string{"text", "preview", "token_stream"})
			select {
			case <-done:
				t.Fatal("final answer crossed the adapter boundary")
			case <-time.After(30 * time.Millisecond):
			}
			conn := dialWS(t, url, nil)
			register(t, conn, "test-backend", []string{"text", "preview", "token_stream"})
			msg := readMsg(t, conn)
			if msg["type"] != "reply_stream" || msg["done"] != true || msg["full_text"] != body || msg["reply_ctx"] != "llm-reconnect" {
				t.Fatalf("incomplete final frame: type=%v done=%v", msg["type"], msg["done"])
			}
			if msg["response_source"] != "native_final" || msg["status"] == nil || msg["usage"] == nil || msg["finalized_item_ids"] == nil {
				t.Fatal("colleague metadata or usage lost")
			}
			select {
			case ok := <-done:
				if !ok {
					t.Fatal("successful final fell back to split replies")
				}
			case <-ctx.Done():
				t.Fatal("final did not finish")
			}
		})
	}
}

func TestBridgeFinalStreamCancellationAndLegacy(t *testing.T) {
	bs, _ := startTestBridge(t, "")
	bp := bs.NewPlatform("project")
	rc := &bridgeReplyCtx{Platform: "offline", ReplyCtx: "llm-cancel", tokenStream: true}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := bp.FinishStream(ctx, rc, "answer", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline ignored: %v", err)
	}
	if err := bp.FinishStream(context.Background(), &bridgeReplyCtx{}, "answer", ""); err != ErrNotSupported {
		t.Fatalf("legacy behavior changed: %v", err)
	}
}
