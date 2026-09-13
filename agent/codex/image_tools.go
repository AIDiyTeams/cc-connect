package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
			"description": "Preferred Tomako image generation/edit entry. Follow the shared image policy and edit-image skill; supply the brief and actual selected source references without inspecting scripts or assembling shell commands. One call submits one image. For multiple independent images, set waitForResult=false on each and submit them before waiting with tomako_image_status. If an image depends on a previous result, wait for that result before submitting it. Put the edited source first; preserve the requested scope and framing. Discussion does not authorize generation. A failed, pending or unconfirmed result does not authorize resubmitting that image. For compositing or other unsupported options use the existing shared image helper. This tool does not attach an image to a document.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"operation", "prompt", "size"}, "properties": map[string]any{
					"operation":          map[string]any{"type": "string", "enum": []string{"create", "edit", "variation"}},
					"prompt":             text,
					"waitForResult":      map[string]any{"type": "boolean", "description": "Defaults to true. Set false for independent images in an authorized multi-image request: return the accepted task id promptly, submit the remaining independent images, then wait via tomako_image_status. Shared source references alone do not make images dependent. Keep true for a single image or when the next image requires this result."},
					"referenceImageUrls": map[string]any{"type": "array", "items": map[string]any{"type": "string", "description": "Actual http(s) image URL from current conversation or relevant brand assets. Never invent a URL."}},
					"referenceImages":    map[string]any{"type": "array", "maxItems": 16, "description": "Ordered selected references, each with exactly one url or absolute path in the current workspace. Put the edited source first. Use this or referenceImageUrls, never both.", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"url": text, "path": text}, "minProperties": 1, "maxProperties": 1}},
					"size":               map[string]any{"type": "string", "description": "Provider generation WIDTHxHEIGHT. Current default Flare VIP supports custom dimensions: multiples of 16, each <=3840, aspect ratio 1:3..3:1, total pixels 655360..8294400. Choose the user's intended aspect ratio; for edits preserve the source framing unless asked to change it. Prefer about 1 megapixel unless higher resolution is requested: square 1024x1024, portrait 864x1152 (3:4), landscape 1152x864 (4:3), 768x1344 (4:7). Exact delivered dimensions belong in targetWidth/targetHeight; they do not set generation size. Do not use auto or silently use a square for a non-square request. No capability lookup is needed."},
					"targetWidth":        positive, "targetHeight": positive,
					"resizeMode": map[string]any{"type": "string", "enum": []string{"cover", "contain"}, "description": "Final sizing only. Different aspect ratios: cover crops; contain adds a faded image background and may repeat text. Match generation and target ratios to avoid these effects when preserving framing. Neither mode guarantees unchanged content."},
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
	// Work on a private copy: RPC inputs and later turns retain their original paths.
	var call map[string]any
	_ = json.Unmarshal(encoded, &call)
	if args, ok := call["arguments"].(map[string]any); ok {
		if err := snapshotImageReferences(s.workDir, filepath.Dir(path), args); err != nil {
			removeTaskRuntimeEnv(path)
			return nil, nil, nil, err
		}
	}
	encoded, _ = json.Marshal(call)
	if len(encoded) > 64*1024 {
		removeTaskRuntimeEnv(path)
		return nil, nil, nil, fmt.Errorf("staged image arguments exceed size limit")
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
// a new generation. Local references are confined by os.Root before this command.
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

// os.Root prevents traversal and symlink escapes, including concurrent symlink
// replacement. Copy bounded regular files to the private task snapshot so the
// existing Node uploader never opens an unconfined model-selected path.
func snapshotImageReferences(workDir, snapshotDir string, args map[string]any) error {
	value, exists := args["referenceImages"]
	if !exists {
		return nil
	}
	refs, ok := value.([]any)
	if !ok || len(refs) > 16 {
		return fmt.Errorf("referenceImages must contain at most 16 selected references")
	}
	if _, mixed := args["referenceImageUrls"]; mixed {
		return fmt.Errorf("select only one reference input format")
	}
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return fmt.Errorf("current image workspace unavailable")
	}
	defer root.Close()
	for i, value := range refs {
		ref, ok := value.(map[string]any)
		if !ok || len(ref) != 1 {
			return fmt.Errorf("each reference must have exactly one url or path")
		}
		input, local := ref["path"]
		if !local {
			continue
		}
		source, ok := input.(string)
		if !ok || !filepath.IsAbs(source) {
			return fmt.Errorf("image source must be an absolute workspace path")
		}
		rel, err := filepath.Rel(workDir, source)
		if err != nil {
			return fmt.Errorf("image source is outside the current workspace")
		}
		data, err := readWorkspaceImage(root, rel)
		if err != nil {
			return err
		}
		dest := filepath.Join(snapshotDir, fmt.Sprintf("reference-%d.image", i))
		if err := os.WriteFile(dest, data, 0600); err != nil {
			return fmt.Errorf("stage selected image: %w", err)
		}
		ref["path"] = dest
	}
	return nil
}

func readWorkspaceImage(root *os.Root, path string) ([]byte, error) {
	file, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("selected image is unavailable or outside the current workspace")
	}
	defer file.Close()
	info, err := file.Stat()
	const maxBytes = 12 * 1024 * 1024
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("selected image must be a nonempty regular file no larger than 12 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxBytes {
		return nil, fmt.Errorf("selected image changed or could not be read")
	}
	return data, nil
}
