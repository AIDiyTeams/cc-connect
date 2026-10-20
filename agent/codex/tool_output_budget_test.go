package codex

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func startToolBudgetTestSession(t *testing.T, model string, runtime core.SessionRuntime) *appServerSession {
	t.Helper()
	dir := t.TempDir()
	writeFakeCodexScript(t, dir, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":1,"result":{}}\n' ;;
  esac
done
`, `
while (($line = [Console]::In.ReadLine()) -ne $null) {
  if ($line -like '*"method":"initialize"*') { [Console]::Out.WriteLine('{"id":1,"result":{}}') }
}
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s, err := newAppServerSession(context.Background(), "", dir, model, "medium", "", "", "", "", "custom", nil, "", runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestToolOutputBudget_OrdinaryGPTStartupWithoutCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		runtime     core.SessionRuntime
		want        bool
	}{
		{"ordinary GPT", "old-model", core.SessionRuntime{GatewayModel: gpt61SolGatewaySlug}, true},
		{"configured model", gpt61SolGatewaySlug, core.SessionRuntime{}, true},
		{"other gateway model", "old-model", core.SessionRuntime{GatewayModel: "tomako/gpt-6-luna"}, true},
		{"unprefixed model Codex may know", "gpt-6.1-sol", core.SessionRuntime{}, false},
		{"native catalog", "old-model", core.SessionRuntime{GatewayModel: gpt61SolGatewaySlug, WebSearch: "live", Scene: "brand_competitor_discovery"}, false},
		{"DeepSeek catalog", "old-model", core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := startToolBudgetTestSession(t, tc.model, tc.runtime)
			if !s.SupportsSessionRuntime(tc.runtime) {
				t.Fatal("unchanged startup runtime must remain reusable")
			}
			args := strings.Join(s.startupArgs(), "\n")
			if got := strings.Contains(args, "tool_output_token_limit=8000"); got != tc.want {
				t.Fatalf("startup output budget present=%v, want %v: %s", got, tc.want, args)
			}
			if strings.Contains(args, "model_context_window") {
				t.Fatal("tool budget must not override model context")
			}
		})
	}
}

func TestToolOutputBudget_ModelSwitchRequiresProcessRebuild(t *testing.T) {
	gpt := core.SessionRuntime{GatewayModel: gpt61SolGatewaySlug}
	other := core.SessionRuntime{GatewayModel: "gpt-5.6-sol"}
	for _, initial := range []core.SessionRuntime{gpt, other} {
		s := startToolBudgetTestSession(t, "old-model", initial)
		if !s.SupportsSessionRuntime(initial) {
			t.Fatal("unchanged runtime must reuse process")
		}
		next := gpt
		if initial.GatewayModel == gpt.GatewayModel {
			next = other
		}
		if s.SupportsSessionRuntime(next) {
			t.Fatal("switching budget policy must rebuild process even when both catalogs are empty")
		}
		// Updating turn metadata cannot change the budget of an existing process.
		if err := s.SetSessionRuntime(next); err != nil {
			t.Fatal(err)
		}
		if s.SupportsSessionRuntime(next) {
			t.Fatal("runtime mutation must not disguise the startup budget")
		}
	}
}

// Gateway models without a catalog share one budget, so moving a conversation between
// them keeps its process; DeepSeek keeps its own catalog and is unaffected.
func TestToolOutputBudget_GatewayModelsShareBudget(t *testing.T) {
	luna := core.SessionRuntime{GatewayModel: "tomako/gpt-6-luna"}
	s := startToolBudgetTestSession(t, "old-model", luna)
	for _, next := range []string{gpt61SolGatewaySlug, "tomako/gpt-6-astra", "tomako/kimi-k3"} {
		if !s.SupportsSessionRuntime(core.SessionRuntime{GatewayModel: next}) {
			t.Fatalf("switching %s -> %s must not rebuild the process", luna.GatewayModel, next)
		}
	}
	deepseek := core.SessionRuntime{GatewayModel: "tomako/deepseek-v4-flash-vision-exp"}
	if s.SupportsSessionRuntime(deepseek) {
		t.Fatal("DeepSeek runs on its own catalog process")
	}
	if got := toolOutputTokensFor(deepseek.GatewayModel, modelCatalogFor(deepseek)); got != 0 {
		t.Fatalf("DeepSeek must keep its catalog output policy, got startup budget %d", got)
	}
}
