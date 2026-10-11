package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const fencedPrefixTestPolicy = "Current application policy"

// fencedPrefixTestSession mirrors a fenced conversation after newAppServerSession:
// broker mode points TMPDIR at the conversation's own scratch directory.
func fencedPrefixTestSession(t *testing.T, brand, chatSessionID string, runtime core.SessionRuntime) *appServerSession {
	t.Helper()
	workDir := filepath.Join(t.TempDir(), brand)
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env, err := prepareFencedEnvironment(workDir, []string{"TOMAKO_ENV_NAMESPACE=Test"})
	if err != nil {
		t.Fatal(err)
	}
	s := &appServerSession{workDir: workDir, permissionsProfile: "tomako-brand-fence", extraEnv: env}
	s.alive.Store(true)
	t.Cleanup(func() { removeTaskRuntimeEnv(s.currentTaskRuntimeEnvFile()) })
	runtime.WorkspaceID = "ws-" + brand
	runtime.BrandID = brand
	runtime.UserID = "user-" + chatSessionID
	runtime.ChatSessionID = chatSessionID
	runtime.TaskID = "task-" + chatSessionID
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	scratch := s.prepareConversationScratch(time.Now())
	if scratch == "" {
		t.Fatal("fenced conversation has no scratch directory")
	}
	s.extraEnv = core.MergeEnv(s.extraEnv, []string{"TMPDIR=" + scratch})
	return s
}

func managedRuntime(policy string) core.SessionRuntime {
	return core.SessionRuntime{GatewayModel: "gpt-6.1-sol", ReasoningEffort: "medium", DeveloperInstructions: policy}
}

// leadingPrefix is everything bridge-controlled that Codex places before the
// end of the first developer message, plus the key that routes it.
func leadingPrefix(s *appServerSession) map[string]any {
	params := s.threadRequestParams()
	config := params["config"].(map[string]any)
	return map[string]any{
		"baseInstructions":                 params["baseInstructions"],
		"dynamicTools":                     params["dynamicTools"],
		"model":                            params["model"],
		"permissions":                      params["permissions"],
		"approvalPolicy":                   params["approvalPolicy"],
		"model_reasoning_effort":           config["model_reasoning_effort"],
		"web_search":                       config["web_search"],
		"include_permissions_instructions": config["include_permissions_instructions"],
		"prompt_cache_key":                 config["prompt_cache_key"],
		"collaborationMode":                s.turnDeveloperInstructions(),
	}
}

func TestFencedConversationsOfDifferentBrandsShareOneLeadingPrefix(t *testing.T) {
	first := fencedPrefixTestSession(t, "brand-a", "csess-1", managedRuntime(fencedPrefixTestPolicy))
	second := fencedPrefixTestSession(t, "brand-b", "csess-2", managedRuntime(fencedPrefixTestPolicy))

	a, b := leadingPrefix(first), leadingPrefix(second)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("leading prefix differs between conversations:\n%#v\n%#v", a, b)
	}
	if a["include_permissions_instructions"] != false {
		t.Fatal("the per-conversation permissions block must be off for fenced conversations")
	}
	if tools, _ := a["dynamicTools"].([]map[string]any); len(tools) == 0 {
		t.Fatal("expected the conversation tool set to be part of the compared prefix")
	}
	base, _ := a["baseInstructions"].(string)
	if !strings.HasSuffix(base, "\n\n"+fencedSandboxNote+"\n") {
		t.Fatal("the fenced base instructions must end with the static sandbox note")
	}
	if key, _ := a["prompt_cache_key"].(string); !regexp.MustCompile(`^tomako:test:[0-9a-f]{16}$`).MatchString(key) {
		t.Fatalf("prompt_cache_key = %q", key)
	}
	if !strings.Contains(strings.Join(first.startupArgs(), " "), "-c skills.bundled.enabled=false") {
		t.Fatal("fenced startup keeps the bundled system skills")
	}

	// The per-conversation values still reach Codex, outside the cached prefix.
	firstConfig := first.threadRequestParams()["config"].(map[string]any)
	secondConfig := second.threadRequestParams()["config"].(map[string]any)
	if firstConfig["shell_environment_policy.set.TMPDIR"] == secondConfig["shell_environment_policy.set.TMPDIR"] {
		t.Fatal("conversations must keep their own scratch directories")
	}
	if first.threadRequestParams()["cwd"] == second.threadRequestParams()["cwd"] {
		t.Fatal("brands must keep their own workspaces")
	}
}

func TestPromptCacheKeyChangesWithTheCachedPrefix(t *testing.T) {
	key := func(s *appServerSession) string {
		value, _ := s.threadRequestParams()["config"].(map[string]any)["prompt_cache_key"].(string)
		return value
	}
	base := key(fencedPrefixTestSession(t, "brand-a", "csess-1", managedRuntime(fencedPrefixTestPolicy)))

	otherPolicy := key(fencedPrefixTestSession(t, "brand-a", "csess-2", managedRuntime("Another application policy")))
	otherModel := managedRuntime(fencedPrefixTestPolicy)
	otherModel.GatewayModel = "deepseek-v4-flash"
	otherEffort := managedRuntime(fencedPrefixTestPolicy)
	otherEffort.ReasoningEffort = "high"
	analysis := managedRuntime(fencedPrefixTestPolicy)
	analysis.Scene = "brand_analysis"
	for name, got := range map[string]string{
		"developer instructions": otherPolicy,
		"model":                  key(fencedPrefixTestSession(t, "brand-a", "csess-3", otherModel)),
		"reasoning effort":       key(fencedPrefixTestSession(t, "brand-a", "csess-4", otherEffort)),
		"dynamic tools":          key(fencedPrefixTestSession(t, "brand-a", "csess-5", analysis)),
	} {
		if got == "" || got == base {
			t.Fatalf("a different %s must select a different key: %q vs %q", name, got, base)
		}
	}
}

func TestOnlyFencedApplicationConversationsShareThePrefix(t *testing.T) {
	unfenced := &appServerSession{workDir: "/srv/tomako"}
	unfenced.alive.Store(true)
	defer func() { removeTaskRuntimeEnv(unfenced.currentTaskRuntimeEnvFile()) }()
	if err := unfenced.SetSessionRuntime(managedRuntime(fencedPrefixTestPolicy)); err != nil {
		t.Fatal(err)
	}
	want, err := publicConversationBaseInstructions()
	if err != nil {
		t.Fatal(err)
	}
	params := unfenced.threadRequestParams()
	if params["baseInstructions"] != want {
		t.Fatal("unfenced conversations keep the public base instructions unchanged")
	}

	unmanaged := fencedPrefixTestSession(t, "brand-a", "csess-1", core.SessionRuntime{})
	for name, session := range map[string]*appServerSession{"unfenced": unfenced, "fenced without application instructions": unmanaged} {
		config := session.threadRequestParams()["config"].(map[string]any)
		for _, key := range []string{"include_permissions_instructions", "prompt_cache_key"} {
			if _, ok := config[key]; ok {
				t.Fatalf("%s session must keep Codex's default %s", name, key)
			}
		}
	}
	if strings.Contains(strings.Join(unfenced.startupArgs(), " "), "skills.bundled") {
		t.Fatal("unfenced startup changed bundled skills")
	}
}

func TestPromptCacheNamespaceMatchesSkillsRuntimeNamespace(t *testing.T) {
	for raw, want := range map[string]string{
		"":                      "default",
		"prod":                  "prod",
		" Test / CN ":           "test_cn",
		"Prod.Env":              "prod_env",
		strings.Repeat("a", 40): strings.Repeat("a", 32),
	} {
		if got := promptCacheNamespace([]string{"TOMAKO_ENV_NAMESPACE=" + raw}); got != want {
			t.Errorf("namespace(%q) = %q, want %q", raw, got, want)
		}
	}
}
