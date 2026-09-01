package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestUpdateTaskRuntimeEnvWritesProtectedReusableFile(t *testing.T) {
	runtime := core.SessionRuntime{
		TaskID:                   "llm-task-1",
		MachineCapabilityToken:   "machine-token",
		TaskAuthorityEnvelopeB64: "authority-envelope",
	}
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil {
		t.Fatalf("updateTaskRuntimeEnv: %v", err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(path) })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat task runtime file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("task runtime mode = %o, want 600", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read task runtime file: %v", err)
	}
	text := string(body)
	for _, expected := range []string{
		"export MACHINE_CAPABILITY_TOKEN='machine-token'",
		"export IMAGE_CAPABILITY_TOKEN='machine-token'",
		"export TOMAKO_TASK_AUTHORITY_ENVELOPE_B64='authority-envelope'",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("task runtime file missing %q: %q", expected, text)
		}
	}

	updated := runtime
	updated.MachineCapabilityToken = "rotated-token"
	updatedPath, err := updateTaskRuntimeEnv(path, updated)
	if err != nil {
		t.Fatalf("rotate task runtime env: %v", err)
	}
	if updatedPath != path {
		t.Fatalf("rotated path = %q, want stable %q", updatedPath, path)
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rotated task runtime file: %v", err)
	}
	if strings.Contains(string(body), "machine-token") ||
		!strings.Contains(string(body), "rotated-token") {
		t.Fatalf("task runtime file was not atomically rotated: %q", body)
	}
	if filepath.Base(path) != "machine.env" {
		t.Fatalf("task runtime basename = %q", filepath.Base(path))
	}
}

func TestUpdateTaskRuntimeEnvRejectsIncompleteAuthority(t *testing.T) {
	_, err := updateTaskRuntimeEnv("", core.SessionRuntime{
		TaskID:                 "llm-task-1",
		MachineCapabilityToken: "machine-token",
	})
	if err == nil || !strings.Contains(err.Error(), "must be supplied together") {
		t.Fatalf("incomplete authority error = %v", err)
	}
}

func TestThreadParamsExposeOnlyTaskRuntimeFilePath(t *testing.T) {
	s := &appServerSession{
		workDir:            "/srv/tomako",
		taskRuntimeEnvFile: "/tmp/cc-connect-task-runtime-safe/machine.env",
	}
	params := s.threadRequestParams()
	config := params["config"].(map[string]any)
	if got := config["shell_environment_policy.set.TOMAKO_TASK_ENV_FILE"]; got != s.taskRuntimeEnvFile {
		t.Fatalf("task runtime env config = %#v", config)
	}
	for key, value := range config {
		if strings.Contains(key, "CAPABILITY") || strings.Contains(strings.TrimSpace(strings.ReplaceAll(key, "TOMAKO_TASK_ENV_FILE", "")), "AUTHORITY") {
			t.Fatalf("secret unexpectedly placed in app-server config: %s=%v", key, value)
		}
	}
}

func TestSessionRuntimeRebindsResumedThreadWithTaskEnvFile(t *testing.T) {
	s := &appServerSession{}
	s.alive.Store(true)
	s.threadID.Store("thread-existing")
	if err := s.SetSessionRuntime(core.SessionRuntime{
		TaskID:                   "llm-task-1",
		MachineCapabilityToken:   "machine-token",
		TaskAuthorityEnvelopeB64: "authority-envelope",
	}); err != nil {
		t.Fatalf("SetSessionRuntime: %v", err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(s.currentTaskRuntimeEnvFile()) })
	if got := s.CurrentSessionID(); got != "" {
		t.Fatalf("current thread id = %q, want cleared for configured resume", got)
	}
	if s.resumeID != "thread-existing" {
		t.Fatalf("resume id = %q, want thread-existing", s.resumeID)
	}
}
