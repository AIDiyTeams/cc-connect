package core

// TurnSteerer appends text to the exact running turn. No new turn is started.
// An empty target is a read-only snapshot. Status is ACCEPTED, REJECTED or UNKNOWN.
type TurnSteerer interface {
	SteeringTurnID() string
	SteerTurn(turnID, clientID, text string) string
}
