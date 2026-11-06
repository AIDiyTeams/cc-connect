package core

import "testing"

func TestBridgePreservesEverySelectableTurnReasoningEffort(t *testing.T) {
	// Run at the bridge boundary, before the agent's runtime setter. Dropping a
	// supported value here silently leaves a reused session at its previous level.
	for _, effort := range []string{"low", "medium", "high", "xhigh", "max", "ultra"} {
		t.Run(effort, func(t *testing.T) {
			runtime := normalizeSessionRuntime(SessionRuntime{GatewayModel: "openrouter/openai/gpt-6.1-sol", ReasoningEffort: effort})
			if runtime.ReasoningEffort != effort {
				t.Fatalf("bridge dropped selected effort %q: got %q", effort, runtime.ReasoningEffort)
			}
		})
	}
	for _, invalid := range []string{"unknown", "minimal", "high;command"} {
		if got := normalizeSessionRuntime(SessionRuntime{ReasoningEffort: invalid}).ReasoningEffort; got != "" {
			t.Fatalf("invalid effort %q accepted as %q", invalid, got)
		}
	}
}
