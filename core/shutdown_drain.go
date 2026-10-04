package core

import (
	"context"
	"log/slog"
	"time"
)

// TurnInterruptedByShutdown is the typed failure code a machine adapter receives
// for a turn the bridge had to cut while shutting down.
const TurnInterruptedByShutdown = "BRIDGE_SHUTDOWN"

type activeTurn struct {
	platform Platform
	replyCtx any
}

// trackActiveTurn records a streaming turn until the returned func is called.
// Nested (queued) turns register their own entry, so each reply context is
// reported exactly once.
func (e *Engine) trackActiveTurn(p Platform, replyCtx any) func() {
	if p == nil || replyCtx == nil {
		return func() {}
	}
	turn := &activeTurn{platform: p, replyCtx: replyCtx}
	e.activeTurnsMu.Lock()
	if e.activeTurns == nil {
		e.activeTurns = make(map[*activeTurn]struct{})
	}
	e.activeTurns[turn] = struct{}{}
	e.activeTurnsMu.Unlock()
	return func() {
		e.activeTurnsMu.Lock()
		delete(e.activeTurns, turn)
		e.activeTurnsMu.Unlock()
	}
}

// pendingTurns returns the streaming turns plus messages still queued behind
// them. Both are lost when the agent processes exit.
func (e *Engine) pendingTurns() []activeTurn {
	var turns []activeTurn
	e.activeTurnsMu.Lock()
	for turn := range e.activeTurns {
		turns = append(turns, *turn)
	}
	e.activeTurnsMu.Unlock()

	e.interactiveMu.Lock()
	states := make([]*interactiveState, 0, len(e.interactiveStates))
	for _, state := range e.interactiveStates {
		states = append(states, state)
	}
	e.interactiveMu.Unlock()
	for _, state := range states {
		state.mu.Lock()
		for _, queued := range state.pendingMessages {
			if queued.platform != nil && queued.replyCtx != nil {
				turns = append(turns, activeTurn{platform: queued.platform, replyCtx: queued.replyCtx})
			}
		}
		state.mu.Unlock()
	}
	return turns
}

// PendingTurnCount reports how many turns a shutdown would cut right now.
func (e *Engine) PendingTurnCount() int {
	return len(e.pendingTurns())
}

// drainableTurnCount is what a shutdown can usefully wait for. A turn paused on
// the user cannot finish without a person, so neither it nor the messages queued
// behind it hold the restart; they are still reported when the bridge stops, and
// the control plane keeps such a question answerable across the restart.
func (e *Engine) drainableTurnCount() int {
	total := len(e.pendingTurns())
	e.interactiveMu.Lock()
	states := make([]*interactiveState, 0, len(e.interactiveStates))
	for _, state := range e.interactiveStates {
		states = append(states, state)
	}
	e.interactiveMu.Unlock()
	for _, state := range states {
		state.mu.Lock()
		if state.pending != nil {
			total--
			for _, queued := range state.pendingMessages {
				if queued.platform != nil && queued.replyCtx != nil {
					total--
				}
			}
		}
		state.mu.Unlock()
	}
	if total < 0 {
		return 0
	}
	return total
}

// InterruptPendingTurns reports every turn a shutdown is about to cut, so the
// adapter can settle it at once instead of waiting for it to go silent.
// It must run while the platforms are still connected.
func (e *Engine) InterruptPendingTurns(message string) int {
	turns := e.pendingTurns()
	for _, turn := range turns {
		e.failTurn(turn.platform, turn.replyCtx, TurnInterruptedByShutdown, message)
	}
	return len(turns)
}

// DrainEngines waits until no engine has a pending turn or the timeout passes,
// then reports the turns that are still pending. Engines keep accepting turns
// while draining; a turn that cannot finish in time is reported like the rest.
func DrainEngines(ctx context.Context, engines []*Engine, timeout time.Duration, poll time.Duration) int {
	if timeout <= 0 {
		return 0
	}
	if poll <= 0 {
		poll = time.Second
	}
	count := func() int {
		total := 0
		for _, e := range engines {
			total += e.drainableTurnCount()
		}
		return total
	}
	deadline := time.Now().Add(timeout)
	if n := count(); n > 0 {
		slog.Info("shutdown: waiting for active turns", "turns", n, "timeout", timeout)
	}
	for count() > 0 && time.Now().Before(deadline) {
		wait := time.Until(deadline)
		if poll < wait {
			wait = poll
		}
		select {
		case <-ctx.Done():
			deadline = time.Now()
		case <-time.After(wait):
		}
	}
	interrupted := 0
	for _, e := range engines {
		interrupted += e.InterruptPendingTurns(e.i18n.T(MsgTurnInterruptedByShutdown))
	}
	if interrupted > 0 {
		slog.Warn("shutdown: interrupted turns that did not finish in time", "turns", interrupted)
	}
	return interrupted
}
