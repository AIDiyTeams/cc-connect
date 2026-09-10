package codex

import (
	_ "embed"
	"encoding/json"
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

// Public DeepSeek Codex descriptor, retrieved 2026-09-09 from the setup
// referenced by https://api-docs.deepseek.com/quick_start/agent_integrations/codex/.
// The slug uses our gateway namespace. The tool-output budget is 128k tokens:
// the archive function returns a bounded batch, and the official 10k default
// would truncate its JSON in conversation history. Preserve all other metadata.
//
//go:embed deepseek_web_models.json
var deepseekWebModels []byte

func needsNativeWebModelCatalog(runtime core.SessionRuntime) bool {
	if normalizeWebSearch(runtime.WebSearch) != "live" {
		return false
	}
	switch strings.TrimSpace(runtime.GatewayModel) {
	case "tomako/gpt-5.6-sol", "gpt-5.6-sol", "tomako/deepseek-v4-flash-vision-exp":
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
	var catalog, deepseek struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(nativeWebModels, &catalog); err != nil {
		return "", fmt.Errorf("codex native web catalog decode: %w", err)
	}
	if err := json.Unmarshal(deepseekWebModels, &deepseek); err != nil {
		return "", fmt.Errorf("codex DeepSeek web catalog decode: %w", err)
	}
	catalog.Models = append(catalog.Models, deepseek.Models...)
	data, err := json.Marshal(catalog)
	if err != nil {
		return "", fmt.Errorf("codex native web catalog encode: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("codex native web model catalog: %w", err)
	}
	return path, nil
}

func (s *appServerSession) SupportsSessionRuntime(runtime core.SessionRuntime) bool {
	return (s.nativeWebModelCatalog != "") == needsNativeWebModelCatalog(runtime)
}
