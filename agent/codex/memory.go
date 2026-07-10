package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) WriteMemoryFacts(_ context.Context, req core.AgentMemoryWriteRequest) (*core.AgentMemoryWriteResult, error) {
	a.mu.RLock()
	globalWorkDir := a.workDir
	sessionWorkspaceBase := a.sessionWorkspaceBase
	extension := a.memoryExtension
	factTitle := a.memoryFactTitle
	instructions := a.memoryInstructions
	a.mu.RUnlock()

	// Prefer engine-resolved WorkDir (multi-workspace: {base_dir}/user-{id}).
	// Fall back to session_workspace_base + session_key only for single-workspace
	// deployments that opt into that mapping.
	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		workDir = resolveSessionWorkDir(globalWorkDir, sessionWorkspaceBase, req.SessionKey)
	}
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("codex memory: work_dir is empty")
	}

	extension = firstNonBlank(extension, "tomako")
	extDir := filepath.Join(workDir, ".codex", "memories", "extensions", extension)
	factsDir := filepath.Join(extDir, "facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		return nil, fmt.Errorf("codex memory: create facts dir: %w", err)
	}
	if err := ensureMemoryInstructions(extDir, instructions, extension); err != nil {
		return nil, err
	}

	fileName := memoryFactFileName(req.Title, req.SourceTaskID)
	target := filepath.Join(factsDir, fileName)
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(factsDir)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("codex memory: resolved fact file escaped facts dir")
	}
	if err := writeFileAtomic(target, []byte(renderMemoryFacts(req, factTitle)), 0o644); err != nil {
		return nil, fmt.Errorf("codex memory: write fact file: %w", err)
	}
	return &core.AgentMemoryWriteResult{File: target}, nil
}

func resolveSessionWorkDir(defaultWorkDir, sessionWorkspaceBase, sessionKey string) string {
	base := strings.TrimSpace(sessionWorkspaceBase)
	if base == "" {
		return defaultWorkDir
	}
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return defaultWorkDir
	}
	return filepath.Join(base, sanitizeSessionKey(key))
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func sanitizeSessionKey(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, value)
	value = strings.Trim(value, "_")
	if value == "" {
		return "unknown"
	}
	return value
}

func ensureMemoryInstructions(extDir, customBody, extension string) error {
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return fmt.Errorf("codex memory: create extension dir: %w", err)
	}
	path := filepath.Join(extDir, "instructions.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("codex memory: stat instructions: %w", err)
	}
	content := strings.TrimSpace(customBody)
	if content == "" {
		content = fmt.Sprintf(`# Platform Facts (%s)

Structured facts written by the host platform into this Codex memory extension.

Process each fact file and integrate relevant information into MEMORY.md
and memory_summary.md for future tasks.
`, firstNonBlank(extension, "default"))
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := writeFileAtomic(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("codex memory: write instructions: %w", err)
	}
	return nil
}

func memoryFactFileName(title, sourceTaskID string) string {
	ts := time.Now().Format("2006-01-02T15-04-05")
	safeTitle := sanitizeSessionKey(firstNonBlank(title, "skill-result"))
	safeTask := sanitizeSessionKey(firstNonBlank(sourceTaskID, "unknown"))
	return strings.ToLower(ts + "-" + safeTitle + "-" + safeTask + ".md")
}

func renderMemoryFacts(req core.AgentMemoryWriteRequest, defaultTitle string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonBlank(req.Title, defaultTitle, "Memory Fact"))
	b.WriteString("\n\n")
	b.WriteString("- Source task: ")
	b.WriteString(firstNonBlank(req.SourceTaskID, "unknown"))
	b.WriteString("\n")
	b.WriteString("- Created at: ")
	b.WriteString(time.Now().Format(time.RFC3339))
	b.WriteString("\n\n## Facts\n\n")
	for _, fact := range req.Facts {
		if strings.TrimSpace(fact.Type) == "" || strings.TrimSpace(fact.Value) == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(fact.Type))
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(fact.Value))
		b.WriteString("\n")
	}
	return b.String()
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
