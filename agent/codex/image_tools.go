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

// Shared by tool schemas and per-turn instructions so resumed conversations receive current semantics.
const imageFramingInstructions = "Tomako image framing: when the user specifies an aspect ratio, express it in size or paired targetWidth/targetHeight pixel dimensions; prompt text alone does not set generation dimensions. Preserving the original result does not mean omitting generation dimensions. Omit resizeMode for complete provider output without cropping or padding; use contain/cover only for an explicitly requested fixed canvas."

const imageSchedulingInstructions = "Choose waiting by dependency, not image count: set waitForResult=false when independent authorized work can proceed while an image generates, including preparing editable text/layout for a single supporting image. Then use tomako_image_status with the accepted id before final composition or delivery. Keep the default wait for an image-only deliverable or when the next step requires the actual image. Submit independent images before waiting; an image that uses another generated result must wait for that result. Pending, failed or unconfirmed results never authorize resubmission or claims of completion."

func imageDynamicTools() []map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	slot := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 1, "description": description}
	}
	return []map[string]any{
		{"type": "function", "name": "tomako_generate_image", "deferLoading": false,
			"description": "Preferred Tomako image generation/edit entry. Follow the shared image policy and edit-image skill; supply the brief and actual selected source references without inspecting scripts or assembling shell commands. One call submits one image. Put the edited source first; preserve the requested scope and framing. Discussion does not authorize generation. For compositing or other unsupported options use the existing shared image helper. This tool does not attach an image to a document. " + imageSchedulingInstructions + " " + imageFramingInstructions,
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false,
				"required": []string{"operation", "prompt", "deliveryRole"}, "properties": map[string]any{
					"operation":          map[string]any{"type": "string", "enum": []string{"create", "edit", "variation"}},
					"prompt":             text,
					"deliveryRole":       map[string]any{"type": "string", "enum": []string{"DELIVERABLE", "SUPPORTING"}, "description": "按用户最终目标声明用途：独立交付的图片用 DELIVERABLE；用于可编辑画布、PPT、文章、视频等最终作品的底图、配图或装饰用 SUPPORTING。中间素材即使先生成成功，也不单独成为成果；不要等最终作品保存后才区分。"},
					"waitForResult":      map[string]any{"type": "boolean", "description": "Defaults to true. Set false when independent authorized work can proceed, even with a single supporting image; return the task id promptly, then wait via tomako_image_status before final composition or delivery. Keep true for an image-only deliverable or when the next step requires this result. Shared source references alone do not make images dependent."},
					"referenceImageUrls": map[string]any{"type": "array", "items": map[string]any{"type": "string", "description": "Actual http(s) image URL from current conversation or relevant brand assets. Never invent a URL."}},
					"referenceImages":    map[string]any{"type": "array", "maxItems": 16, "description": "Ordered selected references, each with exactly one url or absolute path in the current workspace. Put the edited source first. Use this or referenceImageUrls, never both.", "items": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"url": text, "path": text}, "minProperties": 1, "maxProperties": 1}},
					"size":               map[string]any{"type": "string", "description": "Optional provider generation dimensions (WIDTHxHEIGHT). Choose supported dimensions for the intended framing; for edits preserve source framing unless asked to change it. If omitted, the backend resolves the generation size from targetWidth/targetHeight for supported models, otherwise uses its existing default. Results preserve the actual provider image by default."},
					"targetWidth":        map[string]any{"type": "integer", "minimum": 1, "description": "Requested framing width; provide with targetHeight. Without resizeMode this guides generation, not post-generation cropping or an exact delivered-pixel guarantee."},
					"targetHeight":       map[string]any{"type": "integer", "minimum": 1, "description": "Requested framing height; provide with targetWidth. The returned file retains its actual dimensions by default."},
					"resizeMode":         map[string]any{"type": "string", "enum": []string{"cover", "contain"}, "description": "Omit for ordinary generated results: persist the complete provider image. Set only for an explicitly requested fixed canvas with targetWidth/targetHeight: cover crops; contain adds a faded image background. Do not infer a crop from a poster or platform aspect-ratio request."},
					"transparent":        map[string]any{"type": "boolean", "description": "Set true for an asset that will be layered over a canvas, slide, poster or another image (an object, character, prop or sticker): the result is a PNG with a transparent background, so no cutout step is needed. Describe one complete subject with no backdrop or ground shadow. Supported by the default model; omit for full scenes and background plates."},
					"slotId":             text, "slotLabel": text,
					"slotIndex": slot("Position of this image in a planned set, counting from 1: the first image is 1 and the last equals slotCount. Provide with slotCount."),
					"slotCount": slot("How many images the planned set has. Provide with slotIndex."),
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
		if err := snapshotImageReferences(s.workDir, filepath.Dir(path), args, s.sandboxScratchDir); err != nil {
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
func snapshotImageReferences(workDir, snapshotDir string, args map[string]any, fencedScratch string) error {
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
		var data []byte
		if fencedScratch != "" {
			data, err = readFencedWorkspaceImage(workDir, source, fencedScratch)
		} else {
			data, err = readWorkspaceImage(root, rel)
		}
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
	return readImageFile(file)
}

func readImageFile(file *os.File) ([]byte, error) {
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

// A native tool runs outside the shell sandbox and must enforce the same private
// paths. Resolve legitimate local aliases, then atomically refuse every symlink
// while opening the resolved path so a swapped alias cannot bypass this check.
func readFencedWorkspaceImage(workDir, source, scratch string) ([]byte, error) {
	base, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return nil, fmt.Errorf("current image workspace unavailable")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, fmt.Errorf("selected image is unavailable")
	}
	rel, err := filepath.Rel(base, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("selected image is outside the current workspace")
	}
	scratchRel, err := filepath.Rel(workDir, scratch)
	within := func(path, dir string) bool {
		return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
	}
	if err != nil || within(rel, ".codex") && !within(rel, filepath.Join(".codex", "memories")) ||
		within(rel, ".tmp") && !within(rel, scratchRel) {
		return nil, fmt.Errorf("selected image is outside the current task scope")
	}
	file, err := openImageWithoutSymlinks(base, rel)
	if err != nil {
		return nil, fmt.Errorf("selected image is unavailable or changed")
	}
	defer file.Close()
	return readImageFile(file)
}
