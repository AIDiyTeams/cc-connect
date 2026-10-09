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

func TestDeepSeekNativeWebCatalog_PreservesOfficialCapabilities(t *testing.T) {
	for _, scene := range []string{"growth_opportunity_user_voice_plan", "growth_opportunity_user_voice_search", "growth_opportunity_user_voice_judge"} {
		if !needsNativeWebModelCatalog(core.SessionRuntime{Scene: scene, GatewayModel: "tomako/deepseek-v4-flash-vision-exp", WebSearch: "live"}) {
			t.Fatalf("missing DeepSeek catalog for %s", scene)
		}
	}
	file, err := writeNativeWebModelCatalog(filepath.Join(t.TempDir(), "env"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 3 {
		t.Fatalf("expected all model descriptors, got %d", len(catalog.Models))
	}
	gpt := catalog.Models[2]
	if gpt["slug"] != "tomako/gpt-6.1-sol" || gpt["use_responses_lite"] != false || gpt["default_reasoning_level"] != "medium" || gpt["web_search_tool_type"] == "" || gpt["base_instructions"] == "" {
		t.Fatalf("GPT-6.1 Sol native-search descriptor: %v", gpt["slug"])
	}
	model := catalog.Models[1]
	if model["truncation_policy"].(map[string]any)["limit"] != float64(128000) {
		t.Fatal("archive batch would be truncated by the default tool-output budget")
	}
	if model["slug"] != "tomako/deepseek-v4-flash-vision-exp" || model["context_window"] != float64(1048576) || model["default_reasoning_level"] != "high" || model["use_responses_lite"] != false || model["supports_search_tool"] != true || model["base_instructions"] == "" {
		t.Fatal("DeepSeek capability descriptor changed")
	}
}

func TestGPT61SolNativeSearchScenesUseTheSearchCatalog(t *testing.T) {
	for _, scene := range []string{"brand_competitor_discovery", "growth_opportunity_user_voice_plan", "growth_opportunity_user_voice_search", "growth_opportunity_user_voice_judge"} {
		runtime := core.SessionRuntime{Scene: scene, GatewayModel: "tomako/gpt-6.1-sol", WebSearch: "live", ReasoningEffort: "medium"}
		if modelCatalogFor(runtime) != nativeWebModelCatalogTag {
			t.Fatalf("GPT-6.1 Sol %s starts without hosted search", scene)
		}
	}
	if modelCatalogFor(core.SessionRuntime{Scene: "brand_analysis", GatewayModel: "tomako/gpt-6.1-sol", WebSearch: "disabled"}) != "" {
		t.Fatal("an ordinary GPT-6.1 Sol turn must not load the search catalog")
	}
}

func TestBrandCompetitorNativeSearch_RetainsHostedSearchCapability(t *testing.T) {
	runtime := core.SessionRuntime{Scene: "brand_competitor_discovery", GatewayModel: "tomako/deepseek-v4-flash-vision-exp", WebSearch: "live"}
	if !needsNativeWebModelCatalog(runtime) {
		t.Fatal("competitor discovery starts without its hosted search capability")
	}
	ordinary := &appServerSession{}
	if ordinary.SupportsSessionRuntime(runtime) {
		t.Fatal("an ordinary process must restart with the search catalog")
	}
	runtime.WebSearch = "disabled"
	if needsNativeWebModelCatalog(runtime) {
		t.Fatal("catalog must not enable search when the runtime disables it")
	}
}

func TestNativeWebResponsesLite_StartupCatalogScopeAndCleanup(t *testing.T) {
	for _, scene := range []string{"growth_opportunity_user_voice_plan", "growth_opportunity_user_voice_search", "growth_opportunity_user_voice_judge"} {
		r := core.SessionRuntime{Scene: scene, GatewayModel: "tomako/gpt-5.6-sol", WebSearch: "live", ReasoningEffort: "high"}
		if !needsNativeWebModelCatalog(r) {
			t.Fatalf("native search not restored for %s", scene)
		}
		for _, changed := range []core.SessionRuntime{
			{Scene: "brand_analysis", GatewayModel: r.GatewayModel, WebSearch: "live"},
			{Scene: scene, GatewayModel: "tomako/deepseek-v4-flash", WebSearch: "live"},
			{Scene: scene, GatewayModel: r.GatewayModel, WebSearch: "disabled"},
			{},
		} {
			if needsNativeWebModelCatalog(changed) {
				t.Fatalf("catalog leaked to unrelated runtime: %+v", changed)
			}
		}
	}
	file, err := createTaskRuntimeEnv(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(file) })
	catalog, err := writeNativeWebModelCatalog(file)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(catalog)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog permissions: %v %v", info, err)
	}
	var data struct {
		Models []struct {
			Slug         string `json:"slug"`
			Lite         bool   `json:"use_responses_lite"`
			Instructions string `json:"base_instructions"`
			WebSearch    string `json:"web_search_tool_type"`
		} `json:"models"`
	}
	if err := json.Unmarshal(nativeWebModels, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Models) != 1 || data.Models[0].Slug != "gpt-5.6-sol" || data.Models[0].Lite || data.Models[0].Instructions == "" || data.Models[0].WebSearch == "" {
		t.Fatal("invalid native-web compatibility descriptor")
	}
	s := &appServerSession{model: "tomako/gpt-5.6-sol", effort: "high", modelProvider: "custom", nativeWebModelCatalog: catalog, modelCatalogKind: nativeWebModelCatalogTag}
	args := strings.Join(s.startupArgs(), "\n")
	for _, want := range []string{"model_catalog_json=", `model="tomako/gpt-5.6-sol"`, `model_reasoning_effort="high"`, `model_provider="custom"`} {
		if !strings.Contains(args, want) {
			t.Fatalf("startup missing %s: %s", want, args)
		}
	}
	r := core.SessionRuntime{Scene: "growth_opportunity_user_voice_search", GatewayModel: s.model, WebSearch: "live"}
	if !s.SupportsSessionRuntime(r) || s.SupportsSessionRuntime(core.SessionRuntime{}) {
		t.Fatal("process must recycle when crossing catalog boundary")
	}
	ordinary := &appServerSession{model: s.model}
	if ordinary.SupportsSessionRuntime(r) || !ordinary.SupportsSessionRuntime(core.SessionRuntime{}) || strings.Contains(strings.Join(ordinary.startupArgs(), " "), "model_catalog_json") {
		t.Fatal("ordinary process catalog boundary incorrect")
	}
	removeTaskRuntimeEnv(file)
	if _, err := os.Stat(catalog); !os.IsNotExist(err) {
		t.Fatal("catalog survived session cleanup")
	}
}

func TestNativeWebResponsesLite_CatalogAppliedBeforeProcessInitialize(t *testing.T) {
	workDir := t.TempDir()
	capture := filepath.Join(workDir, "startup-args")
	writeFakeCodexScript(t, workDir, `#!/bin/sh
printf '%s\n' "$@" > "$CC_TEST_STARTUP_ARGS"
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":1,"result":{}}\n' ;;
  esac
done
`, `
[System.IO.File]::WriteAllLines($env:CC_TEST_STARTUP_ARGS, [string[]]$args)
while (($line = [Console]::In.ReadLine()) -ne $null) {
  if ($line -like '*"method":"initialize"*') { [Console]::Out.WriteLine('{"id":1,"result":{}}') }
}
`)
	t.Setenv("PATH", workDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := core.SessionRuntime{Scene: "growth_opportunity_user_voice_plan", GatewayModel: "tomako/gpt-5.6-sol", ReasoningEffort: "high", WebSearch: "live"}
	s, err := newAppServerSession(context.Background(), "", workDir, "old-model", "low", "", "", "", "", "custom", []string{"CC_TEST_STARTUP_ARGS=" + capture}, "", r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"model_catalog_json=", `model="tomako/gpt-5.6-sol"`, `model_reasoning_effort="high"`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("process started before runtime applied: missing %s in %s", want, body)
		}
	}
	if _, err := os.Stat(s.nativeWebModelCatalog); err != nil {
		t.Fatal("process received missing catalog", err)
	}
}

func TestDeepSeekConversationCatalog_AdoptsOfficialResponsesAndToolBudgetOnly(t *testing.T) {
	runtime := core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash-vision-exp", WebSearch: "disabled"}
	if got := modelCatalogFor(runtime); got != deepSeekConversationCatalogTag+runtime.GatewayModel {
		t.Fatalf("DeepSeek conversation must start with its catalog, got %q", got)
	}
	for _, other := range []core.SessionRuntime{
		{GatewayModel: "tomako/gpt-5.6-sol"},
		{GatewayModel: "tomako/kimi-k3"},
		{GatewayModel: "tomako/text-balanced-v1"},
		{},
	} {
		if got := modelCatalogFor(other); got != "" {
			t.Fatalf("catalog leaked to %+v: %q", other, got)
		}
	}
	native := core.SessionRuntime{Scene: "growth_opportunity_user_voice_search", GatewayModel: runtime.GatewayModel, WebSearch: "live"}
	if modelCatalogFor(native) != nativeWebModelCatalogTag {
		t.Fatal("user-voice scenes keep their archive catalog")
	}

	env, err := createTaskRuntimeEnv(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(env) })
	path, err := writeModelCatalog(env, runtime)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog permissions: %v %v", info, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil || len(catalog.Models) != 1 {
		t.Fatalf("catalog shape: %v %d", err, len(catalog.Models))
	}
	m := catalog.Models[0]
	policy, _ := m["truncation_policy"].(map[string]any)
	switch {
	case m["slug"] != runtime.GatewayModel:
		t.Fatalf("slug must match the conversation model: %v", m["slug"])
	case m["use_responses_lite"] != false:
		t.Fatal("conversation must use standard Responses")
	case policy["mode"] != "tokens" || policy["limit"] != float64(deepSeekToolOutputTokens):
		t.Fatalf("tool output budget must follow DeepSeek's setup: %v", policy)
	case m["supports_parallel_tool_calls"] != true:
		t.Fatal("parallel tool calls lost")
	case m["auto_compact_token_limit"] != float64(deepSeekAutoCompactTokenLimit):
		t.Fatal("compaction threshold must stay at today's fallback")
	case m["multi_agent_version"] != "v1":
		t.Fatal("multi-agent team prompt must not be introduced")
	case m["support_verbosity"] != false || m["supports_image_detail_original"] != false:
		t.Fatal("verbosity and image detail must stay as today")
	case m["model_messages"] != nil || m["base_instructions"] != codexDefaultBaseInstructions:
		t.Fatal("sessions without an application base prompt must keep the generic Codex prompt")
	}
	removeTaskRuntimeEnv(env)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("catalog survived session cleanup")
	}
}

func TestDeepSeekConversationCatalog_ProcessReuseFollowsCatalog(t *testing.T) {
	deepseek := core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash-vision-exp"}
	gpt := core.SessionRuntime{GatewayModel: "tomako/gpt-5.6-sol"}
	s := &appServerSession{modelCatalogKind: modelCatalogFor(deepseek)}
	if !s.SupportsSessionRuntime(deepseek) {
		t.Fatal("same DeepSeek runtime must reuse the process")
	}
	if s.SupportsSessionRuntime(gpt) || s.SupportsSessionRuntime(core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash"}) {
		t.Fatal("a different model must restart with its own catalog")
	}
	plain := &appServerSession{}
	if plain.SupportsSessionRuntime(deepseek) || !plain.SupportsSessionRuntime(gpt) {
		t.Fatal("a process without a catalog serves only runtimes that need none")
	}
}

func TestDeepSeekConversationCatalog_AppliedBeforeProcessInitialize(t *testing.T) {
	workDir := t.TempDir()
	capture := filepath.Join(workDir, "startup-args")
	writeFakeCodexScript(t, workDir, `#!/bin/sh
printf '%s\n' "$@" > "$CC_TEST_STARTUP_ARGS"
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":1,"result":{}}\n' ;;
  esac
done
`, `
[System.IO.File]::WriteAllLines($env:CC_TEST_STARTUP_ARGS, [string[]]$args)
while (($line = [Console]::In.ReadLine()) -ne $null) {
  if ($line -like '*"method":"initialize"*') { [Console]::Out.WriteLine('{"id":1,"result":{}}') }
}
`)
	t.Setenv("PATH", workDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash-vision-exp", ReasoningEffort: "high", WebSearch: "disabled"}
	s, err := newAppServerSession(context.Background(), "", workDir, "old-model", "low", "", "", "", "", "custom", []string{"CC_TEST_STARTUP_ARGS=" + capture}, "", r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	body, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"model_catalog_json=", `model="tomako/deepseek-v4-flash-vision-exp"`, "deepseek-models.json"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("DeepSeek conversation started without its catalog: missing %s in %s", want, body)
		}
	}
	if s.modelCatalogKind != modelCatalogFor(r) {
		t.Fatalf("process must remember its catalog, got %q", s.modelCatalogKind)
	}
}
