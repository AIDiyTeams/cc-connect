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

func TestFormToolReturnsFilesByFieldWithoutInventingTypedValues(t *testing.T) {
	file := `<user_attachments>[{"url":"https://files.example.com/chart.png","fileName":"chart.png","mimeType":"image/png"}]</user_attachments>`
	questions := []core.UserQuestion{{ID: "budget", InputType: "number"}, {ID: "actual", InputType: "number"}, {ID: "notes", InputType: "text"}}
	result, ok := formToolResult(questions, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{"budget": []string{file}, "actual": []string{"42\n" + file}, "notes": []string{"__tomako_skipped__"}}}})
	if !ok {
		t.Fatal(result)
	}
	var payload struct {
		Values      map[string]any                          `json:"values"`
		Attachments map[string][]core.InteractionAttachment `json:"attachments"`
		Skipped     []string                                `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatal(err)
	}
	if _, invented := payload.Values["budget"]; invented || payload.Values["actual"] != float64(42) || len(payload.Attachments["budget"]) != 1 || len(payload.Attachments["actual"]) != 1 || len(payload.Skipped) != 1 || payload.Skipped[0] != "notes" {
		t.Fatal(result)
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
	if strings.Join(names, ",") != "tomako_generate_image,tomako_image_status,tomako_request_form,tomako_request_browser_help" {
		t.Fatalf("tools=%v", names)
	}
	s.runtime.ChatSessionID = ""
	for _, tool := range s.threadRequestParams()["dynamicTools"].([]map[string]any) {
		if tool["name"] == formToolName || tool["name"] == browserHelpToolName {
			t.Fatalf("a task without a conversation received %v", tool["name"])
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

func TestBrowserFormPreservesTakeoverAndReturnsAnswersWithoutExecutionReceipt(t *testing.T) {
	for _, path := range []string{"cloud", "local-chrome"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			stdin := &lockedWriteCloser{}
			s := &appServerSession{ctx: ctx, cancel: cancel, stdin: stdin, events: make(chan core.Event, 4),
				pendingApprovals: map[string]chan core.PermissionResult{}, runtime: core.SessionRuntime{ChatSessionID: "csess-a"}}
			s.alive.Store(true)
			args := formArguments()
			args["browser"] = map[string]any{"site": "example.com", "path": path}
			s.handleFormToolCall(json.RawMessage(`8`), "browser-form-1", args)
			event := <-s.events
			if event.NonBlocking || event.Form == nil || event.UserAction == nil ||
				event.UserAction.Kind != "take_over" || event.UserAction.Browser != path || event.UserAction.Site != "example.com" {
				t.Fatalf("form must wait with the existing scoped takeover action: %#v", event)
			}
			raw := event.ToolInputRaw
			if raw["userAction"] != event.UserAction {
				t.Fatal("persisted interaction lost the browser action")
			}
			if err := s.RespondPermission(event.RequestID, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{
				"answers": map[string]any{"round_size": []string{"42"}, "website": []string{"__tomako_skipped__"}},
			}}); err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, stdin)), &response); err != nil {
				t.Fatal(err)
			}
			result := response["result"].(map[string]any)
			text := result["contentItems"].([]any)[0].(map[string]any)["text"].(string)
			var payload map[string]any
			if err := json.Unmarshal([]byte(text), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["status"] != "submitted" || payload["values"].(map[string]any)["round_size"] != float64(42) ||
				payload["browser"].(map[string]any)["path"] != path || !strings.Contains(payload["next"].(string), "HUMAN_CONTROL") {
				t.Fatalf("answer should retain context and require verification: %s", text)
			}
			if _, ok := payload["values"].(map[string]any)["website"]; ok {
				t.Fatal("skipped value must not be filled")
			}
		})
	}
}

func TestBrowserFormRejectsInvalidHandoffBeforeOpeningInteraction(t *testing.T) {
	for _, value := range []any{"cloud", map[string]any{"site": "example.com"},
		map[string]any{"site": "", "path": "cloud"}, map[string]any{"site": "example.com", "path": "other"}} {
		stdin := &lockedWriteCloser{}
		s := &appServerSession{stdin: stdin, events: make(chan core.Event, 1), pendingApprovals: map[string]chan core.PermissionResult{}}
		args := formArguments()
		args["browser"] = value
		s.handleFormToolCall(json.RawMessage(`9`), "bad-form", args)
		if len(s.events) != 0 || len(s.pendingApprovals) != 0 {
			t.Fatalf("invalid context created a pending form: %#v", value)
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, stdin)), &response); err != nil {
			t.Fatal(err)
		}
		if response["result"].(map[string]any)["success"] != false {
			t.Fatalf("invalid context accepted: %#v", value)
		}
	}
}

func TestNewFormsExplicitlyChooseBrowserContextWhileLegacyAndGeneralFormsStillWork(t *testing.T) {
	// In the real browser CUJ an optional browser property was omitted, so a
	// working form lost its takeover entry. Make the routing choice explicit in
	// the schema, while keeping already-running threads' old calls compatible.
	schema := formDynamicTool()["inputSchema"].(map[string]any)
	found := false
	for _, field := range schema["required"].([]string) {
		found = found || field == "browser"
	}
	if !found {
		t.Fatal("new browser forms can silently omit the takeover context")
	}
	general := formArguments()
	for _, explicit := range []bool{false, true} {
		if explicit {
			general["browser"] = nil
		}
		action, err := formBrowserAction(general)
		if err != nil || action != nil {
			t.Fatalf("general/legacy form acquired a browser action: %#v %v", action, err)
		}
	}
}

func TestBrowserFormCancellationAndSkipDoNotReportWebsiteCompletion(t *testing.T) {
	_, questions, _ := parseFormRequest(formArguments())
	action := &core.InteractionUserAction{Kind: "take_over", Site: "example.com", Browser: "cloud"}
	for _, behavior := range []string{"allow", "deny"} {
		text, success := browserFormToolResult(questions, core.PermissionResult{Behavior: behavior}, action)
		var result map[string]any
		if err := json.Unmarshal([]byte(text), &result); err != nil {
			t.Fatal(err)
		}
		if success != (behavior == "allow") || !strings.Contains(result["next"].(string), "do not reopen this form automatically") {
			t.Fatalf("lost cancellation/skip semantics: %s", text)
		}
		if behavior == "allow" && (len(result["values"].(map[string]any)) != 0 || len(result["skipped"].([]any)) != len(questions)) {
			t.Fatalf("skipped form invented values: %s", text)
		}
		if behavior == "deny" && result["status"] != "cancelled" {
			t.Fatalf("cancelled form reported success: %s", text)
		}
	}
}
