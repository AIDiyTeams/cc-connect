package codex

import "testing"

// The gateway's flex variant of a reasoning model is the same model at a cheaper tier.
func TestFlexVariantKeepsTheReasoningCapability(t *testing.T) {
	if !needsReasoningCapability("tomako/gpt-6.1-sol-flex", "medium") || !needsReasoningCapability("tomako/gpt-6.1-sol", "medium") {
		t.Fatal("flex variant lost reasoning capability")
	}
	if needsReasoningCapability("tomako/unknown-flex", "medium") || needsReasoningCapability("tomako/gpt-6.1-sol-flex", "") {
		t.Fatal("only known reasoning models with an effort qualify")
	}
}
