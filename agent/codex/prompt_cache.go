package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// The model gateway caches a request prefix only up to the end of the leading
// developer message (or a previously sent full request), and only for the same
// prompt_cache_key. Codex keys every thread by its own id and puts per-brand,
// per-conversation text in that message: the permissions block (writable
// roots include the conversation scratch path) and the skill-roots table (the
// brand CODEX_HOME's bundled system skills). Every new conversation therefore
// paid for its whole prefix.
//
// Fenced application conversations drop both parts and share one key per
// identical prefix. The permissions block is replaced by a static note in the
// base instructions; <environment_context>, a later user-role message, still
// carries the exact filesystem and network profile. Bundled system skills are
// disabled at process start (startupArgs).
//
// The key covers everything that precedes the end of the leading developer
// message and differs between conversation kinds: base instructions, the
// dynamic tool definitions, model, reasoning effort, web search mode and the
// current application instructions (the collaboration-mode block). Codex's own
// built-in tools and fragments follow from the same values and the deployed
// Codex build. OpenAI routes roughly 15 requests per minute per prefix and key
// to one cache node; above that, requests spill to other nodes and hit less.
// When one bucket outgrows that, append a stable shard to the key, such as a
// hash of the brand id modulo N.
const fencedSandboxNote = "## Sandbox and approvals\n\n" +
	"Commands run inside this workspace's sandbox without approval prompts: never request escalation or set `sandbox_permissions`; " +
	"public network access is enabled; the writable and denied paths are listed in `<environment_context>`."

const promptCacheKeyPrefix = "tomako"

var promptCacheNamespaceUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// shareFencedPromptPrefix runs after the rest of thread/start (or resume)
// params are built. A loaded thread ignores these overrides and keeps its
// original developer message; an unloaded thread resumes with them, and its
// recorded history still contains whatever permissions block it started with.
func (s *appServerSession) shareFencedPromptPrefix(params, config map[string]any) {
	base, _ := params["baseInstructions"].(string)
	// Only application-managed conversations carry our base instructions, so
	// only they can replace the permissions block with the static note.
	if s.permissionsProfile == "" || base == "" {
		return
	}
	params["baseInstructions"] = strings.TrimRight(base, "\n") + "\n\n" + fencedSandboxNote + "\n"
	config["include_permissions_instructions"] = false
	config["prompt_cache_key"] = s.sharedPromptCacheKey(params, config)
}

func (s *appServerSession) sharedPromptCacheKey(params, config map[string]any) string {
	material, _ := json.Marshal(map[string]any{
		"base_instructions":  params["baseInstructions"],
		"dynamic_tools":      params["dynamicTools"],
		"model":              params["model"],
		"reasoning_effort":   config["model_reasoning_effort"],
		"web_search":         config["web_search"],
		"collaboration_mode": s.turnDeveloperInstructions(),
	})
	sum := sha256.Sum256(material)
	return promptCacheKeyPrefix + ":" + promptCacheNamespace(core.MergeEnv(os.Environ(), s.extraEnv)) + ":" + hex.EncodeToString(sum[:8])
}

// promptCacheNamespace separates environments the way Skills-OL does.
func promptCacheNamespace(env []string) string {
	namespace := strings.ToLower(strings.TrimSpace(envValue(env, "TOMAKO_ENV_NAMESPACE")))
	namespace = promptCacheNamespaceUnsafe.ReplaceAllString(namespace, "_")
	if len(namespace) > 32 {
		namespace = namespace[:32]
	}
	if namespace == "" {
		return "default"
	}
	return namespace
}
