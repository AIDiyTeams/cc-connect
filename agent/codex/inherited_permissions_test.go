package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestFencedInheritanceDropsLegacySkillsAliasesAndNestedGrants(t *testing.T) {
	global, home, legacy, current := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	alias := filepath.Join(t.TempDir(), "old-skills")
	if err := os.Symlink(legacy, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", global)
	config := fmt.Sprintf(`model = "synthetic-model"
default_permissions = "legacy"
[permissions.brand]
extends = "legacy"
[permissions.brand.filesystem]
%q = "read"
%q = "read"
":root" = "read"
[permissions.brand.filesystem.":workspace_roots"]
".codex/auth.json" = "read"
[permissions.brand.network]
enabled = true
allow_local_binding = true
[permissions.brand.network.unix_sockets]
"/synthetic/sibling.sock" = "allow"
[permissions.legacy.filesystem]
":root" = "write"
[model_providers.synthetic]
base_url = "https://example.invalid"
`, alias, legacy)
	if err := os.WriteFile(filepath.Join(global, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, skills := range []string{current, filepath.Join(t.TempDir(), "next-skills")} {
		if err := ensureCodexHomeInheritedConfig(home, "brand", skills); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if _, err := toml.Decode(string(data), &parsed); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"brand": map[string]any{
			"filesystem": map[string]any{
				":minimal": "read", skills: "read",
				":workspace_roots": map[string]any{".": "write", ".codex": "none", ".codex/memories": "write", ".tmp": "none"},
			},
			"network": map[string]any{"enabled": true},
		}}
		if !reflect.DeepEqual(parsed["permissions"], want) || parsed["default_permissions"] != "brand" {
			t.Fatalf("permission grants survived replacement: %#v", parsed["permissions"])
		}
		if parsed["model"] != "synthetic-model" || parsed["model_providers"] == nil {
			t.Fatal("provider routing was lost")
		}
	}
}

func TestFencedInheritanceRejectsMissingProfileAndRelativeSkills(t *testing.T) {
	for _, tc := range []struct{ config, skills string }{
		{`[permissions.other.filesystem]`, t.TempDir()},
		{`[permissions.brand.filesystem]`, "relative-skills"},
		{`[permissions.brand.filesystem]`, "/"},
	} {
		if _, err := fencedInheritedPermissions(tc.config, "brand", tc.skills); err == nil {
			t.Fatalf("unsafe inheritance unexpectedly succeeded: %#v", tc)
		}
	}
	global := t.TempDir()
	t.Setenv("CODEX_HOME", global)
	if err := ensureCodexHomeInheritedConfig(global, "brand", t.TempDir()); err == nil || !strings.Contains(err.Error(), "private codex home") {
		t.Fatalf("fenced host home should fail closed, got %v", err)
	}
}
