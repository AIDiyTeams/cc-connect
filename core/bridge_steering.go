package core

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
)

type bridgeSteeringRequest struct {
	RequestID  string `json:"request_id"`
	InstanceID string `json:"instance_id"`
	SessionKey string `json:"session_key"`
	ReplyCtx   string `json:"reply_ctx"`
	TurnNo     int    `json:"turn_no"`
	Content    string `json:"content"`
}
type bridgeSteeringReceipt struct {
	request bridgeSteeringRequest
	status  string
}
type bridgeSteeringLedger struct {
	mu       sync.Mutex
	receipts map[string]bridgeSteeringReceipt
}

// The ledger belongs to the process, not a websocket. A new process must never replay
// a request addressed to its predecessor: that predecessor may have accepted it.
func (bs *BridgeServer) steer(r bridgeSteeringRequest) string {
	if r.InstanceID != bs.instanceID {
		return "UNKNOWN"
	}
	if r.RequestID == "" || r.SessionKey == "" || r.ReplyCtx == "" || strings.TrimSpace(r.Content) == "" || len(r.Content) > 100000 {
		return "REJECTED"
	}
	bs.steering.mu.Lock()

	if old, ok := bs.steering.receipts[r.RequestID]; ok {
		bs.steering.mu.Unlock()
		if old.request != r {
			return "REJECTED"
		}
		return old.status
	}
	// Never evict a deduplication receipt while this process can accept retries.
	if len(bs.steering.receipts) >= 10000 {
		bs.steering.mu.Unlock()
		return "REJECTED"
	}
	if bs.steering.receipts == nil {
		bs.steering.receipts = make(map[string]bridgeSteeringReceipt)
	}
	bs.steering.receipts[r.RequestID] = bridgeSteeringReceipt{r, "PENDING"}
	bs.steering.mu.Unlock()
	status := "REJECTED"
	bs.enginesMu.RLock()
	refs := make([]*bridgeEngineRef, 0, len(bs.engines))
	for _, ref := range bs.engines {
		refs = append(refs, ref)
	}
	bs.enginesMu.RUnlock()
	for _, ref := range refs {
		if agent, turn := ref.engine.steeringTarget(r); agent != nil {
			status = agent.SteerTurn(turn, r.RequestID, r.Content)
			break
		}
	}
	bs.steering.mu.Lock()
	bs.steering.receipts[r.RequestID] = bridgeSteeringReceipt{r, status}
	bs.steering.mu.Unlock()
	return status
}

func (e *Engine) steeringTarget(r bridgeSteeringRequest) (TurnSteerer, string) {
	e.interactiveMu.Lock()
	states := make([]*interactiveState, 0, len(e.interactiveStates))
	for _, state := range e.interactiveStates {
		states = append(states, state)
	}
	e.interactiveMu.Unlock()
	for _, state := range states {
		state.mu.Lock()
		rc, ok := state.replyCtx.(*bridgeReplyCtx)
		agent, supported := state.agentSession.(TurnSteerer)
		if ok && supported && !state.stopped && rc.SessionKey == r.SessionKey && rc.ReplyCtx == r.ReplyCtx && (r.TurnNo == 0 || rc.TurnNo == r.TurnNo) {
			turn := agent.SteeringTurnID()
			state.mu.Unlock()
			if turn != "" {
				return agent, turn
			}
		} else {
			state.mu.Unlock()
		}
	}
	return nil, ""
}

func (a *bridgeAdapter) handleSteer(raw []byte) {
	var r bridgeSteeringRequest
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	status := a.server.steer(r)
	if err := writeJSON(a.conn, &a.writeMu, map[string]any{"type": "steer_receipt", "request_id": r.RequestID, "status": status}); err != nil {
		slog.Debug("steering receipt delivery failed; retained for reconnect", "request_id", r.RequestID, "error", err)
		return
	}
}
