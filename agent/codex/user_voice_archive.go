package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// One active archive batch per Bridge process; the Skills adapter spaces its HTTP requests.
var userVoiceArchiveGate = make(chan struct{}, 1)

func (s *appServerSession) isUserVoiceArchiveRuntime() bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtime.Scene == "growth_opportunity_user_voice_search" &&
		strings.EqualFold(s.runtime.LogicalModel, "DEEPSEEK_V4_FLASH_SEARCH")
}

func userVoiceArchiveDynamicTools() []map[string]any {
	return []map[string]any{{"type": "function", "name": "search_reddit_archive", "deferLoading": false,
		"description": "Fetch one bounded batch of Arctic Shift archive snapshots from up to 10 public subreddits and an explicit window of at most 360 hours. Review returned originals locally. Archive content does not prove current Reddit state. At most one call per task.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"subreddits", "after", "before"}, "properties": map[string]any{
				"subreddits": map[string]any{"type": "array", "minItems": 1, "maxItems": 10, "items": map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_]{2,21}$"}},
				"after":      map[string]any{"type": "string"}, "before": map[string]any{"type": "string"},
			}},
	}}
}

func (s *appServerSession) collectUserVoiceArchive(arguments map[string]any) (string, error) {
	if !s.isUserVoiceArchiveRuntime() {
		return "", fmt.Errorf("archive tool unavailable for this task")
	}
	s.archiveMu.Lock()
	if s.archiveUsed {
		s.archiveMu.Unlock()
		return "", fmt.Errorf("archive batch already requested; use its preserved result")
	}
	s.archiveUsed = true
	s.archiveMu.Unlock()
	script := strings.TrimSpace(os.Getenv("TOMAKO_USER_VOICE_ARCHIVE_SCRIPT"))
	if script == "" {
		root := strings.TrimSpace(os.Getenv("SKILLS_OL_DIR"))
		if root == "" {
			return "", fmt.Errorf("active Skills directory missing for archive tool")
		}
		script = filepath.Join(root, "user-voice-archive.mjs")
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > 8192 {
		return "", fmt.Errorf("invalid archive arguments")
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	select {
	case userVoiceArchiveGate <- struct{}{}:
		defer func() { <-userVoiceArchiveGate }()
	case <-ctx.Done():
		return "", fmt.Errorf("archive collection deadline exceeded")
	}
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Dir = s.workDir
	cmd.Stdin = bytes.NewReader(encoded)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("archive collector failed: %w", err)
	}
	if stdout.Len() > 8*1024*1024 {
		return "", fmt.Errorf("archive result exceeds 8 MiB")
	}
	var result map[string]any
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result["source"] != "arctic_shift" {
		return "", fmt.Errorf("invalid archive collector result")
	}
	return stdout.String(), nil
}

// Keep a complete small execution receipt in the event ledger, not a truncated body or model claim.
func archiveExecutionReceipt(raw string) string {
	var result map[string]any
	if json.Unmarshal([]byte(raw), &result) != nil || result["source"] != "arctic_shift" {
		return "archive tool returned no valid receipt"
	}
	receipt := map[string]any{}
	for _, key := range []string{"source", "sourceUrl", "retrievedAt", "window", "subreddits", "execution", "coverage"} {
		receipt[key] = result[key]
	}
	encoded, _ := json.Marshal(receipt)
	return string(encoded)
}
