package codex

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestReasoningCapabilityIsBoundToKnownModelAndRequestedEffort(t *testing.T) {
	for _, tc := range []struct {
		model, effort string
		want          bool
	}{
		{"tomako/gpt-5.6-sol", "high", true},
		{"gpt-5.6-sol", "medium", true},
		{"tomako/gpt-5.6-sol", "", false},
		{"tomako/deepseek-v4-flash", "high", true},
		{"deepseek-v4-flash", "medium", true},
		{"tomako/deepseek-v4-flash", "", false},
		{"tomako/deepseek-v4-flash-vision-exp", "high", true},
		{"deepseek-v4-flash-vision-exp", "high", true},
		{"tomako/deepseek-v4-flash-vision-exp", "", false},
		{"other/deepseek-v4-flash-vision-exp", "high", false},
		{"other/deepseek-v4-flash", "high", false},
		{"other/gpt-5.6-sol", "high", false},
		{"unknown", "high", false},
	} {
		t.Run(tc.model+"/"+tc.effort, func(t *testing.T) {
			cs := &codexSession{model: tc.model, effort: tc.effort, mode: "suggest"}
			got := containsSequence(cs.buildExecArgs("public prompt", nil), []string{"-c", "model_supports_reasoning_summaries=true"})
			if got != tc.want {
				t.Fatalf("CLI reasoning capability = %v, want %v", got, tc.want)
			}
			s := &appServerSession{model: tc.model, effort: tc.effort}
			config := s.threadRequestParams()["config"].(map[string]any)
			got = config["model_supports_reasoning_summaries"] == true
			if got != tc.want {
				t.Fatalf("thread reasoning capability = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppServerReasoningCapabilityChangesDoNotReuseThreadOverrides(t *testing.T) {
	s := &appServerSession{model: "tomako/unknown-model", effort: "medium"}
	s.alive.Store(true)
	s.threadID.Store("previous-default-thread")
	if err := s.SetSessionRuntime(core.SessionRuntime{GatewayModel: "tomako/gpt-5.6-sol", ReasoningEffort: "high"}); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSessionID() != "" || s.threadRequestParams()["config"].(map[string]any)["model_supports_reasoning_summaries"] != true {
		t.Fatal("known model did not acquire its reasoning capability on a new thread")
	}
	s.threadID.Store("reasoning-thread")
	if err := s.SetSessionRuntime(core.SessionRuntime{GatewayModel: "tomako/unknown-model", ReasoningEffort: "medium"}); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSessionID() != "" {
		t.Fatal("previous model's thread override survived a model change")
	}
	if _, exists := s.threadRequestParams()["config"].(map[string]any)["model_supports_reasoning_summaries"]; exists {
		t.Fatal("unknown model must use its own native/configured capabilities")
	}
}
