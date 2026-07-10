package core

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceShareOptions controls how multi-workspace user dirs share a skill library.
// Product copy stays in the shared library; cc-connect only creates dirs/symlinks.
// Defaults match the historical Skills-OL / Tomako layout when fields are empty.
type WorkspaceShareOptions struct {
	SharedSkillsDir  string
	SharedSkillsEnv  string   // default SKILLS_OL_DIR
	SharedSkillsName string   // default Skills-OL
	PrivateDirs      []string // default outputs, .codex
	SymlinkItems     []string // default skills, node_modules, package.json
	SymlinkGlobs     []string // default *.mjs; empty slice disables globs
	UserDirPrefix    string   // default user-
	PlatformDocs     PlatformDocsOptions
}

// PlatformDocsOptions maps shared library docs into a user workspace via symlinks.
type PlatformDocsOptions struct {
	// Disable skips linking when true. Default false keeps platform docs forwarding on
	// (backwards compatible with Tomako / Skills-OL).
	Disable      bool
	SourceSubdir string // default platform-facts
	AgentsFile   string // default AGENTS.md
	FilePrefix   string // default platform-
	TargetRel    string // default .codex/memories/extensions/tomako/facts
}

// Normalize fills empty fields with built-in defaults (Tomako/Skills-OL compatible).
func (o WorkspaceShareOptions) Normalize() WorkspaceShareOptions {
	out := o
	if strings.TrimSpace(out.SharedSkillsEnv) == "" {
		out.SharedSkillsEnv = "SKILLS_OL_DIR"
	}
	if strings.TrimSpace(out.SharedSkillsName) == "" {
		out.SharedSkillsName = "Skills-OL"
	}
	if strings.TrimSpace(out.UserDirPrefix) == "" {
		out.UserDirPrefix = "user-"
	}
	if out.PrivateDirs == nil {
		out.PrivateDirs = []string{"outputs", ".codex"}
	}
	if out.SymlinkItems == nil {
		out.SymlinkItems = []string{"skills", "node_modules", "package.json"}
	}
	if out.SymlinkGlobs == nil {
		out.SymlinkGlobs = []string{"*.mjs"}
	}
	if strings.TrimSpace(out.PlatformDocs.SourceSubdir) == "" {
		out.PlatformDocs.SourceSubdir = "platform-facts"
	}
	if strings.TrimSpace(out.PlatformDocs.AgentsFile) == "" {
		out.PlatformDocs.AgentsFile = "AGENTS.md"
	}
	if strings.TrimSpace(out.PlatformDocs.FilePrefix) == "" {
		out.PlatformDocs.FilePrefix = "platform-"
	}
	if strings.TrimSpace(out.PlatformDocs.TargetRel) == "" {
		out.PlatformDocs.TargetRel = ".codex/memories/extensions/tomako/facts"
	}
	return out
}

// linkSharedPlatformFacts symlinks shared platform docs into a user workspace.
// Idempotent. Does not write file contents (business text stays in the skill library).
func linkSharedPlatformFacts(workspace, sharedSkillsDir string, docs PlatformDocsOptions) error {
	if strings.TrimSpace(workspace) == "" {
		return fmt.Errorf("workspace is empty")
	}
	if strings.TrimSpace(sharedSkillsDir) == "" {
		return fmt.Errorf("shared skills dir is empty")
	}
	docs = WorkspaceShareOptions{PlatformDocs: docs}.Normalize().PlatformDocs
	if docs.Disable {
		return nil
	}

	sharedFacts := filepath.Join(sharedSkillsDir, filepath.FromSlash(docs.SourceSubdir))
	info, err := os.Stat(sharedFacts)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("shared platform docs missing; skip linking", "path", sharedFacts)
			return nil
		}
		return fmt.Errorf("stat platform docs: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("platform docs is not a directory: %s", sharedFacts)
	}

	agentsName := docs.AgentsFile
	agentsSrc := filepath.Join(sharedFacts, agentsName)
	if _, err := os.Stat(agentsSrc); err == nil {
		if err := ensureSymlink(filepath.Join(workspace, agentsName), agentsSrc); err != nil {
			return fmt.Errorf("link %s: %w", agentsName, err)
		}
	}

	factsDstDir := filepath.Join(workspace, filepath.FromSlash(docs.TargetRel))
	if err := os.MkdirAll(factsDstDir, 0o755); err != nil {
		return fmt.Errorf("create facts dir: %w", err)
	}

	entries, err := os.ReadDir(sharedFacts)
	if err != nil {
		return fmt.Errorf("read platform docs: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isSharedPlatformFactFile(name, docs) {
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

func isSharedPlatformFactFile(name string, docs PlatformDocsOptions) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		return false
	}
	lower := strings.ToLower(name)
	agentsLower := strings.ToLower(strings.TrimSpace(docs.AgentsFile))
	if lower == "readme.md" || (agentsLower != "" && lower == agentsLower) {
		return false
	}
	prefix := strings.ToLower(strings.TrimSpace(docs.FilePrefix))
	if prefix == "" {
		return false
	}
	return strings.HasPrefix(lower, prefix)
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
