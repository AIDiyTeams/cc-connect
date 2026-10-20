package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
	if action.Kind != "sign_in" || action.Site != "kittl.com" || action.Browser != "local-chrome" || action.Sites != nil {
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

func TestBrowserHelpRequest_ListsEverySiteOnce(t *testing.T) {
	action, _, err := browserHelpRequest(map[string]any{
		"kind": "sign_in", "browser": "local-chrome", "message": "请登录这几个网站。",
		"sites": []any{"producthunt.com", "startupbase.io", "ProductHunt.com", "alternativeto.net"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if action.Site != "producthunt.com" || strings.Join(action.Sites, ",") != "producthunt.com,startupbase.io,alternativeto.net" {
		t.Fatalf("action = %+v", action)
	}
	many := []any{}
	for i := 0; i <= browserHelpMaxSites; i++ {
		many = append(many, fmt.Sprintf("site%d.com", i))
	}
	if _, _, err := browserHelpRequest(map[string]any{"kind": "sign_in", "browser": "cloud", "message": "m", "sites": many}); err == nil {
		t.Fatal("more sites than one card can hold must be refused")
	}
}

func waitForWrittenMethod(t *testing.T, w *lockedWriteCloser, method string) rpcRequestLine {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(w.String(), "\n") {
			var request rpcRequestLine
			if json.Unmarshal([]byte(strings.TrimSpace(line)), &request) == nil && request.Method == method {
				return request
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s request written, buffer=%q", method, w.String())
	return rpcRequestLine{}
}

// The card does not pause the turn: the tool answers at once, and the user's answer
// is steered into the turn still running.
func TestBrowserHelpDoesNotPauseTheTurnAndSteersTheAnswerIn(t *testing.T) {
	s, stdin := finalReplyTestSession(t)
	s.handleBrowserHelpToolCall(json.RawMessage(`7`), "call-1", map[string]any{
		"kind": "sign_in", "browser": "local-chrome", "message": "我已在你的 Chrome 里打开这两个网站，登录后点完成。",
		"sites": []any{"producthunt.com", "startupbase.io"},
	})
	event := <-s.events
	if event.Type != core.EventPermissionRequest || !event.NonBlocking || event.UserAction == nil || len(event.UserAction.Sites) != 2 {
		t.Fatalf("event = %#v", event)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(waitForWrittenJSONLine(t, stdin)), &response); err != nil {
		t.Fatal(err)
	}
	result, _ := response["result"].(map[string]any)
	items, _ := result["contentItems"].([]any)
	if response["id"] != float64(7) || result["success"] != true || len(items) != 1 || !strings.Contains(items[0].(map[string]any)["text"].(string), `"status":"shown"`) {
		t.Fatalf("the tool must answer at once: %#v", response)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.AnswerDetached(event.RequestID, core.PermissionResult{Behavior: "allow", UpdatedInput: map[string]any{"answers": map[string]any{"browser_help": []any{browserHelpDoneID}}}})
	}()
	steer := waitForWrittenMethod(t, stdin, "turn/steer")
	text, _ := json.Marshal(steer.Params["input"])
	if steer.Params["threadId"] != "thread-1" || steer.Params["expectedTurnId"] != "turn-1" ||
		!strings.Contains(string(text), "producthunt.com, startupbase.io") || !strings.Contains(string(text), "user_reported_done") {
		t.Fatalf("steer = %+v", steer)
	}
	s.handleResponse(rpcResponseEnvelope{ID: float64(steer.ID), Result: json.RawMessage(`{"turnId":"turn-1"}`)})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.AnswerDetached(event.RequestID, core.PermissionResult{Behavior: "allow"}); err == nil {
		t.Fatal("a card is answered once")
	}
}

// After the turn has ended there is nothing to steer: the control plane continues instead.
func TestBrowserHelpAnswerWithoutARunningTurnIsRefused(t *testing.T) {
	s, _ := finalReplyTestSession(t)
	s.handleBrowserHelpToolCall(json.RawMessage(`8`), "call-2", map[string]any{
		"kind": "sign_in", "browser": "cloud", "message": "Please sign in.", "sites": []any{"x.com"},
	})
	event := <-s.events
	s.stateMu.Lock()
	s.currentTurn = ""
	s.stateMu.Unlock()
	if err := s.AnswerDetached(event.RequestID, core.PermissionResult{Behavior: "allow"}); err == nil || !strings.Contains(err.Error(), "no running turn") {
		t.Fatalf("err = %v", err)
	}
}
