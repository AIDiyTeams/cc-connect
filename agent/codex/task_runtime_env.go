package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

var taskRuntimeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$`)

func updateTaskRuntimeEnv(existingPath string, runtime core.SessionRuntime) (string, error) {
	token := strings.TrimSpace(runtime.MachineCapabilityToken)
	imageToken := strings.TrimSpace(runtime.ImageCapabilityToken)
	envelope := strings.TrimSpace(runtime.TaskAuthorityEnvelopeB64)
	if token == "" && imageToken == "" && envelope == "" {
		if existingPath == "" {
			return "", nil
		}
		// Keep the path already bound to the thread, but revoke prior authority.
		return writeTaskRuntimeEnv(existingPath, "")
	}
	if token == "" || envelope == "" {
		return existingPath, fmt.Errorf("machine capability and task authority envelope must be supplied together")
	}
	if imageToken == "" {
		imageToken = token
	}
	taskID := strings.TrimSpace(runtime.TaskID)
	if !taskRuntimeIDPattern.MatchString(taskID) {
		return existingPath, fmt.Errorf("valid task id is required for machine authority")
	}
	for label, value := range map[string]string{
		"machine capability":      token,
		"image capability":        imageToken,
		"task authority envelope": envelope,
	} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return existingPath, fmt.Errorf("%s contains forbidden control characters", label)
		}
	}

	content := strings.Join([]string{
		"export MACHINE_CAPABILITY_TOKEN=" + shellSingleQuote(token),
		"export IMAGE_CAPABILITY_TOKEN=" + shellSingleQuote(imageToken),
		"export TOMAKO_TASK_AUTHORITY_ENVELOPE_B64=" + shellSingleQuote(envelope),
		"export TASK_AUTHORITY_ENVELOPE_B64=" + shellSingleQuote(envelope),
		"",
	}, "\n")
	return writeTaskRuntimeEnv(existingPath, content)
}

func writeTaskRuntimeEnv(existingPath, content string) (string, error) {
	path := existingPath
	if path == "" {
		dir, err := os.MkdirTemp("", "cc-connect-task-runtime-")
		if err != nil {
			return "", fmt.Errorf("create task runtime directory: %w", err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("protect task runtime directory: %w", err)
		}
		path = filepath.Join(dir, "machine.env")
	}

	// A failed initial write must not leave a directory that Close cannot find.
	published := false
	defer func() {
		if existingPath == "" && !published {
			removeTaskRuntimeEnv(path)
		}
	}()
	tmp, err := os.CreateTemp(filepath.Dir(path), "machine.env.tmp-*")
	if err != nil {
		return existingPath, fmt.Errorf("create task runtime file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return existingPath, fmt.Errorf("protect task runtime file: %w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return existingPath, fmt.Errorf("write task runtime file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return existingPath, fmt.Errorf("close task runtime file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return existingPath, fmt.Errorf("publish task runtime file: %w", err)
	}
	published = true
	return path, nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func removeTaskRuntimeEnv(path string) {
	if path == "" || filepath.Base(path) != "machine.env" {
		return
	}
	dir := filepath.Dir(path)
	if !strings.HasPrefix(filepath.Base(dir), "cc-connect-task-runtime-") {
		return
	}
	_ = os.RemoveAll(dir)
}
