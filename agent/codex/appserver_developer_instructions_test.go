package codex

import (
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestDeveloperPolicyUpdatesAndRevocationPreserveTheNativeThread(t *testing.T) {
	s := &appServerSession{model: "current-model", effort: "high"}
	s.alive.Store(true)
	s.threadID.Store("existing-conversation")
	other := &appServerSession{model: "another-model"}
	defer func() { removeTaskRuntimeEnv(s.currentTaskRuntimeEnvFile()) }()
	for _, policy := range []string{"First policy", "Second policy", ""} {
		if err := s.SetSessionRuntime(core.SessionRuntime{DeveloperInstructions: policy}); err != nil {
			t.Fatal(err)
		}
		mode := s.turnDeveloperInstructions()
		settings := mode["settings"].(map[string]any)
		if s.CurrentSessionID() != "existing-conversation" || settings["model"] != "current-model" || settings["reasoning_effort"] != "high" {
			t.Fatal("policy change reset the conversation or changed model settings")
		}
		if policy == "" {
			if settings["developer_instructions"] != nil {
				t.Fatal("revocation must restore the native preset with null, not empty or stale instructions")
			}
		} else if !strings.HasSuffix(settings["developer_instructions"].(string), policy) {
			t.Fatal("next turn did not receive the current policy")
		}
		if other.turnDeveloperInstructions() != nil {
			t.Fatal("policy affected another session")
		}
	}
}

func TestResumedThreadExplicitlyClearsPreviousRuntimePolicy(t *testing.T) {
	s := &appServerSession{model: "resumed-model", developerInstructionsManaged: true}
	settings := s.turnDeveloperInstructions()["settings"].(map[string]any)
	if settings["developer_instructions"] != nil || settings["model"] != "resumed-model" {
		t.Fatal("resumed thread retained stale collaboration instructions")
	}
}
