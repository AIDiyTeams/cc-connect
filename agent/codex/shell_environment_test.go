package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgentCommandsDoNotInheritTheBridgesCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for _, runtime := range []core.SessionRuntime{
		{TaskID: "cmsg-a", WorkspaceID: "ws-a", BrandID: "b-a", ChatSessionID: "csess-a"},
		{TaskID: "llm-a", WorkspaceID: "ws-a", BrandID: "b-a"},
		{},
	} {
		s := &appServerSession{ctx: ctx, cancel: cancel, workDir: t.TempDir(), runtime: runtime}
		config := s.threadRequestParams()["config"].(map[string]any)
		excluded, _ := config["shell_environment_policy.exclude"].([]string)
		for _, secret := range []string{"DEEPSEEK_API_KEY", webSearchConfigEnv} {
			found := false
			for _, name := range excluded {
				found = found || name == secret
			}
			if !found {
				t.Fatalf("runtime %+v: %s not in excluded=%v", runtime, secret, excluded)
			}
		}
	}
}
