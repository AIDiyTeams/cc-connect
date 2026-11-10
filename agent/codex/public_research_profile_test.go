package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPublicResearchProfileTransitionAfterProcessRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake app-server")
	}
	for _, tc := range []struct {
		name, previousBase, nextProfile string
		wantFresh                       bool
	}{
		{"review keeps research history", publicResearchBaseInstructions, core.ToolProfilePublicResearch, false},
		{"selection restores execution", publicResearchBaseInstructions, "", true},
		{"legacy research restores execution", strings.TrimPrefix(publicResearchBaseInstructions, publicResearchProfileMarker+"\n"), "", true},
		{"enter research", "ordinary application instructions", core.ToolProfilePublicResearch, true},
		{"ordinary resume unchanged", "ordinary application instructions", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			requests := filepath.Join(workDir, "requests.jsonl")
			rollout := filepath.Join(workDir, "rollout.jsonl")
			meta, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"base_instructions": map[string]any{"text": tc.previousBase}}})
			if err := os.WriteFile(rollout, append(meta, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
REQUESTS=` + shellSingleQuote(requests) + `
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$REQUESTS"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) result='{}' ;;
    *'"method":"thread/start"'*) result='{"thread":{"id":"thread-current-tools"}}' ;;
    *'"method":"thread/resume"'*) result='{"thread":{"id":"thread-previous","path":` + mustJSON(rollout) + `}}' ;;
    *'"method":"account/rateLimits/read"'*) result='{}' ;;
    *) continue ;;
  esac
  printf '{"id":%s,"result":%s}\n' "$id" "$result"
done
`
			writeFakeCodexScript(t, workDir, script, "")
			t.Setenv("PATH", workDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			startup := cmoRuntime()
			startup.ToolProfile = tc.nextProfile
			startup.DeveloperInstructions = "Current Portal conversation and product context"
			s, err := newAppServerSession(context.Background(), "", workDir, "test-model", "low", "", "", "thread-previous", "", "", nil, "", startup)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			wantID := "thread-previous"
			if tc.wantFresh {
				wantID = "thread-current-tools"
			}
			if s.CurrentSessionID() != wantID {
				t.Fatalf("current thread = %s, want %s", s.CurrentSessionID(), wantID)
			}
			body, err := os.ReadFile(requests)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(body), `"method":"thread/start"`); got != tc.wantFresh {
				t.Fatalf("replacement thread = %v, want %v", got, tc.wantFresh)
			}
		})
	}
}

func TestPublicResearchProfileLimitsToolsAndContextWithoutChangingFences(t *testing.T) {
	s := webReadTestSession(t, "")
	writeWebSearchConfig(t, "DEEPSEEK_API_KEY=ds\n")
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	s.runtime.DeveloperInstructions = "Verify source evidence"
	s.runtime.ImageCapabilityToken = "read-only-capability"
	s.runtime.CmoAPI = "https://example.test/team"
	s.developerInstructionsManaged = true
	s.permissionsProfile = "brand-fence"
	s.webSearch = "live"
	params := s.threadRequestParams()
	tools := params["dynamicTools"].([]map[string]any)
	if len(tools) != 2 || tools[0]["name"] != webSearchToolName || tools[1]["name"] != webReadToolName {
		t.Fatalf("research received workbench tools: %#v", tools)
	}
	config := params["config"].(map[string]any)
	for _, key := range []string{"skills.include_instructions", "features.shell_tool", "features.unified_exec", "features.multi_agent", "features.multi_agent_v2", "features.view_image", "features.goals", "features.default_mode_request_user_input", "tools.experimental_request_user_input.enabled", "tools.update_plan.enabled"} {
		if config[key] != false {
			t.Errorf("%s remains enabled", key)
		}
	}
	if config["project_doc_max_bytes"] != 0 || config["web_search"] != "disabled" || params["permissions"] != "brand-fence" || params["approvalPolicy"] != "never" {
		t.Fatal("research lost its bounded context or existing permission fence")
	}
	base := params["baseInstructions"].(string)
	if !strings.Contains(base, "untrusted evidence") || strings.Contains(base, "coding agent") || len(base) > 2400 {
		t.Fatal("research base is not focused or lost its trust boundary")
	}
	policy := s.turnDeveloperInstructions()["settings"].(map[string]any)["developer_instructions"].(string)
	if strings.Contains(policy, imageFramingInstructions) || strings.Contains(policy, teamCoordinationHelperFallback) || !strings.Contains(policy, "Verify source evidence") {
		t.Fatal("research inherited image/team framing or lost current instructions")
	}
	s.runtime.ToolProfile = ""
	ordinary := s.threadRequestParams()["config"].(map[string]any)
	if _, exists := ordinary["skills.include_instructions"]; exists || ordinary["tools.update_plan.enabled"] != true || s.unattended() {
		t.Fatal("research settings leaked into ordinary conversation")
	}
}

func TestPublicResearchRetainsReviewHistoryAndResetsForUserExecution(t *testing.T) {
	s := webReadTestSession(t, "")
	runtime := s.runtime
	runtime.ToolProfile = core.ToolProfilePublicResearch
	runtime.DeveloperInstructions = "research"
	s.threadID.Store("ordinary")
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSessionID() != "" {
		t.Fatal("research reused ordinary tools")
	}
	s.threadID.Store("research-history")
	runtime.TaskID = "review"
	runtime.DeveloperInstructions = "review original evidence"
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSessionID() != "research-history" {
		t.Fatal("review lost original evidence history")
	}
	runtime.ToolProfile = ""
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if s.CurrentSessionID() != "" {
		t.Fatal("user execution inherited research-only tools")
	}
	runtime.ToolProfile = "unknown"
	if err := s.SetSessionRuntime(runtime); err == nil {
		t.Fatal("unknown profile silently accepted")
	}
	if s.runtime.ToolProfile != "" {
		t.Fatal("rejected profile mutated runtime")
	}
}

func TestPublicResearchRefusesUnadvertisedActionsEvenIfModelRequestsOne(t *testing.T) {
	s := webReadTestSession(t, "")
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	s.stdin = writer
	s.handleDynamicToolCall(json.RawMessage(`7`), json.RawMessage(`{"tool":"tomako_team_coordination","arguments":{"operation":"handoff"}}`))
	var response struct {
		Result struct {
			Success bool `json:"success"`
		} `json:"result"`
	}
	if err := json.NewDecoder(reader).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Result.Success || len(s.events) != 0 {
		t.Fatal("research executed or emitted a workbench action")
	}
}

func TestPublicResearchCatalogRemovesPatchAndShellWithoutChangingModelOrReasoning(t *testing.T) {
	runtime := core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash-vision-exp", ToolProfile: core.ToolProfilePublicResearch}
	envFile := filepath.Join(t.TempDir(), "runtime.env")
	path, err := writeModelCatalog(envFile, runtime)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err = json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	model := catalog.Models[0]
	if model["slug"] != runtime.GatewayModel || model["apply_patch_tool_type"] != nil || model["shell_type"] != "disabled" || model["default_reasoning_level"] != "high" || model["supports_parallel_tool_calls"] != true {
		t.Fatal("research catalog changed model/reasoning or left local mutations enabled")
	}
	s := &appServerSession{modelCatalogKind: modelCatalogFor(runtime)}
	if !s.SupportsSessionRuntime(runtime) {
		t.Fatal("review cannot reuse research process")
	}
	runtime.ToolProfile = ""
	if s.SupportsSessionRuntime(runtime) {
		t.Fatal("ordinary followup reused restricted model catalog")
	}
	path, err = writeModelCatalog(envFile, runtime)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if err = json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Models[0]["apply_patch_tool_type"] != "freeform" {
		t.Fatal("research restriction changed ordinary model descriptor")
	}
}
