package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestUpdateTaskRuntimeEnvWritesProtectedReusableFile(t *testing.T) {
	runtime := core.SessionRuntime{
		TaskID:                   "llm-task-1",
		MachineCapabilityToken:   "machine-token",
		TaskAuthorityEnvelopeB64: "authority-envelope",
	}
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil {
		t.Fatalf("updateTaskRuntimeEnv: %v", err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(path) })

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat task runtime file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("task runtime mode = %o, want 600", got)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read task runtime file: %v", err)
	}
	text := string(body)
	for _, expected := range []string{
		"export MACHINE_CAPABILITY_TOKEN='machine-token'",
		"export IMAGE_CAPABILITY_TOKEN='machine-token'",
		"export TOMAKO_TASK_AUTHORITY_ENVELOPE_B64='authority-envelope'",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("task runtime file missing %q: %q", expected, text)
		}
	}

	updated := runtime
	updated.MachineCapabilityToken = "rotated-token"
	updatedPath, err := updateTaskRuntimeEnv(path, updated)
	if err != nil {
		t.Fatalf("rotate task runtime env: %v", err)
	}
	if updatedPath != path {
		t.Fatalf("rotated path = %q, want stable %q", updatedPath, path)
	}
	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read rotated task runtime file: %v", err)
	}
	if strings.Contains(string(body), "machine-token") ||
		!strings.Contains(string(body), "rotated-token") {
		t.Fatalf("task runtime file was not atomically rotated: %q", body)
	}
	if filepath.Base(path) != "machine.env" {
		t.Fatalf("task runtime basename = %q", filepath.Base(path))
	}
}

func TestUpdateTaskRuntimeEnvRejectsIncompleteAuthority(t *testing.T) {
	_, err := updateTaskRuntimeEnv("", core.SessionRuntime{
		TaskID:                 "llm-task-1",
		MachineCapabilityToken: "machine-token",
	})
	if err == nil || !strings.Contains(err.Error(), "must be supplied together") {
		t.Fatalf("incomplete authority error = %v", err)
	}
}

func TestThreadParamsExposeOnlyTaskRuntimeFilePath(t *testing.T) {
	s := &appServerSession{
		workDir:            "/srv/tomako",
		taskRuntimeEnvFile: "/tmp/cc-connect-task-runtime-safe/machine.env",
	}
	params := s.threadRequestParams()
	config := params["config"].(map[string]any)
	if got := config["shell_environment_policy.set.TOMAKO_TASK_ENV_FILE"]; got != s.taskRuntimeEnvFile {
		t.Fatalf("task runtime env config = %#v", config)
	}
	for key, value := range config {
		if strings.Contains(key, "CAPABILITY") || strings.Contains(strings.TrimSpace(strings.ReplaceAll(key, "TOMAKO_TASK_ENV_FILE", "")), "AUTHORITY") {
			t.Fatalf("secret unexpectedly placed in app-server config: %s=%v", key, value)
		}
	}
}

// Loaded app-server threads ignore subsequent resume config overrides. Capture
// the FIRST resume request and verify that every turn uses that original binding.
func TestResumedThreadToolsReceiveRotatingAuthorityWithoutReResume(t *testing.T) {
	testResumedThreadAuthority(t, "")
}

func TestFencedResumedThreadAuthorityStaysInReadOnlyBrandDirectory(t *testing.T) {
	testResumedThreadAuthority(t, "tomako-brand-fence")
}

func testResumedThreadAuthority(t *testing.T, permissionsProfile string) {
	t.Helper()
	workDir := t.TempDir()
	requestsFile := filepath.Join(workDir, "requests.jsonl")
	shellScript := `#!/bin/sh
printf '%s' "$TOMAKO_TASK_ENV_FILE" > "$CC_TEST_RUNTIME_REQUESTS.env"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CC_TEST_RUNTIME_REQUESTS"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) result='{}' ;;
    *'"method":"thread/resume"'*) result='{"thread":{"id":"thread-existing"}}' ;;
    *'"method":"account/rateLimits/read"'*) result='{}' ;;
    *'"method":"turn/start"'*) result='{"turn":{"id":"test-turn"}}' ;;
    *) continue ;;
  esac
  printf '{"id":%s,"result":%s}\n' "$id" "$result"
done
`
	powershellScript := `
[System.IO.File]::WriteAllText($env:CC_TEST_RUNTIME_REQUESTS + '.env', [string]$env:TOMAKO_TASK_ENV_FILE)
while (($line = [Console]::In.ReadLine()) -ne $null) {
  [System.IO.File]::AppendAllText($env:CC_TEST_RUNTIME_REQUESTS, $line + [Environment]::NewLine)
  $request = $line | ConvertFrom-Json
  switch ($request.method) {
    'initialize' { $result = '{}' }
    'thread/resume' { $result = '{"thread":{"id":"thread-existing"}}' }
    'account/rateLimits/read' { $result = '{}' }
    'turn/start' { $result = '{"turn":{"id":"test-turn"}}' }
    default { continue }
  }
  [Console]::Out.WriteLine('{"id":' + $request.id + ',"result":' + $result + '}')
}
`
	writeFakeCodexScript(t, workDir, shellScript, powershellScript)
	t.Setenv("PATH", workDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CC_TEST_RUNTIME_REQUESTS", requestsFile)
	t.Setenv("TOMAKO_TASK_ENV_FILE", filepath.Join(workDir, "stale-authority.env"))
	var extraEnv []string
	if permissionsProfile != "" {
		var err error
		extraEnv, err = prepareFencedEnvironment(workDir, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := newAppServerSession(context.Background(), "", workDir, "test-model", "low", "", permissionsProfile, "thread-existing", "", "", extraEnv, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	readRequests := func() []struct {
		Method string
		Params map[string]any
	} {
		body, err := os.ReadFile(requestsFile)
		if err != nil {
			t.Fatal(err)
		}
		var requests []struct {
			Method string
			Params map[string]any
		}
		for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
			var request struct {
				Method string
				Params map[string]any
			}
			if err := json.Unmarshal([]byte(line), &request); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, request)
		}
		return requests
	}
	var boundPath string
	for _, request := range readRequests() {
		if request.Method == "thread/resume" {
			config := request.Params["config"].(map[string]any)
			boundPath, _ = config["shell_environment_policy.set.TOMAKO_TASK_ENV_FILE"].(string)
		}
	}
	if boundPath == "" {
		t.Fatal("first resume did not bind the tool authority file; a second resume cannot repair a loaded thread")
	}
	processEnv, err := os.ReadFile(requestsFile + ".env")
	if err != nil || string(processEnv) != boundPath {
		t.Fatal("app-server child tools do not inherit the same authority path as shell tools")
	}
	if permissionsProfile != "" && filepath.Dir(filepath.Dir(boundPath)) != filepath.Join(workDir, ".codex") {
		t.Fatal("authority file is outside the brand's read-only .codex mount and is invisible inside the filesystem fence")
	}
	assertBoundContents := func(expected string) {
		body, err := os.ReadFile(boundPath)
		if err != nil {
			t.Fatal(err)
		}
		if expected == "" {
			if len(body) != 0 {
				t.Fatal("unscoped turn retained prior authority")
			}
		} else if !strings.Contains(string(body), "export MACHINE_CAPABILITY_TOKEN='"+expected+"'") {
			t.Fatal("tool's original shell environment cannot resolve current turn authority")
		}
		if s.currentTaskRuntimeEnvFile() != boundPath {
			t.Fatal("file path changed after thread config was frozen")
		}
	}
	assertBoundContents("")
	for _, token := range []string{"first-token", "second-token", "", "restored-token"} {
		var runtime core.SessionRuntime
		if token != "" {
			wire := `{"task_id":"llm-task-1","machine_capability_token":"` + token + `","task_authority_envelope_b64":"test-envelope"}`
			if err := json.Unmarshal([]byte(wire), &runtime); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SetSessionRuntime(runtime); err != nil {
			t.Fatal(err)
		}
		if err := s.Send("Check current value without modifying it", nil, nil); err != nil {
			t.Fatal(err)
		}
		assertBoundContents(token)
		if s.CurrentSessionID() != "thread-existing" {
			t.Fatal("authority rotation lost conversation history")
		}
	}
	resumes, turns := 0, 0
	for _, request := range readRequests() {
		switch request.Method {
		case "thread/resume":
			resumes++
		case "thread/start":
			t.Fatal("authority rotation replaced the history thread")
		case "turn/start":
			turns++
		}
	}
	if resumes != 1 || turns != 4 {
		t.Fatalf("got %d resumes and %d turns, want 1 and 4", resumes, turns)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(boundPath)); !os.IsNotExist(err) {
		t.Fatal("closing session did not remove its authority directory")
	}
}
