package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func envNames(env []string) map[string]bool {
	names := map[string]bool{}
	for _, entry := range env {
		if name, _, ok := strings.Cut(entry, "="); ok {
			names[name] = true
		}
	}
	return names
}

func TestCodexProcessEnvDropsTheBridgesCredentials(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "provider-secret")
	t.Setenv(webSearchConfigEnv, "/secrets/web-search.env")
	t.Setenv("XAI_API_KEY", "x-search-key")
	names := envNames(codexProcessEnv([]string{"CODEX_HOME=/w/.codex", "DEEPSEEK_API_KEY=from-project"}))
	if names["DEEPSEEK_API_KEY"] || names[webSearchConfigEnv] {
		t.Fatalf("Codex would inherit bridge credentials: %v", names)
	}
	if !names["XAI_API_KEY"] || !names["CODEX_HOME"] || !names["PATH"] {
		t.Fatalf("Codex lost variables it needs: %v", names)
	}
}

// Codex hands its own environment to the shell snapshot that every Agent
// command sources, so the credentials must be absent from the process itself.
func TestAppServerStartsWithoutTheBridgesCredentials(t *testing.T) {
	workDir := t.TempDir()
	capture := filepath.Join(workDir, "env-names")
	writeFakeCodexScript(t, workDir, `#!/bin/sh
env | cut -d= -f1 > "$CC_TEST_ENV_NAMES"
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":1,"result":{}}\n' ;;
  esac
done
`, `
[System.IO.File]::WriteAllLines($env:CC_TEST_ENV_NAMES, [string[]](Get-ChildItem Env: | ForEach-Object Name))
while (($line = [Console]::In.ReadLine()) -ne $null) {
  if ($line -like '*"method":"initialize"*') { [Console]::Out.WriteLine('{"id":1,"result":{}}') }
}
`)
	t.Setenv("PATH", workDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEEPSEEK_API_KEY", "provider-secret")
	t.Setenv(webSearchConfigEnv, "/secrets/web-search.env")
	t.Setenv("XAI_API_KEY", "x-search-key")
	s, err := newAppServerSession(context.Background(), "", workDir, "", "", "", "", "", "", "", []string{"CC_TEST_ENV_NAMES=" + capture}, "",
		core.SessionRuntime{TaskID: "cmsg-a", WorkspaceID: "ws-a", BrandID: "b-a", ChatSessionID: "csess-a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, line := range strings.Fields(string(body)) {
		names[strings.ToUpper(line)] = true
	}
	if names["DEEPSEEK_API_KEY"] || names[webSearchConfigEnv] {
		t.Fatalf("app-server inherited bridge credentials: %s", body)
	}
	if !names["XAI_API_KEY"] {
		t.Fatalf("X search lost its key: %s", body)
	}
}
