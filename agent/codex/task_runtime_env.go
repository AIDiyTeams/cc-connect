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
	content, err := taskRuntimeEnvContent(runtime)
	if err != nil {
		return existingPath, err
	}
	if existingPath == "" && content == "" {
		return "", nil
	}
	return writeTaskRuntimeEnv(existingPath, content)
}

func taskRuntimeEnvContent(runtime core.SessionRuntime) (string, error) {
	token := strings.TrimSpace(runtime.MachineCapabilityToken)
	imageToken := strings.TrimSpace(runtime.ImageCapabilityToken)
	documentToken := strings.TrimSpace(runtime.DocumentCapabilityToken)
	employeeToken := strings.TrimSpace(runtime.EmployeeCommandCapabilityToken)
	productToken := strings.TrimSpace(runtime.ProductUpdateCapabilityToken)
	envelope := strings.TrimSpace(runtime.TaskAuthorityEnvelopeB64)
	if token == "" && imageToken == "" && documentToken == "" && employeeToken == "" && productToken == "" && envelope == "" {
		return "", nil
	}
	if (token == "") != (envelope == "") {
		return "", fmt.Errorf("machine capability and task authority envelope must be supplied together")
	}
	if imageToken == "" {
		imageToken = token
	}
	taskID := strings.TrimSpace(runtime.TaskID)
	if !taskRuntimeIDPattern.MatchString(taskID) {
		return "", fmt.Errorf("valid task id is required for machine authority")
	}
	for label, value := range map[string]string{
		"machine capability":          token,
		"image capability":            imageToken,
		"document capability":         documentToken,
		"employee command capability": employeeToken,
		"product update capability":   productToken,
		"task authority envelope":     envelope,
	} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", fmt.Errorf("%s contains forbidden control characters", label)
		}
	}

	for _, value := range []string{runtime.WorkspaceID, runtime.BrandID} {
		if value != "" && !taskRuntimeIDPattern.MatchString(value) {
			return "", fmt.Errorf("invalid task runtime scope")
		}
	}
	if token == "" && (runtime.WorkspaceID == "" || runtime.BrandID == "") {
		return "", fmt.Errorf("image authority requires workspace and brand scope")
	}
	lines := []string{
		"export MACHINE_CAPABILITY_TOKEN=" + shellSingleQuote(token),
		"export IMAGE_CAPABILITY_TOKEN=" + shellSingleQuote(imageToken),
		"export TOMAKO_TASK_AUTHORITY_ENVELOPE_B64=" + shellSingleQuote(envelope),
		"export TASK_AUTHORITY_ENVELOPE_B64=" + shellSingleQuote(envelope),
		"export TOMAKO_TASK_ID=" + shellSingleQuote(taskID),
		"export TOMAKO_WORKSPACE_ID=" + shellSingleQuote(runtime.WorkspaceID),
		"export TOMAKO_BRAND_ID=" + shellSingleQuote(runtime.BrandID),
		"",
	}
	if employeeToken != "" {
		lines = append(lines, "export EMPLOYEE_COMMAND_CAPABILITY_TOKEN="+shellSingleQuote(employeeToken))
	}
	if documentToken != "" {
		lines = append(lines, "export DOCUMENT_CAPABILITY_TOKEN="+shellSingleQuote(documentToken))
	}
	if productToken != "" {
		lines = append(lines, "export PRODUCT_UPDATE_CAPABILITY_TOKEN="+shellSingleQuote(productToken))
	}
	return strings.Join(lines, "\n"), nil
}

func writeTaskRuntimeEnv(existingPath, content string) (string, error) {
	return writeTaskRuntimeEnvInDir(existingPath, content, "")
}

// The brand fence hides host /tmp. Stage authority under its read-only .codex
// mount, never under writable outputs or memories, without widening the fence.
func createTaskRuntimeEnv(workDir, permissionsProfile string) (string, error) {
	var dir string
	if strings.TrimSpace(permissionsProfile) != "" {
		dir = filepath.Join(workDir, ".codex")
		if err := ensureFencedPrivateDir(dir, 0o700); err != nil {
			return "", err
		}
	}
	return writeTaskRuntimeEnvInDir("", "", dir)
}

func writeTaskRuntimeEnvInDir(existingPath, content, tempDir string) (string, error) {
	path := existingPath
	if path == "" {
		dir, err := os.MkdirTemp(tempDir, "cc-connect-task-runtime-")
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
