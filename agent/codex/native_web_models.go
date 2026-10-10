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
	case "tomako/gpt-5.6-sol", "gpt-5.6-sol", "tomako/deepseek-v4-flash-vision-exp",
		gpt61SolGatewaySlug, "gpt-6.1-sol":
	default:
		return false
	}
	switch strings.TrimSpace(runtime.Scene) {
	case "brand_competitor_discovery", "growth_opportunity_user_voice_plan", "growth_opportunity_user_voice_search", "growth_opportunity_user_voice_judge":
		return true
	default:
		return false
	}
}

// GPT-6.1 Sol is the platform default model behind the gateway slug below. Codex has no
// metadata for that namespaced slug, so native-search scenes would fall back to Responses
// Lite, which omits hosted web_search. Its descriptor is the public GPT-5.6 Sol one above
// (same tool, shell and image capabilities) under the gateway slug, defaulting to the
// platform's medium effort.
const gpt61SolGatewaySlug = "tomako/gpt-6.1-sol"

func gpt61SolNativeWebDescriptor() (json.RawMessage, error) {
	var source struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(nativeWebModels, &source); err != nil || len(source.Models) != 1 {
		return nil, fmt.Errorf("codex GPT-6.1 Sol web catalog: unexpected base descriptor")
	}
	model := source.Models[0]
	model["slug"] = gpt61SolGatewaySlug
	model["display_name"] = "GPT-6.1 Sol"
	model["default_reasoning_level"] = "medium"
	model["use_responses_lite"] = false
	data, err := json.Marshal(model)
	if err != nil {
		return nil, fmt.Errorf("codex GPT-6.1 Sol web catalog encode: %w", err)
	}
	return data, nil
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
	gpt61, err := gpt61SolNativeWebDescriptor()
	if err != nil {
		return "", err
	}
	catalog.Models = append(catalog.Models, gpt61)
	data, err := json.Marshal(catalog)
	if err != nil {
		return "", fmt.Errorf("codex native web catalog encode: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("codex native web model catalog: %w", err)
	}
	return path, nil
}

// Codex has no built-in metadata for the DeepSeek gateway slugs, so without a catalog every
// DeepSeek conversation runs on generic fallbacks: tool output cut at 10 KB and Responses
// Lite, which sends the base prompt as a developer message, sends the tool list as an input
// item and turns off parallel tool calls. DeepSeek's own Codex setup declares standard
// Responses and a 10,000-token tool-output budget
// (https://api-docs.deepseek.com/quick_start/agent_integrations/codex/). The conversation
// catalog adopts those two settings from the official descriptor and keeps everything else
// that changes behaviour as it is today: compaction at the fallback threshold (90% of the
// generic 272k window; the uplink re-sends the whole context on every call), no multi-agent
// v2 team prompt, no verbosity parameter, no original-detail images, and the generic Codex
// base prompt for sessions without an application base prompt.
const (
	deepSeekToolOutputTokens       = 10000
	deepSeekAutoCompactTokenLimit  = 244800
	deepSeekConversationCatalogTag = "deepseek:"
	nativeWebModelCatalogTag       = "native-web"
)

func isDeepSeekGatewayModel(model string) bool {
	slug := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(strings.TrimPrefix(slug, "tomako/"), "deepseek-")
}

// modelCatalogFor names the startup catalog a runtime needs, or "" for none. The catalog
// is a process start argument, so a live process serves a runtime only when they agree.
func modelCatalogFor(runtime core.SessionRuntime) string {
	if needsNativeWebModelCatalog(runtime) {
		return nativeWebModelCatalogTag
	}
	if model := strings.TrimSpace(runtime.GatewayModel); isDeepSeekGatewayModel(model) {
		return deepSeekConversationCatalogTag + model
	}
	return ""
}

func writeModelCatalog(envFile string, runtime core.SessionRuntime) (string, error) {
	switch kind := modelCatalogFor(runtime); {
	case kind == nativeWebModelCatalogTag:
		return writeNativeWebModelCatalog(envFile)
	case strings.HasPrefix(kind, deepSeekConversationCatalogTag):
		return writeDeepSeekConversationCatalog(envFile, strings.TrimPrefix(kind, deepSeekConversationCatalogTag))
	default:
		return "", nil
	}
}

func writeDeepSeekConversationCatalog(envFile, slug string) (string, error) {
	if envFile == "" {
		return "", fmt.Errorf("codex DeepSeek catalog requires a private session directory")
	}
	var source struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(deepseekWebModels, &source); err != nil {
		return "", fmt.Errorf("codex DeepSeek catalog decode: %w", err)
	}
	if len(source.Models) != 1 {
		return "", fmt.Errorf("codex DeepSeek catalog: expected one descriptor, got %d", len(source.Models))
	}
	model := source.Models[0]
	model["slug"] = slug
	model["use_responses_lite"] = false
	model["truncation_policy"] = map[string]any{"mode": "tokens", "limit": deepSeekToolOutputTokens}
	model["auto_compact_token_limit"] = deepSeekAutoCompactTokenLimit
	model["multi_agent_version"] = "v1"
	model["support_verbosity"] = false
	model["default_verbosity"] = nil
	model["supports_image_detail_original"] = false
	delete(model, "model_messages")
	model["base_instructions"] = codexDefaultBaseInstructions
	data, err := json.Marshal(map[string]any{"models": []any{model}})
	if err != nil {
		return "", fmt.Errorf("codex DeepSeek catalog encode: %w", err)
	}
	path := filepath.Join(filepath.Dir(envFile), "deepseek-models.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("codex DeepSeek model catalog: %w", err)
	}
	return path, nil
}

func (s *appServerSession) SupportsSessionRuntime(runtime core.SessionRuntime) bool {
	kind := modelCatalogFor(runtime)
	model := strings.TrimSpace(runtime.GatewayModel)
	if model == "" {
		s.runtimeMu.RLock()
		model = s.model
		s.runtimeMu.RUnlock()
	}
	return s.modelCatalogKind == kind && s.startupToolOutputTokens == toolOutputTokensFor(model, kind)
}

// Ordinary namespaced GPT has no Codex descriptor and otherwise gets a 10KB
// tool-output fallback. Bound output to 8k tokens without replacing its model
// metadata. Existing catalogs retain their own output policies.
func toolOutputTokensFor(model, catalog string) int {
	if catalog == "" && strings.TrimSpace(model) == gpt61SolGatewaySlug {
		return 8000
	}
	return 0
}
