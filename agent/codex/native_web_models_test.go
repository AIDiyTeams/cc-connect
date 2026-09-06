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
	s := &appServerSession{model: "tomako/gpt-5.6-sol", effort: "high", modelProvider: "custom", nativeWebModelCatalog: catalog}
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
