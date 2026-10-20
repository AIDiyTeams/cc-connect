package codex

import (
	"regexp"
	"strings"
)

// DeepSeek occasionally writes a tool call into its message instead of making it:
// csess-b9075636ae2044d1 (10-21) ended with
//
//	<tomako_browser>
//	<action>tabs</action>
//	<path>local-chrome</path>
//	</tomako_browser>
//	</｜｜DSML｜｜ calls>
//
// The gateway saw prose, Codex completed the turn, and the user got a promise to look
// at their inbox with nothing done. DSML is DeepSeek's own tool-call markup; a closing
// tag after a complete answer is harmless residue, so only an opening invoke/calls tag,
// or residue next to an element shaped like a call (an element whose content starts
// with a child element), counts as a call that never ran.
var (
	dsmlMarker     = regexp.MustCompile(`</?\s*[｜|]{1,2}\s*DSML\s*[｜|]{1,2}[^>\n]*>`)
	dsmlOpenCall   = regexp.MustCompile(`<\s*[｜|]{1,2}\s*DSML\s*[｜|]{1,2}\s*(?:invoke|function_calls|tool_calls)\b`)
	callShapedNode = regexp.MustCompile(`<([a-z][a-z0-9_]*)>\s*<([a-zA-Z][a-zA-Z0-9_]*)>[^<]*</([a-zA-Z][a-zA-Z0-9_]*)>`)
)

// writtenToolCallPrompt is the follow-up a model receives after it wrote a tool call
// as text, so nothing ran.
const writtenToolCallPrompt = "Your previous message wrote a tool call as plain text instead of calling the tool, so it did not run " +
	"and the user has not received a result. Make that call now through the tool interface, never as text in a message, " +
	"and finish the user's request. Do not repeat work that is already complete."

// splitWrittenToolCall reports whether text contains a tool call written as text and
// returns the prose before it, which is what the model meant to say to the user.
func splitWrittenToolCall(text string) (prose string, written bool) {
	cut := -1
	if loc := dsmlOpenCall.FindStringIndex(text); loc != nil {
		cut = loc[0]
	} else if dsmlMarker.MatchString(text) {
		for _, m := range callShapedNode.FindAllStringSubmatchIndex(text, -1) {
			if text[m[4]:m[5]] == text[m[6]:m[7]] {
				cut = m[0]
				break
			}
		}
	}
	if cut < 0 {
		return text, false
	}
	return strings.TrimSpace(text[:cut]), true
}
