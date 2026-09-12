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

	"github.com/chenhg5/cc-connect/core"
)

func (s *appServerSession) imageToolScript() string {
	for _, entry := range core.MergeEnv(os.Environ(), s.extraEnv) {
		if root, ok := strings.CutPrefix(entry, "SKILLS_OL_DIR="); ok && root != "" {
			path := filepath.Join(root, "tomako-image-tool.mjs")
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path
			}
		}
	}
	return ""
}

func imageRuntimeAuthorized(runtime core.SessionRuntime) bool {
	return runtime.TaskID != "" && runtime.WorkspaceID != "" && runtime.BrandID != "" &&
		(strings.TrimSpace(runtime.ImageCapabilityToken) != "" || strings.TrimSpace(runtime.MachineCapabilityToken) != "")
}

func (s *appServerSession) imageToolsAvailable() bool {
	s.runtimeMu.RLock()
	authorized := imageRuntimeAuthorized(s.runtime)
	s.runtimeMu.RUnlock()
	return authorized && s.imageToolScript() != ""
}

func imageDynamicTools() []map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	positive := map[string]any{"type": "integer", "minimum": 1}
	return []map[string]any{
		{"type": "function", "name": "tomako_generate_image", "deferLoading": false,
			"description": "Preferred Tomako image generation/edit entry. Follow the shared image policy and edit-image skill; supply the brief and actual hosted source URL(s), without inspecting scripts or assembling shell commands. One call submits one image and waits for its result. Put the edited source first; preserve the requested scope and framing. Discussion does not authorize generation. A failed, pending or unconfirmed result does not authorize another submission. For local-only sources, compositing or other unsupported options use the existing shared image helper. This tool does not attach an image to a document.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"operation", "prompt", "referenceImageUrls"}, "properties": map[string]any{
					"operation":          map[string]any{"type": "string", "enum": []string{"create", "edit", "variation"}},
					"prompt":             text,
					"referenceImageUrls": map[string]any{"type": "array", "items": map[string]any{"type": "string", "description": "Actual http(s) image URL from current conversation or relevant brand assets. Never invent a URL."}},
					"size":               map[string]any{"type": "string", "description": "Optional provider generation size. Omit to retain the existing default; exact delivered dimensions belong in targetWidth/targetHeight."},
					"targetWidth":        positive, "targetHeight": positive,
					"resizeMode": map[string]any{"type": "string", "enum": []string{"cover", "contain"}},
					"slotId":     text, "slotLabel": text, "slotIndex": positive, "slotCount": positive,
				}},
		},
		{"type": "function", "name": "tomako_image_status", "deferLoading": false,
			"description": "Read/wait for an existing imageTaskId from this current task. Does not generate or charge for a new image. Use after a pending result, never resubmit to poll. A stopped/older task remains visible in Studio; this tool does not expand authority to a different task.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"imageTaskId"},
				"properties": map[string]any{"imageTaskId": map[string]any{"type": "string", "pattern": "^img-[A-Za-z0-9_-]+$"}}},
		},
	}
}

// Snapshot the authenticated runtime before starting asynchronous work. A later
// turn may rotate the session file; it must never change this operation's owner.
func (s *appServerSession) prepareImageTool(tool string, arguments map[string]any) (*exec.Cmd, context.CancelFunc, func(), error) {
	if tool != "tomako_generate_image" && tool != "tomako_image_status" {
		return nil, nil, nil, fmt.Errorf("unknown image tool")
	}
	script := s.imageToolScript()
	s.runtimeMu.RLock()
	runtime := s.runtime
	s.runtimeMu.RUnlock()
	if script == "" || !imageRuntimeAuthorized(runtime) {
		return nil, nil, nil, fmt.Errorf("image tool unavailable for this task")
	}
	encoded, err := json.Marshal(map[string]any{"tool": tool, "arguments": arguments})
	if err != nil || len(encoded) > 64*1024 {
		return nil, nil, nil, fmt.Errorf("invalid image arguments")
	}
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	cmd := exec.CommandContext(ctx, "node", script)
	cmd.Dir = s.workDir
	cmd.Env = core.MergeEnv(os.Environ(), append(append([]string(nil), s.extraEnv...), "TOMAKO_TASK_ENV_FILE="+path))
	cmd.Stdin = bytes.NewReader(encoded)
	return cmd, cancel, func() { removeTaskRuntimeEnv(path) }, nil
}

// The adapter emits the accepted receipt immediately, then a final receipt.
// Retain the former even if waiting is interrupted; never turn a lost wait into
// a new generation. No model-selected local paths are read outside Codex's fence.
func runImageToolCommand(cmd *exec.Cmd) (string, error) {
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// Only structured stdout is returned. Provider stderr must not leak credentials.
	err := cmd.Run()
	if stdout.Len() > 256*1024 {
		return "", fmt.Errorf("image tool receipt exceeds size limit; do not resubmit")
	}
	var last map[string]any
	for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
		var value map[string]any
		if json.Unmarshal(line, &value) == nil && value["code"] != nil {
			last = value
		}
	}
	if last == nil {
		return "", fmt.Errorf("image tool returned no receipt; submission may already exist, do not resubmit")
	}
	if err != nil && last["data"] != nil {
		last["waiting"] = "interrupted"
		last["message"] = "Waiting ended. Preserve any returned imageTaskId; do not submit another image."
	}
	result, _ := json.Marshal(last)
	return string(result), nil
}
