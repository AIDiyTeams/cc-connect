package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestBrowserHelpRequest_BuildsOneQuestionWithTheStep(t *testing.T) {
	action, question, err := browserHelpRequest(map[string]any{
		"kind": "sign_in", "site": "kittl.com", "browser": "local-chrome",
		"message": "请在你的 Chrome 里登录 Kittl，我才能截到真实工作台。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if action != (core.InteractionUserAction{Kind: "sign_in", Site: "kittl.com", Browser: "local-chrome"}) {
		t.Fatalf("action = %+v", action)
	}
	if question.ID != "browser_help" || len(question.Options) != 2 || question.Options[0].Label != "已完成，请继续" {
		t.Fatalf("question = %+v", question)
	}
	_, english, _ := browserHelpRequest(map[string]any{"kind": "take_over", "site": "x.com", "browser": "cloud", "message": "Please enter the code."})
	if english.Options[0].Label != "Done, continue" {
		t.Fatalf("labels follow the message language, got %+v", english.Options)
	}
}

func TestBrowserHelpRequest_RejectsInvalidSteps(t *testing.T) {
	cases := []map[string]any{
		{"kind": "pay", "site": "a.com", "browser": "cloud", "message": "m"},
		{"kind": "sign_in", "site": "a.com", "browser": "phone", "message": "m"},
		{"kind": "connect_local_chrome", "site": "a.com", "browser": "cloud", "message": "m"},
		{"kind": "sign_in", "site": "", "browser": "cloud", "message": "m"},
		{"kind": "sign_in", "site": "a.com", "browser": "cloud", "message": ""},
	}
	for _, arguments := range cases {
		if _, _, err := browserHelpRequest(arguments); err == nil {
			t.Fatalf("expected an error for %+v", arguments)
		}
	}
}

func TestBrowserHelpResult_DoneIsOnlyAReportToVerify(t *testing.T) {
	_, question, _ := browserHelpRequest(map[string]any{"kind": "sign_in", "site": "kittl.com", "browser": "cloud", "message": "Sign in please."})
	read := func(answer any) map[string]any {
		text, _ := browserHelpResult(question, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{"browser_help": answer}}})
		var payload map[string]any
		if err := json.Unmarshal([]byte(text), &payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	done := read([]any{browserHelpDoneID})
	if done["status"] != "user_reported_done" || !strings.Contains(done["next"].(string), "Verify") {
		t.Fatalf("done = %+v", done)
	}
	if read([]any{"Done, continue"})["status"] != "user_reported_done" {
		t.Fatal("the option label counts as the same answer")
	}
	if read([]any{browserHelpDeclineID})["status"] != "declined" {
		t.Fatal("decline")
	}
	if read([]any{"__tomako_skipped__"})["status"] != "skipped" {
		t.Fatal("skip")
	}
	if reply := read([]any{"I have no account"}); reply["status"] != "replied" || reply["reply"] != "I have no account" {
		t.Fatalf("free text = %+v", reply)
	}
	if text, ok := browserHelpResult(question, core.PermissionResult{Behavior: "deny"}); ok || !strings.Contains(text, "cancelled") {
		t.Fatalf("closed request = %s", text)
	}
}

func TestBridgeInteractionCarriesUserAction(t *testing.T) {
	request := core.InteractionRequest{UserAction: &core.InteractionUserAction{Kind: "connect_local_chrome", Site: "kittl.com", Browser: "local-chrome"}}
	encoded, _ := json.Marshal(request)
	if !strings.Contains(string(encoded), `"userAction":{"kind":"connect_local_chrome","site":"kittl.com","browser":"local-chrome"}`) {
		t.Fatalf("encoded = %s", encoded)
	}
}
