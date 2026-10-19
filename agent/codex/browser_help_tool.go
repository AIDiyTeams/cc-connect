package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/chenhg5/cc-connect/core"
)

// The browser help tool asks the user to do a browser step only they can do
// (sign in or sign up with their own account, connect their own Chrome, take
// over the browser) and waits for them. Requesting it was previously possible
// only through the generic question tool, which every instruction layer asks
// the Agent to avoid unless blocked, so the Agent narrowed requests to what it
// could reach alone. It rides the same pending-interaction path as forms; the
// card carries the step so the app can offer the matching one-click action.

const browserHelpToolName = "tomako_request_browser_help"

const (
	browserHelpDoneID    = "browser_help-done"
	browserHelpDeclineID = "browser_help-decline"
)

var browserHelpKinds = map[string]bool{"sign_in": true, "connect_local_chrome": true, "take_over": true}
var browserHelpPaths = map[string]bool{"local-chrome": true, "cloud": true}

func browserHelpDynamicTool() map[string]any {
	text := func(max int, description string) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max, "description": description}
	}
	return map[string]any{"type": "function", "name": browserHelpToolName, "deferLoading": false,
		"description": "Ask the user to do a browser step only they can do, and wait for their answer. Steps: sign_in (sign in to or create their own account on a site), connect_local_chrome (connect their own Chrome through the Tomako Browser extension) and take_over (any other step in the browser you must not do yourself, such as a password, a verification code or accepting terms). The conversation shows a card with a one-click action: connecting Chrome opens the connection page, and cloud steps open the live browser for the user to take over. Check the current browser status and page first and do not ask for a sign-in the page already shows. For sign_in, use local-chrome whenever status shows it available: the user's own Chrome usually already has their logins, and Google sign-in is one click there. If this conversation is working in a cloud tab, close it and open the site with provider local-chrome before asking; use cloud only when local-chrome is not available. Whether the user has an account on the site cannot be seen from the page; asking is how to find out. Signing in or connecting usually takes the user a minute or two, and the request stays open until they answer. Use it as soon as you know a confirmed requirement of the user's request depends on such a step and what you can reach without it would not meet that requirement; keep any finished work. The answer only reports what the user did: verify it with browser status and the page before relying on it. If the user declines, deliver what you can and say which requirement is not met.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "site", "browser", "message"},
			"properties": map[string]any{
				"kind":    map[string]any{"type": "string", "enum": []string{"sign_in", "connect_local_chrome", "take_over"}},
				"site":    text(120, "The website or service, e.g. its domain."),
				"browser": map[string]any{"type": "string", "enum": []string{"local-chrome", "cloud"}, "description": "Where the user does the step: local-chrome whenever status shows it available, otherwise the conversation's cloud browser."},
				"message": text(300, "One or two sentences to the user, in the user's language: what to do, where, and what it unlocks in their request."),
			}},
	}
}

// browserHelpRequest validates the call and turns it into one question with two answers.
func browserHelpRequest(arguments map[string]any) (core.InteractionUserAction, core.UserQuestion, error) {
	action := core.InteractionUserAction{
		Kind:    strings.TrimSpace(stringArg(arguments["kind"])),
		Site:    strings.TrimSpace(stringArg(arguments["site"])),
		Browser: strings.TrimSpace(stringArg(arguments["browser"])),
	}
	if !browserHelpKinds[action.Kind] {
		return action, core.UserQuestion{}, fmt.Errorf("kind must be sign_in, connect_local_chrome or take_over")
	}
	if !browserHelpPaths[action.Browser] {
		return action, core.UserQuestion{}, fmt.Errorf("browser must be local-chrome or cloud")
	}
	if action.Kind == "connect_local_chrome" && action.Browser != "local-chrome" {
		return action, core.UserQuestion{}, fmt.Errorf("connect_local_chrome is done in local-chrome")
	}
	if action.Site == "" || len([]rune(action.Site)) > 120 {
		return action, core.UserQuestion{}, fmt.Errorf("site is required and must be at most 120 characters")
	}
	message := strings.TrimSpace(stringArg(arguments["message"]))
	if message == "" || len([]rune(message)) > 300 {
		return action, core.UserQuestion{}, fmt.Errorf("message is required and must be at most 300 characters")
	}
	done, decline, header := "Done, continue", "Not now, deliver what you have", "Browser"
	if containsHan(message) {
		done, decline, header = "已完成，请继续", "先不做，按现有材料交付", "需要你操作"
	}
	question := core.UserQuestion{
		ID:       "browser_help",
		Header:   header,
		Question: message,
		Options: []core.UserQuestionOption{
			{ID: browserHelpDoneID, Label: done},
			{ID: browserHelpDeclineID, Label: decline},
		},
	}
	return action, question, nil
}

func containsHan(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// browserHelpResult tells the Agent what the user chose without treating it as proof.
func browserHelpResult(question core.UserQuestion, result core.PermissionResult) (string, bool) {
	if !strings.EqualFold(result.Behavior, "allow") {
		return `{"status":"cancelled","next":"The request was closed without an answer. Do not assume the step was done."}`, false
	}
	answers, _ := result.UpdatedInput["answers"].(map[string]any)
	values := appServerRequestUserInputAnswerValues(answers[question.ID])
	answer := ""
	if len(values) > 0 {
		answer = strings.TrimSpace(values[0])
	}
	payload := map[string]any{}
	switch {
	case answer == "" || answer == "__tomako_skipped__":
		payload["status"] = "skipped"
		payload["next"] = "The user skipped this. Continue without the step: deliver what you can and say which requirement is not met."
	case answer == browserHelpDoneID || answer == question.Options[0].Label:
		payload["status"] = "user_reported_done"
		payload["next"] = "The user says the step is done. Verify before relying on it: check browser status and read the target page (signed in, connected, no longer under the user's control). If it is not, say what you saw and ask again only if the request still needs it."
	case answer == browserHelpDeclineID || answer == question.Options[1].Label:
		payload["status"] = "declined"
		payload["next"] = "Continue without the step: deliver what you can and say which requirement is not met; do not present substitutes as meeting it."
	default:
		payload["status"] = "replied"
		payload["reply"] = answer
		payload["next"] = "The user answered in their own words. Follow it, and verify any step they say is done with browser status and the page."
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded), true
}

// handleBrowserHelpToolCall shows the step as a conversation interaction and answers
// the tool call once the user answers, skips or the session stops.
func (s *appServerSession) handleBrowserHelpToolCall(rawID json.RawMessage, callID string, arguments map[string]any) {
	action, question, err := browserHelpRequest(arguments)
	if err != nil {
		s.writeDynamicToolResponse(rawID, false, err.Error())
		return
	}
	requestID := "browser-help-" + strings.TrimSpace(callID)
	if requestID == "browser-help-" {
		requestID = fmt.Sprintf("browser-help-%s", strings.Trim(string(rawID), `"`))
	}
	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	questions := []core.UserQuestion{question}
	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "AskUserQuestion",
		ToolInput:    question.Question,
		ToolInputRaw: map[string]any{"questions": questions, "userAction": action},
		Questions:    questions,
		UserAction:   &action,
	})

	go func() {
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()
		text, success := browserHelpResult(question, result)
		s.writeDynamicToolResponse(rawID, success, text)
	}()
}

// browserHelpToolAvailable: the step is answered by a person on a conversation card,
// with the same scope as forms; unattended runs and dedicated analysis workflows do not get it.
func (s *appServerSession) browserHelpToolAvailable() bool {
	return s.formToolAvailable() && !s.isBrandAnalysisRuntime() && !s.isUserVoiceArchiveRuntime() && !s.isUserVoiceJudgmentRuntime()
}
