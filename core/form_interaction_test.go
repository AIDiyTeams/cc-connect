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

type authorityRecordingSession struct {
	recordingAgentSession
	order     []string
	authority SessionRuntime
}

func (s *authorityRecordingSession) RefreshCapabilityAuthority(runtime SessionRuntime) error {
	s.order = append(s.order, "refresh")
	s.authority = runtime
	return nil
}

func (s *authorityRecordingSession) RespondPermission(id string, res PermissionResult) error {
	s.order = append(s.order, "answer")
	return s.recordingAgentSession.RespondPermission(id, res)
}

func authorityInteractionEngine(session AgentSession) *Engine {
	e := newTestEngine()
	e.interactiveMu.Lock()
	e.interactiveStates["test:chat:user1"] = &interactiveState{
		agentSession: session,
		pending: &pendingPermission{
			RequestID:     "form-call-late",
			InteractionID: "interaction-late",
			ToolName:      "AskUserQuestion",
			ToolInput:     map[string]any{"form": map[string]any{"title": "Facts"}},
			Questions:     []UserQuestion{{ID: "audience", Question: "Audience", InputType: "text"}},
			Resolved:      make(chan struct{}),
		},
	}
	e.interactiveMu.Unlock()
	return e
}

func TestALateAnswerRefreshesToolAuthorityBeforeTheTurnResumes(t *testing.T) {
	session := &authorityRecordingSession{}
	e := authorityInteractionEngine(session)
	if err := e.RespondInteractionWithAuthority("test:chat:user1", "interaction-late", "",
		map[string][]string{"audience": {"Developers"}},
		&SessionRuntime{TaskID: "cmsg-1", EmployeeCommandCapabilityToken: "fresh"}); err != nil {
		t.Fatalf("RespondInteractionWithAuthority() error = %v", err)
	}
	if len(session.order) != 2 || session.order[0] != "refresh" || session.order[1] != "answer" {
		t.Fatalf("order = %v, want the credentials replaced before the turn resumes", session.order)
	}
	if session.authority.EmployeeCommandCapabilityToken != "fresh" {
		t.Fatalf("authority = %#v", session.authority)
	}
}

func TestAnAnswerWithoutAuthorityLeavesTheTurnCredentialsAlone(t *testing.T) {
	session := &authorityRecordingSession{}
	e := authorityInteractionEngine(session)
	if err := e.RespondInteraction("test:chat:user1", "interaction-late", "",
		map[string][]string{"audience": {"Developers"}}); err != nil {
		t.Fatalf("RespondInteraction() error = %v", err)
	}
	if len(session.order) != 1 || session.order[0] != "answer" {
		t.Fatalf("order = %v, want only the answer", session.order)
	}
}

func TestARefusedAnswerDoesNotTouchTheTurnCredentials(t *testing.T) {
	session := &authorityRecordingSession{}
	e := authorityInteractionEngine(session)
	if err := e.RespondInteractionWithAuthority("test:chat:user1", "interaction-late", "",
		map[string][]string{}, &SessionRuntime{TaskID: "cmsg-1"}); err == nil {
		t.Fatal("an answer missing its field must be refused")
	}
	if len(session.order) != 0 {
		t.Fatalf("order = %v, want nothing applied for a refused answer", session.order)
	}
}
