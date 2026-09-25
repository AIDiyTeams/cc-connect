package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func formArguments() map[string]any {
	return map[string]any{
		"title":       "Facts for your deck",
		"description": "Only what we could not find in your materials.",
		"fields": []any{
			map[string]any{"id": "round_size", "type": "number", "label": "How much are you raising?", "required": true, "min": float64(0)},
			map[string]any{"id": "stage", "type": "single_choice", "label": "Round", "options": []any{
				map[string]any{"label": "Pre-seed"}, map[string]any{"label": "Seed"}}},
			map[string]any{"id": "channels", "type": "multi_choice", "label": "Channels", "allowOther": true, "options": []any{
				map[string]any{"label": "Direct"}, map[string]any{"label": "Partners"}}},
			map[string]any{"id": "website", "type": "url", "label": "Website"},
		},
	}
}

func TestFormRequestBecomesTypedQuestions(t *testing.T) {
	form, questions, err := parseFormRequest(formArguments())
	if err != nil {
		t.Fatal(err)
	}
	if form.Title != "Facts for your deck" || len(questions) != 4 {
		t.Fatalf("form=%#v questions=%d", form, len(questions))
	}
	round := questions[0]
	if round.InputType != "number" || !round.Required || round.Min == nil || *round.Min != 0 || round.Question != "How much are you raising?" {
		t.Fatalf("round=%#v", round)
	}
	stage, channels := questions[1], questions[2]
	if stage.MultiSelect || len(stage.Options) != 2 || stage.Options[1].ID != "stage-option-2" || stage.Options[1].Label != "Seed" {
		t.Fatalf("stage=%#v", stage)
	}
	if !channels.MultiSelect || !channels.AllowOther {
		t.Fatalf("channels=%#v", channels)
	}
	if questions[3].Required || questions[3].InputType != "url" {
		t.Fatalf("website=%#v", questions[3])
	}
}

func TestInvalidFormsAreRefusedBeforeReachingTheUser(t *testing.T) {
	cases := map[string]func(map[string]any){
		"no title":     func(a map[string]any) { a["title"] = " " },
		"no fields":    func(a map[string]any) { a["fields"] = []any{} },
		"duplicate id": func(a map[string]any) { a["fields"].([]any)[1].(map[string]any)["id"] = "round_size" },
		"bad id":       func(a map[string]any) { a["fields"].([]any)[0].(map[string]any)["id"] = "Round Size" },
		"unknown type": func(a map[string]any) { a["fields"].([]any)[0].(map[string]any)["type"] = "file" },
		"choice no option": func(a map[string]any) {
			a["fields"].([]any)[1].(map[string]any)["options"] = []any{map[string]any{"label": "Seed"}}
		},
		"min over max": func(a map[string]any) {
			field := a["fields"].([]any)[0].(map[string]any)
			field["min"], field["max"] = float64(10), float64(1)
		},
	}
	for name, mutate := range cases {
		arguments := formArguments()
		mutate(arguments)
		if _, _, err := parseFormRequest(arguments); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	many := formArguments()
	fields := []any{}
	for i := 0; i < formMaxFields+1; i++ {
		fields = append(fields, map[string]any{"id": "f" + strings.Repeat("x", i), "type": "text", "label": "Field"})
	}
	many["fields"] = fields
	if _, _, err := parseFormRequest(many); err == nil {
		t.Error("too many fields: expected an error")
	}
}

func TestFormAnswersReturnTypedValuesAndNameSkippedFields(t *testing.T) {
	_, questions, err := parseFormRequest(formArguments())
	if err != nil {
		t.Fatal(err)
	}
	text, ok := formToolResult(questions, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{
		"round_size": []string{"2,000,000"},
		"stage":      []string{"Seed"},
		"channels":   []string{"Direct", "Resellers"},
		"website":    []string{"__tomako_skipped__"},
	}}})
	if !ok {
		t.Fatal("submitted form must succeed")
	}
	var decoded struct {
		Status  string         `json:"status"`
		Values  map[string]any `json:"values"`
		Skipped []string       `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "submitted" || decoded.Values["round_size"] != float64(2000000) || decoded.Values["stage"] != "Seed" {
		t.Fatalf("decoded=%#v", decoded)
	}
	if channels, _ := decoded.Values["channels"].([]any); len(channels) != 2 || channels[1] != "Resellers" {
		t.Fatalf("channels=%#v", decoded.Values["channels"])
	}
	if len(decoded.Skipped) != 1 || decoded.Skipped[0] != "website" {
		t.Fatalf("skipped=%#v", decoded.Skipped)
	}
	if text, ok := formToolResult(questions, core.PermissionResult{Behavior: "deny"}); ok || !strings.Contains(text, "cancelled") {
		t.Fatalf("closed form must report cancellation: %s", text)
	}
}

func TestFormToolJoinsConversationToolsWithoutReplacingThem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tomako-image-tool.mjs"), []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &appServerSession{ctx: ctx, cancel: cancel, workDir: dir, extraEnv: []string{"SKILLS_OL_DIR=" + dir}, events: make(chan core.Event, 1),
		runtime: core.SessionRuntime{TaskID: "cmsg-a", WorkspaceID: "ws-a", BrandID: "b-a", ImageCapabilityToken: "image-a", ChatSessionID: "csess-a"}}
	s.alive.Store(true)
	tools, _ := s.threadRequestParams()["dynamicTools"].([]map[string]any)
	names := []string{}
	for _, tool := range tools {
		names = append(names, tool["name"].(string))
	}
	if strings.Join(names, ",") != "tomako_generate_image,tomako_image_status,tomako_request_form" {
		t.Fatalf("tools=%v", names)
	}
	s.runtime.ChatSessionID = ""
	for _, tool := range s.threadRequestParams()["dynamicTools"].([]map[string]any) {
		if tool["name"] == formToolName {
			t.Fatal("a task without a conversation received the form tool")
		}
	}
	s.runtime.ChatSessionID = "csess-a"
	s.runtime.Scene = "brand_analysis"
	for _, tool := range s.threadRequestParams()["dynamicTools"].([]map[string]any) {
		if tool["name"] == formToolName {
			t.Fatal("dedicated analysis tool set changed")
		}
	}
}

func TestFormToolWaitsForTheUserAndAnswersTheCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stdin := &lockedWriteCloser{}
	s := &appServerSession{ctx: ctx, cancel: cancel, stdin: stdin, events: make(chan core.Event, 4), pendingApprovals: map[string]chan core.PermissionResult{},
		runtime: core.SessionRuntime{ChatSessionID: "csess-a"}}
	s.alive.Store(true)
	s.handleFormToolCall(json.RawMessage(`7`), "call-1", formArguments())
	event := <-s.events
	if event.Type != core.EventPermissionRequest || event.ToolName != "AskUserQuestion" || event.Form == nil || len(event.Questions) != 4 {
		t.Fatalf("event=%#v", event)
	}
	if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{
		"round_size": []string{"500000"}, "stage": []string{"Seed"}, "channels": []string{"Direct"}, "website": []string{"__tomako_skipped__"},
	}}}); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, stdin)), &response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	if response["id"] != float64(7) || result["success"] != true {
		t.Fatalf("response=%#v", response)
	}
	items, _ := result["contentItems"].([]any)
	if len(items) != 1 || !strings.Contains(items[0].(map[string]any)["text"].(string), `"skipped":["website"]`) {
		t.Fatalf("items=%#v", items)
	}
}
