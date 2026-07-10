package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Shared platform docs live in Skills-OL/platform-facts/.
// cc-connect only creates symlinks into each user workspace — no product copy here.

const (
	sharedPlatformFactsDirName = "platform-facts"
	workspaceAgentsMDName      = "AGENTS.md"
	workspacePlatformFactsRel  = ".codex/memories/extensions/tomako/facts"
)

// linkSharedPlatformFacts symlinks Skills-OL/platform-facts into a user workspace:
//   - AGENTS.md → <shared>/AGENTS.md
//   - platform-*.md → <workspace>/.codex/memories/extensions/tomako/facts/<name>
// Idempotent. Does not write file contents (business text stays in Skills-OL).
func linkSharedPlatformFacts(workspace, sharedSkillsDir string) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("workspace is empty")
	}
	if strings.TrimSpace(sharedSkillsDir) == "" {
		return fmt.Errorf("shared skills dir is empty")
	}
	sharedFacts := filepath.Join(sharedSkillsDir, sharedPlatformFactsDirName)
	info, err := os.Stat(sharedFacts)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("shared platform-facts missing; skip linking", "path", sharedFacts)
			return nil
		}
		return fmt.Errorf("stat platform-facts: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("platform-facts is not a directory: %s", sharedFacts)
	}

	agentsSrc := filepath.Join(sharedFacts, workspaceAgentsMDName)
	if _, err := os.Stat(agentsSrc); err == nil {
		if err := ensureSymlink(filepath.Join(workspace, workspaceAgentsMDName), agentsSrc); err != nil {
			return fmt.Errorf("link AGENTS.md: %w", err)
		}
	}

	factsDstDir := filepath.Join(workspace, filepath.FromSlash(workspacePlatformFactsRel))
	if err := os.MkdirAll(factsDstDir, 0o755); err != nil {
		return fmt.Errorf("create facts dir: %w", err)
	}

	entries, err := os.ReadDir(sharedFacts)
	if err != nil {
		return fmt.Errorf("read platform-facts: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isSharedPlatformFactFile(name) {
			continue
		}
		src := filepath.Join(sharedFacts, name)
		dst := filepath.Join(factsDstDir, name)
		if err := ensureSymlink(dst, src); err != nil {
			return fmt.Errorf("link platform fact %s: %w", name, err)
		}
	}
	return nil
}

func isSharedPlatformFactFile(name string) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		return false
	}
	lower := strings.ToLower(name)
	if lower == "readme.md" || lower == "agents.md" {
		return false
	}
	return strings.HasPrefix(lower, "platform-")
}

// ensureSymlink makes linkPath a symlink to targetPath.
// Replaces a broken/wrong symlink or a regular file at a platform-managed path.
func ensureSymlink(linkPath, targetPath string) error {
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absTarget); err != nil {
		return fmt.Errorf("symlink target missing: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return err
	}

	fi, err := os.Lstat(linkPath)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			current, readErr := os.Readlink(linkPath)
			if readErr == nil {
				currentAbs := current
				if !filepath.IsAbs(current) {
					currentAbs = filepath.Join(filepath.Dir(linkPath), current)
				}
				if filepath.Clean(currentAbs) == filepath.Clean(absTarget) {
					return nil
				}
			}
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("remove existing path before symlink: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	if err := os.Symlink(absTarget, linkPath); err != nil {
		return err
	}
	return nil
}
