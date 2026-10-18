package codex

import (
	"context"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestExternalIsolationRefusesSharedOrUnconfinedRuntime(t *testing.T) {
	key := strings.Repeat("a", 64)
	for _, a := range []*Agent{
		{backend: "exec", workDir: t.TempDir()},
		{backend: "app_server", appServerURL: "stdio://", workDir: t.TempDir()},
		{backend: "app_server", appServerURL: "ws://127.0.0.1:3845", permissionsProfile: "fence", workDir: t.TempDir()},
		{backend: "app_server", appServerURL: "stdio://", permissionsProfile: "fence", workDir: t.TempDir()},
	} {
		if _, err := a.StartSessionWithRuntime(context.Background(), "", core.SessionRuntime{IsolationKey: key}); err == nil {
			t.Fatal("unsafe startup accepted")
		}
	}
}

func TestExternalIsolationCannotChangeOnLiveSession(t *testing.T) {
	key := strings.Repeat("a", 64)
	s := &appServerSession{isolationKey: key}
	s.alive.Store(true)
	for _, next := range []string{"", strings.Repeat("b", 64), "../private"} {
		r := core.SessionRuntime{IsolationKey: next}
		if s.SupportsSessionRuntime(r) {
			t.Fatal("cross-scope reuse accepted")
		}
		if err := s.SetSessionRuntime(r); err == nil {
			t.Fatal("cross-scope update accepted")
		}
	}
}
