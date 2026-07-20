package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func (a *Agent) WriteMemoryFacts(_ context.Context, req core.AgentMemoryWriteRequest) (*core.AgentMemoryWriteResult, error) {
	factsDir, err := a.resolveFactsDir(req.WorkDir, req.SessionKey)
	if err != nil {
		return nil, err
	}
	a.mu.RLock()
	extension := a.memoryExtension
	factTitle := a.memoryFactTitle
	instructions := a.memoryInstructions
	a.mu.RUnlock()

	extension = firstNonBlank(extension, "tomako")
	extDir := filepath.Dir(factsDir)
	if err := ensureMemoryInstructions(extDir, instructions, extension); err != nil {
		return nil, err
	}

	fileName := memoryFactFileName(req.Title, req.SourceTaskID)
	target, err := safeFactPath(factsDir, fileName)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(target, []byte(renderMemoryFacts(req, factTitle)), 0o644); err != nil {
		return nil, fmt.Errorf("codex memory: write fact file: %w", err)
	}
	return &core.AgentMemoryWriteResult{File: target, Name: fileName}, nil
}

func (a *Agent) ListMemoryFacts(_ context.Context, req core.AgentMemoryListRequest) (*core.AgentMemoryListResult, error) {
	workDir, err := a.resolveWorkDir(req.WorkDir, req.SessionKey)
	if err != nil {
		return nil, err
	}
	factsDir, err := a.factsDirFor(workDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(factsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &core.AgentMemoryListResult{
				SessionKey: req.SessionKey,
				WorkDir:    workDir,
				FactsDir:   factsDir,
				Facts:      []core.AgentMemoryFactMeta{},
			}, nil
		}
		return nil, fmt.Errorf("codex memory: list facts: %w", err)
	}

	facts := make([]core.AgentMemoryFactMeta, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isFactFileName(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		facts = append(facts, core.AgentMemoryFactMeta{
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(facts, func(i, j int) bool {
		return facts[i].Name > facts[j].Name
	})
	return &core.AgentMemoryListResult{
		SessionKey: req.SessionKey,
		WorkDir:    workDir,
		FactsDir:   factsDir,
		Facts:      facts,
	}, nil
}

func (a *Agent) GetMemoryFact(_ context.Context, req core.AgentMemoryGetRequest) (*core.AgentMemoryFactFile, error) {
	factsDir, err := a.resolveFactsDir(req.WorkDir, req.SessionKey)
	if err != nil {
		return nil, err
	}
	target, err := safeFactPath(factsDir, req.Name)
	if err != nil {
		return nil, err
	}
	return readFactFile(target, req.Name)
}

func (a *Agent) UpdateMemoryFact(_ context.Context, req core.AgentMemoryUpdateRequest) (*core.AgentMemoryFactFile, error) {
	factsDir, err := a.resolveFactsDir(req.WorkDir, req.SessionKey)
	if err != nil {
		return nil, err
	}
	target, err := safeFactPath(factsDir, req.Name)
	if err != nil {
		return nil, err
	}
	// Upsert: Memory UI may create user-notes.md on first save.
	content := req.Content
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := writeFileAtomic(target, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("codex memory: update fact file: %w", err)
	}
	return readFactFile(target, req.Name)
}

func (a *Agent) DeleteMemoryFact(_ context.Context, req core.AgentMemoryDeleteRequest) error {
	factsDir, err := a.resolveFactsDir(req.WorkDir, req.SessionKey)
	if err != nil {
		return err
	}
	target, err := safeFactPath(factsDir, req.Name)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("codex memory: fact not found: %s", req.Name)
		}
		return fmt.Errorf("codex memory: delete fact: %w", err)
	}
	return nil
}

func (a *Agent) resolveFactsDir(workDir, sessionKey string) (string, error) {
	resolved, err := a.resolveWorkDir(workDir, sessionKey)
	if err != nil {
		return "", err
	}
	return a.factsDirFor(resolved)
}

func (a *Agent) resolveWorkDir(workDir, sessionKey string) (string, error) {
	a.mu.RLock()
	globalWorkDir := a.workDir
	sessionWorkspaceBase := a.sessionWorkspaceBase
	a.mu.RUnlock()

	// Prefer engine-resolved WorkDir (multi-workspace: Brand scope when present).
	// Fall back to session_workspace_base + session_key only for single-workspace
	// deployments that opt into that mapping.
	resolved := strings.TrimSpace(workDir)
	if resolved == "" {
		resolved = resolveSessionWorkDir(globalWorkDir, sessionWorkspaceBase, sessionKey)
	}
	if strings.TrimSpace(resolved) == "" {
		return "", fmt.Errorf("codex memory: work_dir is empty")
	}
	return resolved, nil
}

func (a *Agent) factsDirFor(workDir string) (string, error) {
	a.mu.RLock()
	extension := a.memoryExtension
	a.mu.RUnlock()
	extension = firstNonBlank(extension, "tomako")
	factsDir := filepath.Join(workDir, ".codex", "memories", "extensions", extension, "facts")
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		return "", fmt.Errorf("codex memory: create facts dir: %w", err)
	}
	return factsDir, nil
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

func isFactFileName(name string) bool {
	if name == "" || name != filepath.Base(name) {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	return strings.HasSuffix(strings.ToLower(name), ".md")
}

func safeFactPath(factsDir, name string) (string, error) {
	name = strings.TrimSpace(name)
	if !isFactFileName(name) {
		return "", fmt.Errorf("codex memory: invalid fact file name %q", name)
	}
	target := filepath.Join(factsDir, name)
	cleanFacts := filepath.Clean(factsDir)
	cleanTarget := filepath.Clean(target)
	prefix := cleanFacts + string(os.PathSeparator)
	if cleanTarget != cleanFacts && !strings.HasPrefix(cleanTarget, prefix) {
		return "", fmt.Errorf("codex memory: resolved fact file escaped facts dir")
	}
	return cleanTarget, nil
}

func readFactFile(path, name string) (*core.AgentMemoryFactFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("codex memory: fact not found: %s", name)
		}
		return nil, fmt.Errorf("codex memory: stat fact: %w", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("codex memory: read fact: %w", err)
	}
	return &core.AgentMemoryFactFile{
		Name:    name,
		Content: string(body),
		Size:    info.Size(),
		ModTime: info.ModTime().UTC().Format(time.RFC3339),
		Path:    path,
	}, nil
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
