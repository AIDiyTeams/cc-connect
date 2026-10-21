package codex

import (
	"encoding/json"
	"time"
)

func (s *appServerSession) SteeringTurnID() string {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if !s.alive.Load() {
		return ""
	}
	return s.currentTurn
}

// Keep explicit protocol rejections separate from ambiguous transport failures.
func (s *appServerSession) SteerTurn(turnID, clientID, text string) string {
	if turnID == "" || s.SteeringTurnID() != turnID {
		return "REJECTED"
	}
	id := s.nextID.Add(1)
	ch := make(chan rpcResponseEnvelope, 1)
	s.pendingMu.Lock()
	s.pending[id] = ch
	s.pendingMu.Unlock()
	defer func() { s.pendingMu.Lock(); delete(s.pending, id); s.pendingMu.Unlock() }()
	if err := s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": id, "method": "turn/steer", "params": map[string]any{
		"threadId": s.CurrentSessionID(), "expectedTurnId": turnID, "clientUserMessageId": clientID,
		"input": []map[string]any{{"type": "text", "text": text, "text_elements": []any{}}},
	}}); err != nil {
		return "UNKNOWN"
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return "REJECTED"
		}
		var result struct {
			TurnID string `json:"turnId"`
		}
		if json.Unmarshal(resp.Result, &result) != nil || result.TurnID != turnID {
			return "UNKNOWN"
		}
		return "ACCEPTED"
	case <-s.ctx.Done():
		return "UNKNOWN"
	case <-time.After(8 * time.Second):
		return "UNKNOWN"
	}
}
