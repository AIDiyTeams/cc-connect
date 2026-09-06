package codex

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// Exact public model descriptor from Codex rust-v0.153.4:
// https://github.com/openai/codex/blob/rust-v0.153.4/codex-rs/models-manager/models.json
// Only use_responses_lite changes. Lite omits hosted web_search for the custom
// Responses provider. This startup-only catalog restores standard Responses
// without changing the model, prompt, reasoning, provider, or host configuration.
// Revalidate this compatibility fixture when upgrading the test Codex runtime.
//
//go:embed native_web_models.json
var nativeWebModels []byte

func needsNativeWebModelCatalog(runtime core.SessionRuntime) bool {
	if normalizeWebSearch(runtime.WebSearch) != "live" {
		return false
	}
	switch strings.TrimSpace(runtime.GatewayModel) {
	case "tomako/gpt-5.6-sol", "gpt-5.6-sol":
	default:
		return false
	}
	switch strings.TrimSpace(runtime.Scene) {
	case "growth_opportunity_user_voice_plan", "growth_opportunity_user_voice_search", "growth_opportunity_user_voice_judge":
		return true
	default:
		return false
	}
}

func writeNativeWebModelCatalog(envFile string) (string, error) {
	if envFile == "" {
		return "", fmt.Errorf("codex native web catalog requires a private session directory")
	}
	path := filepath.Join(filepath.Dir(envFile), "native-web-models.json")
	if err := os.WriteFile(path, nativeWebModels, 0o600); err != nil {
		return "", fmt.Errorf("codex native web model catalog: %w", err)
	}
	return path, nil
}

func (s *appServerSession) SupportsSessionRuntime(runtime core.SessionRuntime) bool {
	return (s.nativeWebModelCatalog != "") == needsNativeWebModelCatalog(runtime)
}
