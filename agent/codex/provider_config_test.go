package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCodexProviderConfig_CreatesNewFile(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")

	err := ensureCodexProviderConfig(home, "shengsuanyun",
		"https://router.shengsuanyun.com/api/v1", "responses",
		map[string]string{"HTTP-Referer": "https://openai.com/zh-Hans-CN/codex/", "X-Title": "CodeX"})
	if err != nil {
		t.Fatalf("ensureCodexProviderConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		`[model_providers.shengsuanyun]`,
		`env_key = "OPENAI_API_KEY"`,
		`wire_api = "responses"`,
		`base_url = "https://router.shengsuanyun.com/api/v1"`,
		`[model_providers.shengsuanyun.http_headers]`,
		`"HTTP-Referer" = "https://openai.com/zh-Hans-CN/codex/"`,
		`"X-Title" = "CodeX"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("config.toml missing %q\ngot:\n%s", want, content)
		}
	}
}

func TestEnsureCodexProviderConfig_UpdatesExistingSection(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	initial := `model = "gpt-5.4"

[model_providers.shengsuanyun]
name = "shengsuanyun"
env_key = "OLD_KEY"
wire_api = "chat"

[some_other_section]
key = "value"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureCodexProviderConfig(home, "shengsuanyun",
		"https://router.shengsuanyun.com/api/v1", "responses", nil)
	if err != nil {
		t.Fatalf("ensureCodexProviderConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, `env_key = "OPENAI_API_KEY"`) {
		t.Errorf("updated config missing new env_key\ngot:\n%s", content)
	}
	if strings.Contains(content, `env_key = "OLD_KEY"`) {
		t.Errorf("updated config still has old env_key\ngot:\n%s", content)
	}
	if !strings.Contains(content, `wire_api = "responses"`) {
		t.Errorf("updated config missing new wire_api\ngot:\n%s", content)
	}
	if !strings.Contains(content, `[some_other_section]`) {
		t.Errorf("updated config lost other section\ngot:\n%s", content)
	}
	if !strings.Contains(content, `model = "gpt-5.4"`) {
		t.Errorf("updated config lost top-level key\ngot:\n%s", content)
	}
}

func TestEnsureCodexProviderConfig_DefaultEnvKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")

	err := ensureCodexProviderConfig(home, "dmxapi", "https://www.dmxapi.cn/v1", "responses", nil)
	if err != nil {
		t.Fatalf("ensureCodexProviderConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, `env_key = "OPENAI_API_KEY"`) {
		t.Errorf("config should contain default env_key OPENAI_API_KEY\ngot:\n%s", content)
	}
	if strings.Contains(content, "requires_openai_auth") {
		t.Errorf("config should NOT contain requires_openai_auth\ngot:\n%s", content)
	}
}

func TestEnsureCodexProviderConfig_PreservesOtherProviders(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	initial := `[model_providers.other]
name = "other"
env_key = "OTHER_KEY"

[model_providers.other.http_headers]
"X-Custom" = "val"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureCodexProviderConfig(home, "shengsuanyun", "", "responses", nil)
	if err != nil {
		t.Fatalf("ensureCodexProviderConfig: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.Contains(content, `[model_providers.other]`) {
		t.Errorf("lost other provider section\ngot:\n%s", content)
	}
	if !strings.Contains(content, `[model_providers.shengsuanyun]`) {
		t.Errorf("new provider not added\ngot:\n%s", content)
	}
}

func TestEnsureCodexProviderConfig_SkipsWhenEmpty(t *testing.T) {
	err := ensureCodexProviderConfig("", "", "", "", nil)
	if err != nil {
		t.Fatalf("unexpected error for empty name: %v", err)
	}
}

func TestEnsureCodexAuth_WritesAuthJSON(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")

	err := ensureCodexAuth(home, "sk-test-key-123")
	if err != nil {
		t.Fatalf("ensureCodexAuth: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		`"OPENAI_API_KEY": "sk-test-key-123"`,
		`"auth_mode": "apikey"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("auth.json missing %q\ngot:\n%s", want, content)
		}
	}
}

func TestEnsureCodexAuth_SkipsEmptyKey(t *testing.T) {
	err := ensureCodexAuth(t.TempDir(), "")
	if err != nil {
		t.Fatalf("unexpected error for empty key: %v", err)
	}
}

func TestEnsureCodexAuth_DoesNotOverwriteInheritedGlobalAuthSymlink(t *testing.T) {
	global := filepath.Join(t.TempDir(), "auth.json")
	original := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"unit-test-only"}}`)
	if err := os.WriteFile(global, original, 0600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	local := filepath.Join(home, "auth.json")
	if err := os.Symlink(global, local); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexAuth(home, "local-provider-test-key"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(global)
	if err != nil || string(got) != string(original) {
		t.Fatal("local provider setup modified the inherited global auth file")
	}
	info, err := os.Lstat(local)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 {
		t.Fatal("local provider auth must be a private regular file")
	}
}

func TestEnsureCodexAuth_OverwritesExisting(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ensureCodexAuth(home, "new-api-key")
	if err != nil {
		t.Fatalf("ensureCodexAuth: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if strings.Contains(content, "chatgpt") {
		t.Errorf("auth.json still has old auth_mode\ngot:\n%s", content)
	}
	if !strings.Contains(content, `"auth_mode": "apikey"`) {
		t.Errorf("auth.json missing apikey mode\ngot:\n%s", content)
	}
	if !strings.Contains(content, `"OPENAI_API_KEY": "new-api-key"`) {
		t.Errorf("auth.json missing new key\ngot:\n%s", content)
	}
}

func TestEnsureCodexHomeInheritedConfigSyncsPermissionProfiles(t *testing.T) {
	globalHome := t.TempDir()
	perWorkspaceHome := filepath.Join(t.TempDir(), ".codex")
	sharedSkillsDir := filepath.Join(t.TempDir(), "Skills-OL-test")
	t.Setenv("CODEX_HOME", globalHome)

	globalConfig := `model = "deepseek-v4-flash"
default_permissions = "tomako-brand-fence"
project_root_markers = ["AGENTS.md"]

[permissions.tomako-brand-fence]
description = "Brand workspace fence"

[permissions.tomako-brand-fence.filesystem]
":minimal" = "read"
"/home/ubuntu/Skills-OL" = "read"

[permissions.tomako-brand-fence.filesystem.":workspace_roots"]
"." = "write"
".codex" = "read"
".codex/memories" = "write"

[permissions.tomako-brand-fence.network]
enabled = true

[projects."/host-only"]
trust_level = "trusted"
`
	if err := os.WriteFile(filepath.Join(globalHome, "config.toml"), []byte(globalConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureCodexHomeInheritedConfig(perWorkspaceHome, "tomako-brand-fence", sharedSkillsDir); err != nil {
		t.Fatalf("ensureCodexHomeInheritedConfig() error = %v", err)
	}
	if err := ensureCodexHomeInheritedConfig(perWorkspaceHome, "tomako-brand-fence", sharedSkillsDir); err != nil {
		t.Fatalf("ensureCodexHomeInheritedConfig() second call error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(perWorkspaceHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		`default_permissions = "tomako-brand-fence"`,
		`project_root_markers = ["AGENTS.md"]`,
		`[permissions.tomako-brand-fence.filesystem]`,
		`"/home/ubuntu/Skills-OL" = "read"`,
		fmt.Sprintf("%q = \"read\"", sharedSkillsDir),
		`[permissions.tomako-brand-fence.filesystem.":workspace_roots"]`,
		`".codex/memories" = "write"`,
		`[permissions.tomako-brand-fence.network]`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("per-workspace config missing %q\ngot:\n%s", want, content)
		}
	}
	if strings.Contains(content, "/host-only") {
		t.Fatalf("global project trust leaked into per-workspace config:\n%s", content)
	}
	if got := strings.Count(content, fmt.Sprintf("%q = \"read\"", sharedSkillsDir)); got != 1 {
		t.Fatalf("shared Skills read permission count = %d, want 1:\n%s", got, content)
	}
	if strings.Count(content, "project_root_markers") != 1 {
		t.Fatal("workspace root markers must be inherited exactly once")
	}
}

func TestAddPermissionReadPathRejectsRelativeDirectory(t *testing.T) {
	config := `[permissions.tomako-brand-fence.filesystem]
":minimal" = "read"
`
	if got := addPermissionReadPath(config, "tomako-brand-fence", "Skills-OL-test"); got != config {
		t.Fatalf("relative directory unexpectedly changed permission config:\n%s", got)
	}
}

func TestExtractTrustOnlyDropsStalePermissionProfile(t *testing.T) {
	config := `default_permissions = "stale"

[permissions.stale]
extends = ":danger-full-access"

[projects."/brand-workspace"]
trust_level = "trusted"
`
	got := extractTrustOnly(config)
	if strings.Contains(got, "default_permissions") || strings.Contains(got, "permissions.stale") {
		t.Fatalf("stale permission config survived:\n%s", got)
	}
	if !strings.Contains(got, `[projects."/brand-workspace"]`) || !strings.Contains(got, `trust_level = "trusted"`) {
		t.Fatalf("workspace trust was not preserved:\n%s", got)
	}
}
