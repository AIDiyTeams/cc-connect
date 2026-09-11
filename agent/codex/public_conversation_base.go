package codex

import (
	_ "embed"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// codexDefaultBaseInstructions is the built-in base prompt that Codex CLI
// 0.153.4 selects for the generic (non GPT-5.x) model family. It was captured
// from a Test thread's session_meta so the override below changes only the
// user-communication guidance and leaves every tool, sandbox and planning rule
// exactly as Codex ships it. Refresh the file when the bridge host upgrades
// Codex; the unit test pins its checksum so a refresh is always deliberate.
//
//go:embed assets/codex-default-base-instructions.md
var codexDefaultBaseInstructions string

// publicConversationSection replaces Codex's "Preamble messages" and "Sharing
// progress updates" guidance for application-managed conversations. Those
// sections teach a terminal coding assistant to announce its next tool call
// ("now checking the API route definitions"), which is exactly the technical
// narration a business user should never see. The base prompt travels on the
// system/instructions channel, so this is the only place where the rule
// outranks that habit; the application's developer instructions still own the
// detailed public-communication policy and remain in force.
const publicConversationSection = `## Talking with the user while you work

In this product you work for a business user inside a conversation, not for a developer watching a terminal. Everything you say before the final answer is public conversation with that user. It is never a log of your actions, and the generic coding-assistant habit of announcing the next tool call does not apply here.

### The first message of a turn

- When a request needs research, tools, documents or more than a quick reply, your first output is one or two sentences that show you understood what the user wants and what you will deliver. Send it before the first tool call, before reading any instructions or capability notes, and before planning in detail. Lead with the user's goal in their terms; your approach, if it appears at all, comes second and stays non-technical. Do not end it with what you will look at, check or confirm first.
- If you have nothing to add yet, restate the request accurately in your own words; a fast, faithful restatement beats a clever one. Keep the user's scope, constraints and exclusions exactly; do not add goals or promise actions that were not requested.
- Write it in the language of the user's latest message (Chinese for a Chinese request), never in the language of these instructions or of tool output. The same applies to every later public message and to the final answer: sources, tool results and these instructions may be English while the user's conversation is not; never drift.
- Never mention the workspace, capabilities, skills, instructions, credentials, environment, tools, files, formats, checks or any preparation. The user cannot see your tools and does not need to.
- Greetings, quick questions and one-line requests get a direct answer with no opening message.

Shape of a good first message (do not copy the wording; match the user's language and request):

- 「你想把团队零散的协作方式整理成一份每周能照着执行的安排，我先按减少临时选题和反复改稿来设计，再给你可以直接用的版本。」
- "You want a landing page headline that speaks to developers without dropping the claims you already make; I'll draft two directions and explain the trade-off."

### Messages between tool calls

- Speak only when you have something for the user: a finding from the material, a decision you are taking and why, a trade-off, or a limitation that changes what they will receive. Silence is better than a message that only describes your next action.
- Never announce that you are about to read, look up, check, confirm, prepare, convert, save, write or deliver something, in any wording, and never name tools, files, paths, commands, credentials or infrastructure. If a step fails in a way that changes the outcome, explain the consequence for the user and the alternative, not the mechanics.
- Exploring the environment is silent: listing folders, reading skill or capability notes, checking which tools, search or network access exist, and verifying credentials or delivery mechanics. When the next step is discovery or preparation, send nothing at all. Messages like these are wrong in every language and must never be written or paraphrased: 「我先看一下这个工作区里可用的文档能力」「我看一下环境里有没有可用的检索能力」「我先确认一下文档交付能力是否可用」「网络可用。」 "Let me check which tools are available." "I'll confirm the document capability first."
- Delivery mechanics are silent too: creating, writing, converting, saving or attaching a document, delivery frames, receipts and format checks. The user sees the finished deliverable itself. Never write messages like 「方案正文写好了，现在把它做成文档」「文档已创建，我把完整方案写入交付帧」 "The draft is ready, converting it into the document now." If you speak at that point, state the deliverable's key decision or an open question the user must settle.
- Do not add tool calls, model calls or stages just to have something to report. Keep the final answer complete and self-contained; progress messages never replace any part of it.
`

const (
	preambleSectionStart = "## Responsiveness\n"
	preambleSectionEnd   = "## Planning\n"
	progressSectionStart = "## Sharing progress updates\n"
	progressSectionEnd   = "## Presenting your work and final message\n"
)

var (
	publicConversationBaseOnce sync.Once
	publicConversationBaseText string
	publicConversationBaseErr  error
)

// publicConversationBaseInstructions returns the Codex base prompt with the
// coding-assistant communication sections replaced. It is composed once; a
// layout mismatch is reported instead of shipping a partially edited prompt.
func publicConversationBaseInstructions() (string, error) {
	publicConversationBaseOnce.Do(func() {
		publicConversationBaseText, publicConversationBaseErr = composePublicConversationBase(
			codexDefaultBaseInstructions, publicConversationSection)
	})
	return publicConversationBaseText, publicConversationBaseErr
}

func composePublicConversationBase(base, section string) (string, error) {
	out, err := replaceBetweenMarkers(base, preambleSectionStart, preambleSectionEnd,
		strings.TrimSpace(section)+"\n\n")
	if err != nil {
		return "", err
	}
	out, err = replaceBetweenMarkers(out, progressSectionStart, progressSectionEnd, "")
	if err != nil {
		return "", err
	}
	return out, nil
}

// replaceBetweenMarkers replaces [start, end) with replacement, keeping the end
// marker. Both markers must appear exactly once in order.
func replaceBetweenMarkers(text, start, end, replacement string) (string, error) {
	a := strings.Index(text, start)
	if a < 0 || strings.Index(text[a+len(start):], start) >= 0 {
		return "", fmt.Errorf("codex base instructions: marker %q must appear exactly once", strings.TrimSpace(start))
	}
	rel := strings.Index(text[a+len(start):], end)
	if rel < 0 {
		return "", fmt.Errorf("codex base instructions: marker %q not found after %q",
			strings.TrimSpace(end), strings.TrimSpace(start))
	}
	b := a + len(start) + rel
	return text[:a] + replacement + text[b:], nil
}

// baseInstructionsOverride selects the public-conversation base prompt for
// application-managed conversations. The trusted control plane marks those by
// supplying developer instructions on the runtime lane before the thread is
// created; plain bridge sessions keep Codex's native coding-assistant prompt.
func (s *appServerSession) baseInstructionsOverride() string {
	s.runtimeMu.RLock()
	managed := strings.TrimSpace(s.runtime.DeveloperInstructions) != ""
	s.runtimeMu.RUnlock()
	if !managed {
		return ""
	}
	text, err := publicConversationBaseInstructions()
	if err != nil {
		slog.Warn("codex: public conversation base instructions unavailable; keeping native base", "error", err)
		return ""
	}
	return text
}
