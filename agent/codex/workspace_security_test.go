package codex

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestWorkspaceSecurityStartupArgsProtectPrivateStateAndKeepOwnScratch(t *testing.T) {
	workDir := t.TempDir()
	scratchDir := filepath.Join(workDir, ".tmp", "conversations", "session-a")
	args, err := workspaceSecurityStartupArgs("tomako-brand-fence", workDir, scratchDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "-c" {
		t.Fatalf("unexpected startup arguments: %#v", args)
	}
	var config struct {
		Permissions map[string]struct {
			Filesystem map[string]any
		}
	}
	if _, err := toml.Decode(args[1], &config); err != nil {
		t.Fatalf("override is not valid TOML: %v", err)
	}
	want := map[string]any{
		"/etc/cc-connect": "none",
		"/etc/systemd":    "none",
		":workspace_roots": map[string]any{
			".codex":                       "none",
			".codex/memories":              "write",
			".tmp":                         "none",
			".tmp/conversations/session-a": "write",
		},
	}
	if !reflect.DeepEqual(config.Permissions["tomako-brand-fence"].Filesystem, want) {
		t.Fatalf("unexpected scope: %#v", config)
	}
}

func TestWorkspaceSecurityStartupArgsKeepQuotedPathsInsideTheValue(t *testing.T) {
	workDir := t.TempDir()
	profile := "host.profile-with-quotes\""
	scratchName := "session.a with spaces"
	args, err := workspaceSecurityStartupArgs(profile, workDir, filepath.Join(workDir, ".tmp", scratchName), "", "")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if _, err := toml.Decode(args[1], &config); err != nil {
		t.Fatal(err)
	}
	profiles := config["permissions"].(map[string]any)
	if len(profiles) != 1 || profiles[profile] == nil {
		t.Fatalf("profile name changed the permission hierarchy: %#v", profiles)
	}
	filesystem := profiles[profile].(map[string]any)["filesystem"].(map[string]any)
	scope := filesystem[":workspace_roots"].(map[string]any)
	if scope[".tmp/"+scratchName] != "write" || len(scope) != 4 {
		t.Fatalf("scratch path changed the permission hierarchy: %#v", scope)
	}
}

func TestWorkspaceSecurityStartupArgsRejectUnsafeScratchScopes(t *testing.T) {
	workDir := t.TempDir()
	for name, scratch := range map[string]string{
		"workspace":     workDir,
		"shared-temp":   filepath.Join(workDir, ".tmp"),
		"sibling":       filepath.Join(workDir, "..", "other"),
		"dot-traversal": filepath.Join(workDir, ".tmp", "a", "..", "..", "other"),
		"relative":      ".tmp/conversations/a",
		"control":       filepath.Join(workDir, ".tmp", "a\nb"),
	} {
		t.Run(name, func(t *testing.T) {
			if args, err := workspaceSecurityStartupArgs("fence", workDir, scratch, "", ""); err == nil || args != nil {
				t.Fatalf("unsafe scope accepted: args=%#v err=%v", args, err)
			}
		})
	}
}

func TestWorkspaceSecurityStartupArgsLeaveUnfencedSessionsAlone(t *testing.T) {
	args, err := workspaceSecurityStartupArgs("", "", "", "", "")
	if err != nil || args != nil {
		t.Fatalf("unexpected unfenced override: %#v, %v", args, err)
	}
	if _, err := workspaceSecurityStartupArgs(":danger-full-access", t.TempDir(), t.TempDir(), "", ""); err == nil {
		t.Fatal("built-in bypass profile accepted")
	}
}

func TestWorkspaceSecurityStartupArgsGrantOnlyCurrentExternalBroker(t *testing.T) {
	workDir := t.TempDir()
	brokerDir := filepath.Join(t.TempDir(), `runtime.a with "quotes"`)
	args, err := workspaceSecurityStartupArgs("fence", workDir, filepath.Join(workDir, ".tmp", "session"), brokerDir, "")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if _, err := toml.Decode(args[1], &config); err != nil {
		t.Fatal(err)
	}
	fs := config["permissions"].(map[string]any)["fence"].(map[string]any)["filesystem"].(map[string]any)
	if len(fs) != 4 || fs[brokerDir] != "read" {
		t.Fatalf("unexpected broker grants: %#v", fs)
	}
	if len(args) != 4 || args[2] != "-c" {
		t.Fatalf("broker network policy is missing: %#v", args)
	}
	var networkConfig map[string]any
	if _, err := toml.Decode(args[3], &networkConfig); err != nil {
		t.Fatal(err)
	}
	wantNetwork := map[string]any{
		"enabled": true, "proxy_url": "http://127.0.0.1:0", "enable_socks5": false,
		"allow_upstream_proxy": false, "allow_local_binding": false,
		"dangerously_allow_all_unix_sockets": false, "mode": "full",
		"domains":      map[string]any{"*": "allow"},
		"unix_sockets": map[string]any{filepath.Join(brokerDir, "cap.sock"): "allow"},
	}
	gotNetwork := networkConfig["features"].(map[string]any)["network_proxy"]
	if !reflect.DeepEqual(gotNetwork, wantNetwork) {
		t.Fatalf("unexpected network capability scope: %#v", gotNetwork)
	}
}

func TestWorkspaceSecurityStartupArgsRejectUnsafeBrokerScopes(t *testing.T) {
	workDir := t.TempDir()
	for name, broker := range map[string]string{
		"workspace":       workDir,
		"hidden-home":     filepath.Join(workDir, ".codex", "runtime"),
		"hidden-temp":     filepath.Join(workDir, ".tmp", "runtime"),
		"workspace-child": filepath.Join(workDir, "runtime"),
		"ancestor":        filepath.Dir(workDir),
		"root":            string(filepath.Separator),
		"relative":        "runtime",
		"control":         filepath.Join(t.TempDir(), "bad\nname"),
	} {
		t.Run(name, func(t *testing.T) {
			if args, err := workspaceSecurityStartupArgs("fence", workDir, filepath.Join(workDir, ".tmp", "session"), broker, ""); err == nil || args != nil {
				t.Fatalf("unsafe broker scope accepted: args=%#v err=%v", args, err)
			}
		})
	}
}

func TestWorkspaceSecurityStartupArgsHideOnlyPrivateSharedSkillPaths(t *testing.T) {
	workDir, sharedDir := t.TempDir(), t.TempDir()
	args, err := workspaceSecurityStartupArgs("fence", workDir, filepath.Join(workDir, ".tmp", "session"), "", sharedDir)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if _, err := toml.Decode(args[1], &config); err != nil {
		t.Fatal(err)
	}
	fs := config["permissions"].(map[string]any)["fence"].(map[string]any)["filesystem"].(map[string]any)
	for _, private := range []string{".git", ".github", ".codex", ".agents", "test", "test-fixtures", "docs", "deploy", "AGENTS.md", "CONTRIBUTING.md", "brand-analysis-pipeline.mjs", "onboarding-activation-pipeline.mjs", "scripts/__pycache__", "skills/brand-name-finder-kit/scripts/__pycache__", "skills/result-writer/scripts/__pycache__"} {
		if fs[filepath.Join(sharedDir, filepath.FromSlash(private))] != "none" {
			t.Fatalf("private runtime path not hidden: %s", private)
		}
	}
	for _, public := range []string{".cache", "examples", "skills", "platform-facts", "capability-fetch.mjs", "machine-auth.mjs", "scripts/capability_transport.py"} {
		if _, exists := fs[filepath.Join(sharedDir, filepath.FromSlash(public))]; exists {
			t.Fatalf("required runtime path changed: %s", public)
		}
	}
}
