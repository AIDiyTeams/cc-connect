package codex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This optional integration probe needs only a local Codex binary; it never
// connects to a model provider or opens a real workspace.
func TestFencedInheritanceLegacySkillsCannotBypassSandbox(t *testing.T) {
	binary := os.Getenv("CC_CODEX_SANDBOX_BINARY")
	if binary == "" {
		t.Skip("set CC_CODEX_SANDBOX_BINARY to run the synthetic Linux sandbox probe")
	}
	base := t.TempDir()
	global, brand := filepath.Join(base, "global"), filepath.Join(base, "brand")
	current, legacy := filepath.Join(base, "current-skills"), filepath.Join(base, "legacy-skills")
	home, scratch := filepath.Join(brand, ".codex"), filepath.Join(brand, ".tmp/current")
	alias := filepath.Join(base, "legacy-alias")
	for _, dir := range []string{global, home, scratch, filepath.Join(home, "memories"), current, legacy} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(current, "public.txt"), filepath.Join(current, "brand-analysis-pipeline.mjs"), filepath.Join(legacy, "private.txt")} {
		if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{alias, filepath.Join(brand, "legacy-link")} {
		if err := os.Symlink(legacy, path); err != nil {
			t.Fatal(err)
		}
	}
	config := fmt.Sprintf("[permissions.brand.filesystem]\n%q = \"read\"\n%q = \"read\"\n[permissions.brand.network]\nenabled = false\n", alias, legacy)
	if err := os.WriteFile(filepath.Join(global, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", global)
	if err := ensureCodexHomeInheritedConfig(home, "brand", current); err != nil {
		t.Fatal(err)
	}
	security, err := workspaceSecurityStartupArgs("brand", brand, scratch, "", current)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"sandbox"}, security...)
	args = append(args, "--", "/bin/sh", "-ec", `
test "$(cat "$1/public.txt")" = synthetic
! cat "$1/brand-analysis-pipeline.mjs" >/dev/null 2>&1
! cat "$2/private.txt" >/dev/null 2>&1
! cat "$3/private.txt" >/dev/null 2>&1
! cat legacy-link/private.txt >/dev/null 2>&1
printf normal-output > result.txt
printf verified
`, "sh", current, legacy, alias)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = brand
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + base, "CODEX_HOME=" + home, "TMPDIR=" + scratch}
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "verified" {
		t.Fatalf("synthetic sandbox failed: %v\n%s", err, output)
	}
}
