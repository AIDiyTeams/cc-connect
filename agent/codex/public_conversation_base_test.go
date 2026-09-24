package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// The vendored prompt must be the exact text Codex 0.153.4 ships, so that the
// override changes communication guidance only. Refreshing it after a Codex
// upgrade means updating this checksum on purpose.
const codexDefaultBaseInstructionsSHA256 = "ac8ae107a0d72fe3476b430afb161ea4e67da2e446d778aefc44828160559807"

func TestVendoredCodexBaseInstructionsArePinned(t *testing.T) {
	sum := sha256.Sum256([]byte(codexDefaultBaseInstructions))
	if got := hex.EncodeToString(sum[:]); got != codexDefaultBaseInstructionsSHA256 {
		t.Fatalf("vendored Codex base instructions changed: sha256=%s", got)
	}
	for _, marker := range []string{preambleSectionStart, preambleSectionEnd, progressSectionStart, progressSectionEnd, finalSectionEnd} {
		if strings.Count(codexDefaultBaseInstructions, marker) != 1 {
			t.Fatalf("marker %q must appear exactly once in the vendored prompt", strings.TrimSpace(marker))
		}
	}
}

func TestPublicConversationBaseReplacesOnlyTheCodingAssistantCommunicationGuidance(t *testing.T) {
	got, err := publicConversationBaseInstructions()
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{
		"### Preamble messages",
		"send a brief preamble to the user explaining what you’re about to do",
		"I’ve explored the repo; now checking the API route definitions.",
		"## Sharing progress updates",
		"describe what is immediately about to be done next",
		"## Presenting your work and final message",
		"You are producing plain text that will later be styled by the CLI",
		"no more than 10 lines",
		"`**Title Case**`",
		"Don’t nest bullets or create deep hierarchies",
		"The user is working on the same computer as you",
	} {
		if strings.Contains(got, removed) {
			t.Fatalf("coding-assistant communication guidance survived: %q", removed)
		}
	}
	for _, kept := range []string{
		"## Talking with the user while you work",
		"Never restate or paraphrase the request",
		"comply with them silently",
		"language of the user's latest message",
		"Never mention the workspace, capabilities, skills, instructions, credentials",
		"Exploring the environment is silent",
		"names what this step achieves for the user",
		"publish a plan of three to five steps",
		"Delivery mechanics are silent too",
		"never drift",
		"do not end with what you will look at, check or confirm first",
		"## Planning",
		"## Task execution",
		"## Ambition vs. precision",
		"## The final answer",
		"application that renders Markdown",
		"Lead with the answer",
		"Match the size to the request",
		"One visual per point, never two views of the same numbers",
		"An explicit output contract always wins",
		"# Tool Guidelines",
		"## `update_plan`",
	} {
		if strings.Count(got, kept) != 1 {
			t.Fatalf("expected %q exactly once in composed base instructions", kept)
		}
	}
	// Everything outside the three communication sections is byte-identical.
	head := codexDefaultBaseInstructions[:strings.Index(codexDefaultBaseInstructions, preambleSectionStart)]
	if !strings.HasPrefix(got, head) {
		t.Fatal("text before the communication section changed")
	}
	tail := codexDefaultBaseInstructions[strings.Index(codexDefaultBaseInstructions, finalSectionEnd):]
	if !strings.HasSuffix(got, tail) {
		t.Fatal("tool guidelines after the final-answer section changed")
	}
	if !strings.Contains(got, "## The final answer\n\nThe final answer is read") || !strings.Contains(got, "without structure.\n\n# Tool Guidelines\n") {
		t.Fatal("final-answer section is not framed by its heading and the tool guidelines")
	}
	middle := codexDefaultBaseInstructions[strings.Index(codexDefaultBaseInstructions, preambleSectionEnd):strings.Index(codexDefaultBaseInstructions, progressSectionStart)]
	if !strings.Contains(got, middle) {
		t.Fatal("planning and execution guidance between the two sections changed")
	}
	if strings.Contains(got, "\n\n\n\n") {
		t.Fatal("composition left stray blank lines")
	}
	t.Logf("composed base instructions: %d chars sha256=%x", len(got), sha256.Sum256([]byte(got)))
}

func TestComposePublicConversationBaseRejectsUnknownLayout(t *testing.T) {
	if _, err := composePublicConversationBase("# Different prompt\n## Planning\n", "## Rules\n", "## Answer\n"); err == nil {
		t.Fatal("missing markers must be reported, not silently skipped")
	}
	doubled := codexDefaultBaseInstructions + "\n" + preambleSectionStart
	if _, err := composePublicConversationBase(doubled, publicConversationSection, publicFinalAnswerSection); err == nil {
		t.Fatal("a duplicated marker must be rejected")
	}
	withoutTools := strings.Replace(codexDefaultBaseInstructions, finalSectionEnd, "# Other\n", 1)
	if _, err := composePublicConversationBase(withoutTools, publicConversationSection, publicFinalAnswerSection); err == nil {
		t.Fatal("a missing final-answer end marker must be rejected")
	}
}

func TestThreadParamsOverrideBaseInstructionsOnlyForApplicationManagedConversations(t *testing.T) {
	plain := &appServerSession{workDir: "/srv/tomako"}
	if _, ok := plain.threadRequestParams()["baseInstructions"]; ok {
		t.Fatal("a session without trusted developer instructions must keep Codex's native base prompt")
	}

	managed := &appServerSession{workDir: "/srv/tomako"}
	managed.alive.Store(true)
	defer func() { removeTaskRuntimeEnv(managed.currentTaskRuntimeEnvFile()) }()
	if err := managed.SetSessionRuntime(core.SessionRuntime{DeveloperInstructions: "Public progress communication: ..."}); err != nil {
		t.Fatal(err)
	}
	want, err := publicConversationBaseInstructions()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := managed.threadRequestParams()["baseInstructions"].(string)
	if got != want {
		t.Fatalf("managed conversation must start with the public-conversation base prompt (got %d chars)", len(got))
	}
	if strings.Contains(got, "### Preamble messages") {
		t.Fatal("override still carries the coding-assistant preamble guidance")
	}
	managedConfig, _ := managed.threadRequestParams()["config"].(map[string]any)
	if managedConfig["show_raw_agent_reasoning"] != true {
		t.Fatal("managed conversations must receive raw reasoning items for the backend summarizer")
	}
	plainConfig, _ := plain.threadRequestParams()["config"].(map[string]any)
	if _, ok := plainConfig["show_raw_agent_reasoning"]; ok {
		t.Fatal("plain bridge sessions keep Codex's default reasoning visibility")
	}
}
