package codex

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Every conversation of a product runs in the same fenced brand workspace, and
// that workspace's .tmp used to be the scratch space of all of them: a later
// conversation listed it, found an earlier one's scraper scripts and reused
// them. A conversation now gets .tmp/conversations/<id> as its thread's TMPDIR.
// Each turn marks the directory as used; directories unused for
// conversationScratchTTL are removed when another conversation prepares its
// own. Durable product knowledge lives in memories and saved documents, which
// are outside .tmp. Background tasks without a conversation keep the shared
// .tmp.
const conversationScratchTTL = 30 * 24 * time.Hour

const conversationScratchDirName = "conversations"

// conversationScratchPath returns where a conversation's scratch directory
// belongs, or "" when the session is not a fenced conversation.
func conversationScratchPath(workDir, permissionsProfile, chatSessionID string) string {
	if strings.TrimSpace(permissionsProfile) == "" || !filepath.IsAbs(workDir) {
		return ""
	}
	name := conversationScratchName(chatSessionID)
	if name == "" {
		return ""
	}
	return filepath.Join(workDir, ".tmp", conversationScratchDirName, name)
}

// conversationScratchName keeps an identifier usable as a single path element.
func conversationScratchName(chatSessionID string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(chatSessionID))
	name = strings.Trim(name, "_")
	if len(name) > 96 {
		name = name[:96]
	}
	return name
}

// prepareConversationScratch makes the current conversation's scratch
// directory, records that the conversation used it now and removes expired
// directories of other conversations. It returns "" when the session has no
// conversation scratch directory or it could not be prepared; the thread then
// keeps the workspace's shared .tmp.
func (s *appServerSession) prepareConversationScratch(now time.Time) string {
	s.runtimeMu.RLock()
	chatSessionID := s.runtime.ChatSessionID
	s.runtimeMu.RUnlock()
	dir := conversationScratchPath(s.workDir, s.permissionsProfile, chatSessionID)
	if dir == "" {
		return ""
	}
	if err := prepareConversationScratchDir(dir, now); err != nil {
		slog.Warn("codex: conversation scratch dir unavailable; using the workspace temporary directory", "error", err)
		return ""
	}
	return dir
}

// conversationScratchReady reports whether dir, its conversations parent and
// the workspace .tmp are real directories, so a link made inside the workspace
// is never advertised as the conversation's scratch space.
func conversationScratchReady(dir string) bool {
	if dir == "" {
		return false
	}
	for _, path := range []string{filepath.Dir(filepath.Dir(dir)), filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

func prepareConversationScratchDir(dir string, now time.Time) error {
	root := filepath.Dir(dir)
	for _, path := range []string{filepath.Dir(root), root, dir} {
		if err := ensureFencedPrivateDir(path, 0o700); err != nil {
			return err
		}
	}
	if err := os.Chtimes(dir, now, now); err != nil {
		return fmt.Errorf("codex: mark conversation scratch dir used: %w", err)
	}
	removeExpiredConversationScratch(root, dir, now)
	return nil
}

// removeExpiredConversationScratch deletes conversation directories whose last
// recorded use is older than conversationScratchTTL. Only direct children of
// the conversations directory are considered, and a symlink is removed as a
// link, never followed.
func removeExpiredConversationScratch(root, keep string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("codex: list conversation scratch dirs", "error", err)
		return
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		if path == keep {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || now.Sub(info.ModTime()) < conversationScratchTTL {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("codex: remove expired conversation scratch dir", "path", path, "error", err)
		}
	}
}
