package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type isolatedTestAgent struct{ cujAgent }

func (a *isolatedTestAgent) SupportsIsolatedSessions() bool { return true }

func TestExternalIsolationRejectsMalformedAndSymlinkScopes(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	e := NewEngine("isolation", &isolatedTestAgent{}, nil, filepath.Join(base, "sessions.json"), LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(base, "bindings.json"))
	defer e.Stop()
	for _, key := range []string{"../brand", strings.Repeat("A", 64), strings.Repeat("a", 65), " "} {
		if _, err := e.isolatedWorkspace(SessionRuntime{IsolationKey: key}); err == nil {
			t.Fatalf("accepted %q", key)
		}
		if normalizeSessionRuntime(SessionRuntime{IsolationKey: key}).IsolationKey != key {
			t.Fatal("normalization downgraded isolation")
		}
	}
	a, err := e.isolatedWorkspace(SessionRuntime{IsolationKey: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.isolatedWorkspace(SessionRuntime{IsolationKey: strings.Repeat("b", 64)})
	if err != nil || a == b || !IsIsolatedWorkspace(a, strings.Repeat("a", 64)) {
		t.Fatal("scope collision")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "external-conversations", strings.Repeat("c", 64))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.isolatedWorkspace(SessionRuntime{IsolationKey: strings.Repeat("c", 64)}); err == nil {
		t.Fatal("followed scope symlink")
	}
}

func TestExternalIsolationNeverFallsBackToLegacyStartup(t *testing.T) {
	a := &cujAgent{}
	if _, err := startSessionWithRuntime(context.Background(), a, "", SessionRuntime{IsolationKey: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("legacy start accepted")
	}
	if len(a.sessions) != 0 {
		t.Fatal("unconfined process started")
	}
}
