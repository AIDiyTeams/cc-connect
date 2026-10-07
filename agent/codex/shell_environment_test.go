package codex

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAgentCommandsDoNotInheritTheBridgesProviderKey(t *testing.T) {
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
		found := false
		for _, name := range excluded {
			found = found || name == "DEEPSEEK_API_KEY"
		}
		if !found {
			t.Fatalf("runtime %+v: excluded=%v", runtime, excluded)
		}
	}
}
