package codex

import (
	"encoding/json"
	"testing"
)

// The 10-21 csess-b9075636ae2044d1 ending, verbatim.
const writtenBrowserCall = "[[tomako:conversation-language=zh-CN]]\n大哥，我先看一下你浏览器里 Gmail 的当前状态，确认那封 Instagram 的邮件后帮你删掉。\n\n" +
	"<tomako_browser>\n<action>tabs</action>\n<path>local-chrome</path>\n</tomako_browser>\n</｜｜DSML｜｜ calls>"

// Endings seen on Test 10-18..10-20: a complete answer followed by DSML closing tags.
const answerWithDSMLResidue = "<tomako_public_response>\n大哥，验证完成了。\n</tomako_public_response>\n" +
	"</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"

func TestSplitWrittenToolCall(t *testing.T) {
	for _, tc := range []struct {
		name, text, prose string
		written           bool
	}{
		{"browser call written as text", writtenBrowserCall,
			"[[tomako:conversation-language=zh-CN]]\n大哥，我先看一下你浏览器里 Gmail 的当前状态，确认那封 Instagram 的邮件后帮你删掉。", true},
		{"opening DSML invoke", "好的。\n<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"tomako_browser\">", "好的。", true},
		{"answer with closing residue only", answerWithDSMLResidue, answerWithDSMLResidue, false},
		{"answer with a structured envelope", "<tomako_conversation_title>\n{\"title\":\"删除邮件\",\"emoji\":\"\"}\n</tomako_conversation_title>\n已删除。", "", false},
		{"markup in prose without DSML", "示例：<item><name>x</name></item>", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prose, written := splitWrittenToolCall(tc.text)
			if written != tc.written {
				t.Fatalf("written=%v, want %v", written, tc.written)
			}
			if tc.written && prose != tc.prose {
				t.Fatalf("prose=%q, want %q", prose, tc.prose)
			}
			if !tc.written && prose != tc.text {
				t.Fatalf("text without a written call must be returned unchanged, got %q", prose)
			}
		})
	}
}

func TestFinalReplyFollowUp_WrittenToolCallAsksOnceToMakeTheCall(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "said", "phase": "final_answer"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "said", "phase": "final_answer", "text": writtenBrowserCall})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	shown, _, results, errors := drainReplyEvents(s)
	if results != 0 || errors != 0 {
		t.Fatalf("a call written as text must keep the reply open, results=%d errors=%d", results, errors)
	}
	want := "[[tomako:conversation-language=zh-CN]]\n大哥，我先看一下你浏览器里 Gmail 的当前状态，确认那封 Instagram 的邮件后帮你删掉。"
	if shown != want {
		t.Fatalf("shown=%q, want only the prose before the written call", shown)
	}

	start := startFollowUpTurn(t, s, stdin, "turn-2")
	if input := start.Params["input"].([]any); input[0].(map[string]any)["text"] != writtenToolCallPrompt {
		t.Fatalf("follow-up input = %#v, want the written-call prompt", input)
	}
	s.handleNotification("turn/started", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2"}}`))
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "done", "phase": "final_answer"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "done", "phase": "final_answer", "text": "已删除。"})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-2","status":"completed"}}`))
	final, _, results, errors := drainReplyEvents(s)
	if final != "\n\n已删除。" || results != 1 || errors != 0 {
		t.Fatalf("final=%q results=%d errors=%d, want the follow-up answer to complete the reply", final, results, errors)
	}
	if got := len(turnStartLines(t, stdin)); got != 1 {
		t.Fatalf("turn/start requests = %d, want one", got)
	}
}

func TestFinalReplyFollowUp_DSMLResidueAfterACompleteAnswerIsNotContinued(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	s.handleItemStarted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer"})
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "id": "answer", "phase": "final_answer", "text": answerWithDSMLResidue})
	s.handleNotification("turn/completed", json.RawMessage(`{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}`))
	final, _, results, errors := drainReplyEvents(s)
	if final != answerWithDSMLResidue || results != 1 || errors != 0 {
		t.Fatalf("final=%q results=%d errors=%d, want the answer delivered as before", final, results, errors)
	}
	if got := len(turnStartLines(t, stdin)); got != 0 {
		t.Fatalf("turn/start requests = %d, want none", got)
	}
}
