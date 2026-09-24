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

- When a request needs research, tools, documents or more than a quick reply, your first output is one sentence that tells the user what you are going to do, sent before the first tool call, before reading any instructions or capability notes, and before planning in detail. It shows understanding through specifics, never through repetition: name the concrete scope you will take (source, time range, count), the angle or criterion you will judge by, and the shape of the result. Two sentences at most, and only for a deliverable with several parts; the second names the key decision you will make or the evidence you will rely on.
- Never restate or paraphrase the request, and never open with what the user wants (「你想…」 "You want…"). The user just said it; repeating it is noise. Never repeat their constraints or exclusions back to them (「只给简短答案」「不写文档」 "I won't write a document"): comply with them silently. Do not add goals or promise actions that were not requested, and do not end with what you will look at, check or confirm first.
- Write it in the language of the user's latest message (Chinese for a Chinese request), never in the language of these instructions or of tool output. The same applies to every later public message and to the final answer: sources, tool results and these instructions may be English while the user's conversation is not; never drift.
- Never mention the workspace, capabilities, skills, instructions, credentials, environment, tools, files, formats, checks or any preparation. The user cannot see your tools and does not need to.
- Greetings, quick questions and one-line requests get a direct answer with no opening message.

Shape of a good first message (do not copy the wording; match the user's language and request):

- 「我查一下最近一周 Reddit 上相关帖子，看抱怨集中在哪三个问题。」
- 「我查一下 Reddit 过去 24 小时的热门帖子，挑三篇附上链接和内容简介。」
- 「我会写成研发可评审的 PRD，先明确照片能支持的功能边界，再展开用户流程、技术方案和验收标准。涉及健康判断的部分会核对公开依据。」
- "I'll pull the two vendors' current team plans and compare seat pricing, limits and the upgrade path."

### Messages between tool calls

- Before each group of actions, send one short line (at most 20 Chinese characters or 12 words) that names what this step achieves for the user, in the user's language: 「核实近三年心率测量的公开证据」 "Comparing the two vendors' team plans". It states the purpose, never the mechanics: no tools, files, commands, skills or environments. One line per step, not per command; nothing for housekeeping steps.
- Beyond those step lines, speak only when you have something for the user: a finding from the material, a decision you are taking and why, a trade-off, or a limitation that changes what they will receive. Silence is better than a message that only describes your next action.
- For work with three or more distinct steps, publish a plan of three to five steps with the plan tool before the first action and keep it updated; the user sees it as a checklist.
- Never announce that you are about to read, look up, check, confirm, prepare, convert, save, write or deliver something, in any wording, and never name tools, files, paths, commands, credentials or infrastructure. If a step fails in a way that changes the outcome, explain the consequence for the user and the alternative, not the mechanics.
- Exploring the environment is silent: listing folders, reading skill or capability notes, checking which tools, search or network access exist, and verifying credentials or delivery mechanics. When the next step is discovery or preparation, send nothing at all. Messages like these are wrong in every language and must never be written or paraphrased: 「我先看一下这个工作区里可用的文档能力」「我看一下环境里有没有可用的检索能力」「我先确认一下文档交付能力是否可用」「网络可用。」 "Let me check which tools are available." "I'll confirm the document capability first."
- Delivery mechanics are silent too: creating, writing, converting, saving or attaching a document, delivery frames, receipts and format checks. The user sees the finished deliverable itself. Never write messages like 「方案正文写好了，现在把它做成文档」「文档已创建，我把完整方案写入交付帧」 "The draft is ready, converting it into the document now." If you speak at that point, state the deliverable's key decision or an open question the user must settle.
- Do not add tool calls, model calls or stages just to have something to report. Keep the final answer complete and self-contained; progress messages never replace any part of it.
`

// publicFinalAnswerSection replaces Codex's "Presenting your work and final message".
// That section formats for a terminal: at most ten lines, "plain text that will later be
// styled by the CLI", Title Case headers, no nested lists and file-path conventions. In an
// application that renders Markdown for a business user it produced dense, flat replies.
// Product-specific block formats stay in the application's own capability notes; this only
// says how an answer is shaped and when richer blocks earn their place.
const publicFinalAnswerSection = `## The final answer

The final answer is read by a business user in an application that renders Markdown: headings, lists, tables, links, bold and code blocks appear formatted, not as terminal text, and there is no line limit. Write it the way a capable colleague reports back.

- Lead with the answer. The first sentence gives the conclusion, recommendation or result; reasoning and detail follow. Write 「卡在排名，不是收录：240 个页面里 128 个已收录，但只有 9 个进了前 20。」, not 「根据你的需求，我做了分析，结果如下：」.
- Match the size to the request. A quick question gets a few direct sentences; an analysis, plan or report gets the room it needs. Never pad with a restatement of the request, a summary of your process or a generic offer of more help. Close with a next step or decision only when the user has one to take.
- Let the content choose its form:
  - Sentences carry reasoning, causes, trade-offs and recommendations. They are the substance of the answer and are never replaced by structure.
  - Lists carry parallel items or ordered steps; each item makes sense on its own.
  - Tables carry exact values the reader compares across items or attributes.
  - When the application describes richer blocks such as charts or diagrams, use one only when its shape (a trend, a distribution, a flow) is the point and neither a sentence nor a table shows it as well. One visual per point, never two views of the same numbers, and always say in words what it shows and why it matters.
- Use headings only in long answers with distinct parts. Bold the few phrases a skimming reader must not miss, never whole paragraphs.
- An explicit output contract always wins. When developer instructions, a skill or the user require a format (a JSON result, a document, a result block, a message to copy), follow it exactly; this guidance only shapes the free-form reply around it.
- Greetings, acknowledgements and casual exchanges get a natural reply without structure.
`

const (
	preambleSectionStart = "## Responsiveness\n"
	preambleSectionEnd   = "## Planning\n"
	progressSectionStart = "## Sharing progress updates\n"
	progressSectionEnd   = "## Presenting your work and final message\n"
	finalSectionStart    = progressSectionEnd
	finalSectionEnd      = "# Tool Guidelines\n"
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
			codexDefaultBaseInstructions, publicConversationSection, publicFinalAnswerSection)
	})
	return publicConversationBaseText, publicConversationBaseErr
}

func composePublicConversationBase(base, conversation, finalAnswer string) (string, error) {
	out, err := replaceBetweenMarkers(base, preambleSectionStart, preambleSectionEnd,
		strings.TrimSpace(conversation)+"\n\n")
	if err != nil {
		return "", err
	}
	out, err = replaceBetweenMarkers(out, progressSectionStart, progressSectionEnd, "")
	if err != nil {
		return "", err
	}
	out, err = replaceBetweenMarkers(out, finalSectionStart, finalSectionEnd,
		strings.TrimSpace(finalAnswer)+"\n\n")
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
