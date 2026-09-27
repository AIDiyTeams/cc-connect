package core

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

type shutdownCapturePlatform struct {
	mu       sync.Mutex
	failures map[any]TurnFailure
}

func (p *shutdownCapturePlatform) Name() string                             { return "shutdown-capture" }
func (p *shutdownCapturePlatform) Start(MessageHandler) error               { return nil }
func (p *shutdownCapturePlatform) Stop() error                              { return nil }
func (p *shutdownCapturePlatform) Send(context.Context, any, string) error  { return nil }
func (p *shutdownCapturePlatform) Reply(context.Context, any, string) error { return nil }
func (p *shutdownCapturePlatform) ReportTurnFailure(_ context.Context, replyCtx any, failure TurnFailure) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures == nil {
		p.failures = map[any]TurnFailure{}
	}
	p.failures[replyCtx] = failure
	return nil
}

func (p *shutdownCapturePlatform) failed() map[any]TurnFailure {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[any]TurnFailure, len(p.failures))
	for k, v := range p.failures {
		out[k] = v
	}
	return out
}

func TestDrainEnginesReturnsAtOnceWhenIdle(t *testing.T) {
	p := &shutdownCapturePlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)

	start := time.Now()
	if n := DrainEngines(context.Background(), []*Engine{e}, time.Minute, 10*time.Millisecond); n != 0 {
		t.Fatalf("interrupted %d turns on an idle engine", n)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("idle drain waited %s", time.Since(start))
	}
	if len(p.failed()) != 0 {
		t.Fatalf("unexpected failures: %v", p.failed())
	}
}

func TestDrainEnginesWaitsForTurnThatFinishes(t *testing.T) {
	p := &shutdownCapturePlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	done := e.trackActiveTurn(p, "cmsg-finishing")
	go func() {
		time.Sleep(50 * time.Millisecond)
		done()
	}()

	if n := DrainEngines(context.Background(), []*Engine{e}, 5*time.Second, 10*time.Millisecond); n != 0 {
		t.Fatalf("interrupted %d turns that finished within the drain", n)
	}
	if len(p.failed()) != 0 {
		t.Fatalf("unexpected failures: %v", p.failed())
	}
}

func TestDrainEnginesReportsTurnsCutAtDeadline(t *testing.T) {
	p := &shutdownCapturePlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangChinese)
	outer := e.trackActiveTurn(p, "cmsg-streaming")
	defer outer()
	inner := e.trackActiveTurn(p, "cmsg-queued-now-streaming")
	defer inner()
	e.interactiveStates["bridge:user"] = &interactiveState{
		platform:        p,
		pendingMessages: []queuedMessage{{platform: p, replyCtx: "cmsg-queued"}},
	}

	if n := DrainEngines(context.Background(), []*Engine{e}, 30*time.Millisecond, 10*time.Millisecond); n != 3 {
		t.Fatalf("interrupted %d turns, want 3", n)
	}
	failures := p.failed()
	for _, ctx := range []string{"cmsg-streaming", "cmsg-queued-now-streaming", "cmsg-queued"} {
		f, ok := failures[ctx]
		if !ok {
			t.Fatalf("turn %s was not reported: %v", ctx, failures)
		}
		if f.Code != TurnInterruptedByShutdown || f.Message == "" {
			t.Fatalf("turn %s reported %+v", ctx, f)
		}
	}
}

func TestDrainEnginesStopsWaitingWhenCancelled(t *testing.T) {
	p := &shutdownCapturePlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	done := e.trackActiveTurn(p, "cmsg-stuck")
	defer done()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if n := DrainEngines(ctx, []*Engine{e}, time.Minute, 10*time.Millisecond); n != 1 {
		t.Fatalf("interrupted %d turns, want 1", n)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cancelled drain waited %s", time.Since(start))
	}
}

func TestDrainEnginesDisabledByZeroTimeout(t *testing.T) {
	p := &shutdownCapturePlatform{}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	done := e.trackActiveTurn(p, "cmsg-running")
	defer done()

	if n := DrainEngines(context.Background(), []*Engine{e}, 0, 0); n != 0 {
		t.Fatalf("disabled drain interrupted %d turns", n)
	}
}

func TestBridgeRegisterAckCarriesStableInstanceID(t *testing.T) {
	bs, wsURL := startTestBridge(t, "tok")
	headers := http.Header{"Authorization": []string{"Bearer tok"}}
	readAck := func() map[string]any {
		conn := dialWS(t, wsURL, headers)
		mustWriteJSON(t, conn, map[string]any{"type": "register", "platform": "instance-probe", "capabilities": []string{"text"}})
		var ack map[string]any
		mustReadJSON(t, conn, &ack)
		conn.Close()
		return ack
	}
	first, second := readAck(), readAck()
	if first["instance_id"] == "" || first["instance_id"] != bs.InstanceID() {
		t.Fatalf("register_ack instance_id = %v, server = %s", first["instance_id"], bs.InstanceID())
	}
	if second["instance_id"] != first["instance_id"] {
		t.Fatalf("instance_id changed across reconnects to the same process: %v -> %v", first["instance_id"], second["instance_id"])
	}
	if other := newBridgeInstanceID(); other == bs.InstanceID() {
		t.Fatal("instance ids should differ between processes")
	}
}
