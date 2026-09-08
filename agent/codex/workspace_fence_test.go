package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFencedSessionStopsBeforeLaunchWhenGlobalPermissionsCannotBeRead(t *testing.T) {
	globalHome := t.TempDir()
	t.Setenv("CODEX_HOME", globalHome)
	if err := os.Mkdir(filepath.Join(globalHome, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{workDir: t.TempDir(), backend: "app_server", appServerURL: "ws://127.0.0.1:1",
		permissionsProfile: "tomako-brand-fence"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // The broken implementation must not start a real CLI during this test.
	session, err := agent.StartSession(ctx, "")
	if err == nil && session != nil {
		_ = session.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "read global permission config") {
		t.Fatalf("must stop at permission inheritance, got %v", err)
	}
}

func TestNewRejectsPermissionsProfileOnExecBackend(t *testing.T) {
	_, err := New(map[string]any{
		"backend":             "exec",
		"permissions_profile": "tomako-brand-fence",
	})
	if err == nil || err.Error() != "codex: permissions_profile requires backend=app_server" {
		t.Fatalf("New() error = %v", err)
	}
}

func TestFencedAgentLocksModeAndPropagatesWorkspaceOptions(t *testing.T) {
	agent := &Agent{
		workDir:            t.TempDir(),
		backend:            "app_server",
		appServerURL:       "stdio://",
		mode:               "fenced",
		permissionsProfile: "tomako-brand-fence",
	}

	agent.SetMode("yolo")
	if got := agent.GetMode(); got != "fenced" {
		t.Fatalf("GetMode() = %q, want fenced", got)
	}
	modes := agent.PermissionModes()
	if len(modes) != 1 || modes[0].Key != "fenced" {
		t.Fatalf("PermissionModes() = %#v", modes)
	}
	opts := agent.WorkspaceAgentOptions()
	if opts["permissions_profile"] != "tomako-brand-fence" {
		t.Fatalf("workspace permissions profile = %#v", opts["permissions_profile"])
	}
}

func TestFencedAgentCreatesWorkspaceLocalTempDir(t *testing.T) {
	workDir := t.TempDir()
	env, err := prepareFencedEnvironment(workDir, []string{"LANG=C"})
	if err != nil {
		t.Fatalf("prepareFencedEnvironment() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(workDir, ".tmp"))
	if err != nil {
		t.Fatalf("workspace temp dir: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace temp dir mode = %v", info.Mode())
	}
	if got := env[len(env)-1]; got != "TMPDIR="+filepath.Join(workDir, ".tmp") {
		t.Fatalf("TMPDIR env = %q", got)
	}
	for _, path := range []string{
		filepath.Join(workDir, ".codex"),
		filepath.Join(workDir, ".codex", "memories"),
	} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("private dir %q = %v, %v", path, info, err)
		}
	}
}

func TestFencedAgentRejectsSymlinkedPrivateDirectory(t *testing.T) {
	workDir := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(workDir, ".codex")); err != nil {
		t.Fatal(err)
	}

	_, err := prepareFencedEnvironment(workDir, nil)
	want := fmt.Sprintf("codex: fenced private path must be a real directory: %q", filepath.Join(workDir, ".codex"))
	if err == nil || err.Error() != want {
		t.Fatalf("prepareFencedEnvironment() error = %v, want %q", err, want)
	}
}
