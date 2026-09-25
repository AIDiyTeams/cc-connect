package core

import "testing"

func formInteractionEngine(t *testing.T, questions []UserQuestion) (*Engine, *recordingAgentSession) {
	t.Helper()
	e := newTestEngine()
	rec := &recordingAgentSession{}
	state := &interactiveState{
		agentSession: rec,
		pending: &pendingPermission{
			RequestID:     "form-call-1",
			InteractionID: "interaction-form",
			ToolName:      "AskUserQuestion",
			ToolInput:     map[string]any{"form": map[string]any{"title": "Facts"}},
			Questions:     questions,
			Resolved:      make(chan struct{}),
		},
	}
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = state
	e.interactiveMu.Unlock()
	return e, rec
}

func TestFormAnswersKeepTheirValuesForTheRequestingTool(t *testing.T) {
	e, rec := formInteractionEngine(t, []UserQuestion{
		{ID: "round_size", Question: "Round size", InputType: "number", Required: true},
		{ID: "channels", Question: "Channels", InputType: "multi_choice", MultiSelect: true,
			Options: []UserQuestionOption{{ID: "channels-option-1", Label: "Direct"}, {ID: "channels-option-2", Label: "Partners"}}},
		{ID: "website", Question: "Website", InputType: "url"},
	})
	if err := e.RespondInteraction("test:chat:user1", "interaction-form", "", map[string][]string{
		"round_size": {" 2000000 "},
		"channels":   {"channels-option-1", "Resellers"},
		"website":    {interactionSkippedAnswer},
	}); err != nil {
		t.Fatalf("RespondInteraction() error = %v", err)
	}
	answers, _ := rec.lastResult.UpdatedInput["answers"].(map[string]any)
	channels, _ := answers["channels"].([]string)
	if len(channels) != 2 || channels[0] != "Direct" || channels[1] != "Resellers" {
		t.Fatalf("channels = %#v", answers["channels"])
	}
	if round, _ := answers["round_size"].([]string); len(round) != 1 || round[0] != "2000000" {
		t.Fatalf("round_size = %#v", answers["round_size"])
	}
	if website, _ := answers["website"].([]string); len(website) != 1 || website[0] != interactionSkippedAnswer {
		t.Fatalf("a skipped field must stay recognisable as skipped: %#v", answers["website"])
	}
}

func TestARequiredFormFieldCannotBeSkipped(t *testing.T) {
	e, rec := formInteractionEngine(t, []UserQuestion{{ID: "round_size", Question: "Round size", InputType: "number", Required: true}})
	if err := e.RespondInteraction("test:chat:user1", "interaction-form", "", map[string][]string{
		"round_size": {interactionSkippedAnswer},
	}); err == nil {
		t.Fatal("skipping a required field must be refused")
	}
	if rec.lastID != "" {
		t.Fatal("a refused answer must not reach the agent")
	}
}
