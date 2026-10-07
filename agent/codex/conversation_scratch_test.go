package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func conversationScratchTestSession(t *testing.T, chatSessionID string) (*appServerSession, string) {
	t.Helper()
	workDir := t.TempDir()
	env, err := prepareFencedEnvironment(workDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &appServerSession{workDir: workDir, permissionsProfile: "tomako-brand-fence", extraEnv: env,
		developerInstructionsManaged: true,
		runtime:                      core.SessionRuntime{ChatSessionID: chatSessionID, DeveloperInstructions: "Current application policy"}}
	return s, workDir
}

func threadTMPDIR(s *appServerSession) (string, bool) {
	value, ok := s.threadRequestParams()["config"].(map[string]any)["shell_environment_policy.set.TMPDIR"]
	text, _ := value.(string)
	return text, ok
}

func turnInstructions(s *appServerSession) string {
	text, _ := s.turnDeveloperInstructions()["settings"].(map[string]any)["developer_instructions"].(string)
	return text
}

func TestEachConversationGetsItsOwnScratchDirectoryAsTMPDIR(t *testing.T) {
	s, workDir := conversationScratchTestSession(t, "csess-a")
	first := filepath.Join(workDir, ".tmp", "conversations", "csess-a")
	if got, ok := threadTMPDIR(s); !ok || got != first {
		t.Fatalf("TMPDIR = %q, want %q", got, first)
	}
	info, err := os.Stat(first)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("scratch dir = %v, %v", info, err)
	}
	if text := turnInstructions(s); !strings.Contains(text, first) || !strings.Contains(text, "this conversation's own temporary directory") {
		t.Fatalf("instructions did not name the conversation's directory: %q", text)
	}

	s.runtime.ChatSessionID = "csess-b"
	if got, _ := threadTMPDIR(s); got != filepath.Join(workDir, ".tmp", "conversations", "csess-b") {
		t.Fatalf("second conversation TMPDIR = %q", got)
	}
}

func TestTasksWithoutAConversationAndUnfencedSessionsKeepTheirTemporaryDirectory(t *testing.T) {
	s, workDir := conversationScratchTestSession(t, "")
	if got, ok := threadTMPDIR(s); ok {
		t.Fatalf("a background task got a conversation TMPDIR %q", got)
	}
	if text := turnInstructions(s); !strings.Contains(text, filepath.Join(workDir, ".tmp")) || !strings.Contains(text, "the prepared workspace temporary directory") {
		t.Fatalf("background task lost the workspace temporary directory: %q", text)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".tmp", "conversations")); !os.IsNotExist(err) {
		t.Fatal("a background task created conversation scratch space")
	}

	unfenced := &appServerSession{workDir: workDir, runtime: core.SessionRuntime{ChatSessionID: "csess-a"}}
	if got, ok := threadTMPDIR(unfenced); ok {
		t.Fatalf("an unfenced session got a conversation TMPDIR %q", got)
	}
}

func TestConversationScratchNamesStayInsideTheConversationsDirectory(t *testing.T) {
	for id, want := range map[string]string{
		"csess-0a1b":       "/w/.tmp/conversations/csess-0a1b",
		"../../etc/passwd": "/w/.tmp/conversations/etc_passwd",
		"a/b c":            "/w/.tmp/conversations/a_b_c",
		"  ":               "",
		"..":               "",
	} {
		if got := conversationScratchPath("/w", "fence", id); got != want {
			t.Fatalf("path for %q = %q, want %q", id, got, want)
		}
	}
	if got := conversationScratchPath("relative", "fence", "csess-a"); got != "" {
		t.Fatalf("a relative workspace produced %q", got)
	}
}

func TestExpiredConversationScratchIsRemovedAndRecentOrCurrentKept(t *testing.T) {
	s, workDir := conversationScratchTestSession(t, "csess-current")
	root := filepath.Join(workDir, ".tmp", "conversations")
	now := time.Now()
	for name, age := range map[string]time.Duration{"csess-old": 31 * 24 * time.Hour, "csess-recent": 24 * time.Hour, "csess-current": 40 * 24 * time.Hour} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "scraper.mjs"), []byte("// leftover"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	if got := s.prepareConversationScratch(now); got != filepath.Join(root, "csess-current") {
		t.Fatalf("prepared %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "csess-old")); !os.IsNotExist(err) {
		t.Fatal("a conversation unused for 31 days kept its scratch")
	}
	if _, err := os.Stat(filepath.Join(root, "csess-recent", "scraper.mjs")); err != nil {
		t.Fatal("a recently used conversation lost its scratch")
	}
	info, err := os.Stat(filepath.Join(root, "csess-current"))
	if err != nil || now.Sub(info.ModTime()) > time.Minute {
		t.Fatal("the current conversation was not marked as used")
	}
	if _, err := os.Stat(filepath.Join(root, "csess-current", "scraper.mjs")); err != nil {
		t.Fatal("the current conversation lost its own files")
	}
}

func TestSymlinkedScratchSpaceFallsBackToTheWorkspaceTemporaryDirectory(t *testing.T) {
	s, workDir := conversationScratchTestSession(t, "csess-a")
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "csess-a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, ".tmp", "conversations")); err != nil {
		t.Fatal(err)
	}
	if got, ok := threadTMPDIR(s); ok {
		t.Fatalf("followed a symlinked conversations directory to %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Join(outside, "csess-a")); len(entries) != 0 {
		t.Fatal("wrote outside the workspace through a symlink")
	}
	if text := turnInstructions(s); !strings.Contains(text, "the prepared workspace temporary directory") || strings.Contains(text, "conversations") {
		t.Fatalf("advertised a linked directory: %q", text)
	}
}
