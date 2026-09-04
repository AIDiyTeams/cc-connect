package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalBind_ConfigLoadsLoopbackHosts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(file, []byte(`
[bridge]
enabled = true
host = "127.0.0.1"
token = "test-token"
[management]
enabled = true
host = "127.0.0.1"
token = "test-token"
[[projects]]
name = "local"
[projects.agent]
type = "codex"
`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bridge.Host != "127.0.0.1" || cfg.Management.Host != "127.0.0.1" {
		t.Fatal("loopback hosts were not preserved by config loading")
	}
}
