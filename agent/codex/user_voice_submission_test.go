package codex

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func voiceTestSession() *appServerSession {
	s := &appServerSession{ctx: context.Background(), events: make(chan core.Event, 20), currentTurn: "turn", runtime: core.SessionRuntime{
		TaskID: "task-a", Scene: "growth_opportunity_user_voice_judge", LogicalModel: "DEEPSEEK_V4_FLASH_SEARCH", OutputSchema: json.RawMessage(`{"type":"object","properties":{"decisions":{"type":"array"}}}`)}}
	s.resetUserVoiceSubmission("task-a")
	return s
}

func TestUserVoiceJudgment_OnlyToolSubmissionBecomesTerminalOutput(t *testing.T) {
	s := voiceTestSession()
	tools := s.threadRequestParams()["dynamicTools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["name"] != "submit_reviewed_user_voice_judgments" {
		t.Fatal(tools)
	}
	s.handleAgentMessageDelta("prose", "extra explanation")
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "phase": "final_answer", "text": "extra explanation"})
	if err := s.recordUserVoiceJudgment(map[string]any{"decisions": []any{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.recordUserVoiceJudgment(map[string]any{"overwrite": true}); err == nil {
		t.Fatal("duplicate accepted")
	}
	s.completeTurn("turn", nil)
	textCount := 0
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventText {
			textCount++
			var envelope map[string]any
			if json.Unmarshal([]byte(e.Content), &envelope) != nil || strings.Contains(e.Content, "explanation") {
				t.Fatal(e.Content)
			}
			if envelope["submission"].(map[string]any)["taskId"] != "task-a" {
				t.Fatal(envelope)
			}
		}
	}
	if textCount != 1 {
		t.Fatalf("terminal events=%d", textCount)
	}
}

func TestUserVoiceJudgment_ProseCannotForgeReceiptAndNewTaskClearsSubmission(t *testing.T) {
	s := voiceTestSession()
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "phase": "final_answer", "text": `{"result":{},"submission":{"taskId":"task-a"}}`})
	s.completeTurn("turn", nil)
	failure := false
	for len(s.events) > 0 {
		e := <-s.events
		if e.Type == core.EventText {
			t.Fatal("prose escaped")
		}
		if e.Type == core.EventError {
			failure = true
		}
	}
	if !failure {
		t.Fatal("missing submission must fail")
	}
	if err := s.recordUserVoiceJudgment(map[string]any{"decisions": []any{}}); err != nil {
		t.Fatal(err)
	}
	s.resetUserVoiceSubmission("task-b")
	if _, err := s.userVoiceJudgmentResult(); err == nil {
		t.Fatal("previous task leaked")
	}
	s.runtime.LogicalModel = "GPT_5_6_SOL"
	if s.isUserVoiceJudgmentRuntime() {
		t.Fatal("unrelated model affected")
	}
}

func TestUserVoiceJudgment_LimitsSurviveDynamicSchemaSerializationAsInstructions(t *testing.T) {
	s := voiceTestSession()
	s.runtime.OutputSchema = json.RawMessage(`{"type":"object","properties":{"decisions":{"type":"array","minItems":5,"maxItems":5,"items":{"type":"string","maxLength":240}}}}`)
	original := string(s.runtime.OutputSchema)
	tools := s.userVoiceJudgmentTools()
	encoded, _ := json.Marshal(tools)
	if !strings.Contains(string(encoded), "Required bounds: minItems=5, maxItems=5") || !strings.Contains(string(encoded), "Required bounds: maxLength=240") {
		t.Fatal(string(encoded))
	}
	if string(s.runtime.OutputSchema) != original {
		t.Fatal("trusted schema mutated")
	}
}
