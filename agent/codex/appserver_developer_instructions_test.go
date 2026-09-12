package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPreparedTemporaryDirectoryIsVisibleOnlyToItsManagedFencedSession(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env, err := prepareFencedEnvironment(workDir, []string{"TMPDIR=/host-temp", "SECRET=must-not-be-exposed"})
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(workDir, ".tmp")
	for _, tc := range []struct {
		name, profile, dir, policy string
		env                        []string
		want                       bool
	}{
		{"prepared", "fence", workDir, "Current application policy", env, true},
		{"unfenced", "", workDir, "Current application policy", env, false},
		{"not prepared", "fence", workDir, "Current application policy", nil, false},
		{"resumed elsewhere", "fence", workDir + "-other", "Current application policy", env, false},
		{"overridden environment", "fence", workDir, "Current application policy", append(append([]string(nil), env...), "TMPDIR=/elsewhere"), false},
		{"policy revoked", "fence", workDir, "", env, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &appServerSession{workDir: tc.dir, model: "unchanged-model", effort: "high",
				permissionsProfile: tc.profile, extraEnv: tc.env, developerInstructionsManaged: true,
				runtime: core.SessionRuntime{DeveloperInstructions: tc.policy}}
			s.threadID.Store("existing-thread")
			for range 2 {
				settings := s.turnDeveloperInstructions()["settings"].(map[string]any)
				text, _ := settings["developer_instructions"].(string)
				if strings.Contains(text, tmpDir) != tc.want {
					t.Fatalf("temporary directory visibility = %v, want %v", text, tc.want)
				}
				if strings.Contains(text, "must-not-be-exposed") || strings.Count(text, tmpDir) > 1 {
					t.Fatal("environment secrets leaked or facts accumulated across turns")
				}
				if tc.policy == "" && settings["developer_instructions"] != nil {
					t.Fatal("revocation must still clear the developer policy")
				}
				if tc.policy != "" && !strings.HasSuffix(text, tc.policy) {
					t.Fatal("current application policy must remain intact and last")
				}
				if settings["model"] != "unchanged-model" || settings["reasoning_effort"] != "high" || s.CurrentSessionID() != "existing-thread" {
					t.Fatal("environment facts changed the model, effort or thread")
				}
			}
		})
	}
}

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
