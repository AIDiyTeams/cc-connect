package codex

import (
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func pausedTurnSession(t *testing.T) *appServerSession {
	t.Helper()
	runtime := core.SessionRuntime{
		TaskID: "cmsg-1", WorkspaceID: "w", BrandID: "b", GatewayModel: "model-a",
		ImageCapabilityToken: "old-image", EmployeeCommandCapabilityToken: "old-employee",
	}
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil {
		t.Fatalf("updateTaskRuntimeEnv: %v", err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(path) })
	s := &appServerSession{model: "model-a", runtime: runtime, taskRuntimeEnvFile: path}
	s.alive.Store(true)
	s.threadID.Store("thread-1")
	return s
}

func TestRefreshCapabilityAuthorityRotatesOnlyTheCredentials(t *testing.T) {
	s := pausedTurnSession(t)
	path := s.taskRuntimeEnvFile

	if err := s.RefreshCapabilityAuthority(core.SessionRuntime{
		TaskID: "cmsg-1", WorkspaceID: "w", BrandID: "b", GatewayModel: "model-b",
		ImageCapabilityToken: "new-image", EmployeeCommandCapabilityToken: "new-employee",
	}); err != nil {
		t.Fatalf("RefreshCapabilityAuthority: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read task runtime file: %v", err)
	}
	if strings.Contains(string(body), "old-") || !strings.Contains(string(body), "new-employee") ||
		!strings.Contains(string(body), "new-image") {
		t.Fatalf("credentials were not rotated in place: %q", body)
	}
	if s.taskRuntimeEnvFile != path || s.runtime.ImageCapabilityToken != "new-image" {
		t.Fatal("tools must read the rotated credentials from the same file and runtime")
	}
	if s.runtime.GatewayModel != "model-a" || s.model != "model-a" || s.CurrentSessionID() != "thread-1" {
		t.Fatal("a credential refresh must not change the model or thread of the paused turn")
	}
}

func TestRefreshCapabilityAuthorityRefusesAnotherTasksCredentials(t *testing.T) {
	s := pausedTurnSession(t)
	for _, fresh := range []core.SessionRuntime{
		{TaskID: "cmsg-other", WorkspaceID: "w", BrandID: "b", EmployeeCommandCapabilityToken: "x"},
		{TaskID: "cmsg-1", WorkspaceID: "w", BrandID: "other", EmployeeCommandCapabilityToken: "x"},
		{WorkspaceID: "w", BrandID: "b", EmployeeCommandCapabilityToken: "x"},
	} {
		if err := s.RefreshCapabilityAuthority(fresh); err == nil {
			t.Fatalf("refresh for %#v must be refused", fresh)
		}
	}
	if s.runtime.EmployeeCommandCapabilityToken != "old-employee" {
		t.Fatal("a refused refresh must keep the current credentials")
	}
}
