package codex

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func imageToolTestSession(t *testing.T, script string) *appServerSession {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tomako-image-tool.mjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &appServerSession{ctx: ctx, cancel: cancel, workDir: dir, extraEnv: []string{"SKILLS_OL_DIR=" + dir}, events: make(chan core.Event, 1), runtime: core.SessionRuntime{TaskID: "cmsg-a", WorkspaceID: "ws-a", BrandID: "b-a", ImageCapabilityToken: "image-a"}}
	s.alive.Store(true)
	return s
}

func TestImageToolFramingDoesNotImplyCropping(t *testing.T) {
	schema := imageDynamicTools()[0]["inputSchema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	size := properties["size"].(map[string]any)["description"].(string)
	mode := properties["resizeMode"].(map[string]any)["description"].(string)
	if strings.Contains(size, "exact delivered dimensions belong") || !strings.Contains(size, "actual provider image") {
		t.Fatal("generation dimensions must not direct ordinary results into fixed canvas cropping")
	}
	if !strings.Contains(mode, "explicitly requested fixed canvas") || !strings.Contains(mode, "cover crops") {
		t.Fatal("image tool must explain explicit resizing and its effect")
	}
	if strings.Contains(strings.Join(schema["required"].([]string), ","), "size") {
		t.Fatal("supplier-specific size must not be mandatory in the bridge")
	}
	transparent := properties["transparent"].(map[string]any)
	if transparent["type"] != "boolean" || !strings.Contains(transparent["description"].(string), "transparent background") {
		t.Fatal("layered assets must be requestable with a transparent background")
	}
	if strings.Contains(strings.Join(schema["required"].([]string), ","), "transparent") {
		t.Fatal("transparency is optional; full scenes keep their backdrop")
	}
}

// A zero-based first slot used to be refused without saying why, so the Agent guessed.
func TestImageToolSlotsSayTheyCountFromOne(t *testing.T) {
	properties := imageDynamicTools()[0]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	index := properties["slotIndex"].(map[string]any)
	if index["minimum"] != 1 || !strings.Contains(index["description"].(string), "counting from 1") {
		t.Fatalf("slotIndex must state that the first image is 1: %#v", index)
	}
	if count := properties["slotCount"].(map[string]any); count["minimum"] != 1 || count["description"] == "" {
		t.Fatalf("slotCount must be described: %#v", count)
	}
}

func TestImageToolsOnlyAdvertisedWithAuthorityAndInstalledAdapter(t *testing.T) {
	s := imageToolTestSession(t, "")
	if tools, ok := s.threadRequestParams()["dynamicTools"].([]map[string]any); !ok || len(tools) != 2 {
		t.Fatalf("tools=%#v", tools)
	} else {
		schema := tools[0]["inputSchema"].(map[string]any)
		wait := schema["properties"].(map[string]any)["waitForResult"].(map[string]any)
		if wait["type"] != "boolean" {
			t.Fatal("image tool must advertise optional submission-only mode")
		}
	}
	s.runtime.ImageCapabilityToken = ""
	if s.threadRequestParams()["dynamicTools"] != nil {
		t.Fatal("unscoped task received image tools")
	}
	s.runtime.ImageCapabilityToken = "image-a"
	s.runtime.Scene = "brand_analysis"
	for _, tool := range s.threadRequestParams()["dynamicTools"].([]map[string]any) {
		if strings.HasPrefix(tool["name"].(string), "tomako_image") {
			t.Fatal("dedicated analysis tool set changed")
		}
	}
	s.runtime.Scene = ""
	if err := os.Remove(filepath.Join(s.workDir, "tomako-image-tool.mjs")); err != nil {
		t.Fatal(err)
	}
	if s.threadRequestParams()["dynamicTools"] != nil {
		t.Fatal("missing adapter broke legacy fallback")
	}
}

func TestImageToolSnapshotsAuthorityAndTreatsPromptAsData(t *testing.T) {
	s := imageToolTestSession(t, "")
	prompt := "只改这一行\n'$(touch must-not-exist)`literal`"
	cmd, cancel, cleanup, err := s.prepareImageTool("tomako_generate_image", map[string]any{"prompt": prompt, "waitForResult": false})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer cleanup()
	s.runtime.TaskID = "cmsg-next"
	s.runtime.ImageCapabilityToken = "next-token"
	var file string
	for _, entry := range cmd.Env {
		if path, ok := strings.CutPrefix(entry, "TOMAKO_TASK_ENV_FILE="); ok {
			file = path
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "cmsg-a") || strings.Contains(string(data), "next-token") {
		t.Fatal("running call adopted a later turn's authority")
	}
	input, _ := io.ReadAll(cmd.Stdin)
	var call map[string]any
	if json.Unmarshal(input, &call) != nil {
		t.Fatal("invalid structured input")
	}
	if call["arguments"].(map[string]any)["prompt"] != prompt {
		t.Fatal("prompt changed during command construction")
	}
	if call["arguments"].(map[string]any)["waitForResult"] != false {
		t.Fatal("submission-only request changed during command construction")
	}
	if strings.Contains(string(input), "image-a") || strings.Contains(string(input), "cmsg-a") {
		t.Fatal("authority exposed in arguments")
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != filepath.Join(s.workDir, "tomako-image-tool.mjs") {
		t.Fatalf("unexpected shell execution: %v", cmd.Args)
	}
}

func TestImageToolAuthorityRotationPreservesExistingConversation(t *testing.T) {
	s := imageToolTestSession(t, "")
	s.threadID.Store("existing-thread")
	runtime := s.runtime
	runtime.TaskID = "cmsg-next"
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(s.taskRuntimeEnvFile) })
	if s.CurrentSessionID() != "existing-thread" {
		t.Fatal("adding image support reset conversation history")
	}
	runtime.ImageCapabilityToken = ""
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.prepareImageTool("tomako_generate_image", map[string]any{}); err == nil {
		t.Fatal("revoked task still executes image tool")
	}
}

func TestImageToolStopCancelsWaitAndPreservesAcceptedReceipt(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required for adapter process test")
	}
	s := imageToolTestSession(t, `import {writeFileSync} from 'node:fs';
console.log(JSON.stringify({code:0,data:{imageTaskId:'img-accepted',status:'PENDING'}}));
writeFileSync('ready','1');
setTimeout(()=>{},30000);`)
	cmd, cancel, cleanup, err := s.prepareImageTool("tomako_generate_image", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer cleanup()
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() { text, err := runImageToolCommand(cmd); done <- result{text, err} }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(s.workDir, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("adapter did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || !strings.Contains(got.text, "img-accepted") || !strings.Contains(got.text, "interrupted") {
			t.Fatalf("receipt lost after stop: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop left image adapter running")
	}
}

func TestImageToolLocalReferencesStayWithinWorkspaceAndPreserveOrder(t *testing.T) {
	s := imageToolTestSession(t, "")
	source := filepath.Join(s.workDir, "source.png")
	if err := os.WriteFile(source, []byte("selected image bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"referenceImages": []any{map[string]any{"path": source}, map[string]any{"url": "https://example.com/style.png"}}}
	cmd, cancel, cleanup, err := s.prepareImageTool("tomako_generate_image", args)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	defer cleanup()
	var call map[string]any
	input, _ := io.ReadAll(cmd.Stdin)
	if json.Unmarshal(input, &call) != nil {
		t.Fatal("invalid input")
	}
	refs := call["arguments"].(map[string]any)["referenceImages"].([]any)
	staged := refs[0].(map[string]any)["path"].(string)
	if staged == source || !strings.Contains(staged, "cc-connect-task-runtime-") {
		t.Fatal("source was not privately staged")
	}
	data, err := os.ReadFile(staged)
	if err != nil || string(data) != "selected image bytes" {
		t.Fatal("source bytes changed")
	}
	if refs[1].(map[string]any)["url"] != "https://example.com/style.png" {
		t.Fatal("reference order changed")
	}
	if args["referenceImages"].([]any)[0].(map[string]any)["path"] != source {
		t.Fatal("mutated RPC arguments")
	}
	outside := filepath.Join(t.TempDir(), "other.png")
	if err := os.WriteFile(outside, []byte("other brand"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.workDir, "escape.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, link, s.workDir} {
		if _, _, _, err := s.prepareImageTool("tomako_generate_image", map[string]any{"referenceImages": []any{map[string]any{"path": path}}}); err == nil {
			t.Fatalf("accepted invalid or escaping source %s", path)
		}
	}
}
