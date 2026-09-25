package codex

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// The form tool lets the Agent ask for several facts at once on one card in the
// conversation, instead of a string of choice questions. It rides the same
// pending-interaction path as request_user_input, so answering, skipping and
// stopping behave the same way; only the tool result is shaped for forms.

const formToolName = "tomako_request_form"

const (
	formMaxFields  = 12
	formMaxOptions = 20
)

var formFieldID = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

var formInputTypes = map[string]bool{
	"text": true, "textarea": true, "number": true, "single_choice": true,
	"multi_choice": true, "date": true, "url": true,
}

func formDynamicTool() map[string]any {
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	option := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"label"},
		"properties": map[string]any{"label": text(80), "description": map[string]any{"type": "string", "maxLength": 200}}}
	field := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "type", "label"},
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "pattern": formFieldID.String(), "description": "Stable key for this answer, e.g. round_size."},
			"type":        map[string]any{"type": "string", "enum": []string{"text", "textarea", "number", "single_choice", "multi_choice", "date", "url"}},
			"label":       text(120),
			"required":    map[string]any{"type": "boolean", "description": "Only for facts the task cannot proceed without. Optional fields may be left blank."},
			"helpText":    map[string]any{"type": "string", "maxLength": 300},
			"placeholder": map[string]any{"type": "string", "maxLength": 120},
			"options":     map[string]any{"type": "array", "minItems": 2, "maxItems": formMaxOptions, "items": option, "description": "Required for single_choice and multi_choice."},
			"allowOther":  map[string]any{"type": "boolean", "description": "Choice fields: let the user type an answer that is not listed."},
			"min":         map[string]any{"type": "number"},
			"max":         map[string]any{"type": "number"},
			"maxLength":   map[string]any{"type": "integer", "minimum": 1, "maximum": 4000},
		}}
	return map[string]any{"type": "function", "name": formToolName, "deferLoading": false,
		"description": "Show the user a small form in the conversation and wait for their answers. Use it when a task needs several specific facts only the user can provide (for example figures for a business plan) and asking one by one would be slow or easy to get wrong. Ask only for what the task truly needs, prefill nothing you would have to guess, and mark as required only what blocks the work. The result lists each submitted value and which fields were skipped; never invent a value for a skipped field. Do not use it for a single yes/no or choice question.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"title", "fields"},
			"properties": map[string]any{
				"title":       text(80),
				"description": map[string]any{"type": "string", "maxLength": 500},
				"submitLabel": map[string]any{"type": "string", "maxLength": 20},
				"fields":      map[string]any{"type": "array", "minItems": 1, "maxItems": formMaxFields, "items": field},
			}},
	}
}

// formToolAvailable: forms are answered on a conversation card, so only chat
// sessions get the tool; background tasks have nobody to fill them in.
func (s *appServerSession) formToolAvailable() bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.TrimSpace(s.runtime.ChatSessionID) != ""
}

// parseFormRequest validates the Agent's form and turns each field into a question.
func parseFormRequest(arguments map[string]any) (core.InteractionForm, []core.UserQuestion, error) {
	var form core.InteractionForm
	title := strings.TrimSpace(stringArg(arguments["title"]))
	if title == "" || len([]rune(title)) > 80 {
		return form, nil, fmt.Errorf("form title is required and must be at most 80 characters")
	}
	form.Title = title
	form.Description = limitRunes(strings.TrimSpace(stringArg(arguments["description"])), 500)
	form.SubmitLabel = limitRunes(strings.TrimSpace(stringArg(arguments["submitLabel"])), 20)
	rawFields, _ := arguments["fields"].([]any)
	if len(rawFields) == 0 || len(rawFields) > formMaxFields {
		return form, nil, fmt.Errorf("a form has between 1 and %d fields", formMaxFields)
	}
	seen := map[string]bool{}
	questions := make([]core.UserQuestion, 0, len(rawFields))
	for index, raw := range rawFields {
		field, _ := raw.(map[string]any)
		if field == nil {
			return form, nil, fmt.Errorf("field %d is not an object", index+1)
		}
		id := strings.TrimSpace(stringArg(field["id"]))
		if !formFieldID.MatchString(id) || seen[id] {
			return form, nil, fmt.Errorf("field %d needs a unique id of lowercase letters, digits and underscores", index+1)
		}
		seen[id] = true
		inputType := strings.TrimSpace(stringArg(field["type"]))
		if !formInputTypes[inputType] {
			return form, nil, fmt.Errorf("field %q has an unsupported type", id)
		}
		label := strings.TrimSpace(stringArg(field["label"]))
		if label == "" || len([]rune(label)) > 120 {
			return form, nil, fmt.Errorf("field %q needs a label of at most 120 characters", id)
		}
		question := core.UserQuestion{
			ID:          id,
			Question:    label,
			InputType:   inputType,
			Required:    field["required"] == true,
			HelpText:    limitRunes(strings.TrimSpace(stringArg(field["helpText"])), 300),
			Placeholder: limitRunes(strings.TrimSpace(stringArg(field["placeholder"])), 120),
			MultiSelect: inputType == "multi_choice",
		}
		choice := inputType == "single_choice" || inputType == "multi_choice"
		if choice {
			options, _ := field["options"].([]any)
			if len(options) < 2 || len(options) > formMaxOptions {
				return form, nil, fmt.Errorf("choice field %q needs 2 to %d options", id, formMaxOptions)
			}
			for _, rawOption := range options {
				option, _ := rawOption.(map[string]any)
				optionLabel := strings.TrimSpace(stringArg(option["label"]))
				if optionLabel == "" || len([]rune(optionLabel)) > 80 {
					return form, nil, fmt.Errorf("choice field %q has an option without a short label", id)
				}
				question.Options = append(question.Options, core.UserQuestionOption{
					ID:          fmt.Sprintf("%s-option-%d", id, len(question.Options)+1),
					Label:       optionLabel,
					Description: limitRunes(strings.TrimSpace(stringArg(option["description"])), 200),
				})
			}
			question.AllowOther = field["allowOther"] == true
		}
		if inputType == "number" {
			question.Min = numberArg(field["min"])
			question.Max = numberArg(field["max"])
			if question.Min != nil && question.Max != nil && *question.Min > *question.Max {
				return form, nil, fmt.Errorf("field %q has min greater than max", id)
			}
		}
		if inputType == "text" || inputType == "textarea" {
			if length := numberArg(field["maxLength"]); length != nil && *length >= 1 && *length <= 4000 {
				question.MaxLength = int(*length)
			}
		}
		questions = append(questions, question)
	}
	return form, questions, nil
}

// formToolResult reads the submitted answers back into typed values the Agent can use directly.
func formToolResult(questions []core.UserQuestion, result core.PermissionResult) (string, bool) {
	if !strings.EqualFold(result.Behavior, "allow") {
		return `{"status":"cancelled","message":"The form was closed without answers. Do not assume any values; ask again only if the task still needs them."}`, false
	}
	answers, _ := result.UpdatedInput["answers"].(map[string]any)
	values := map[string]any{}
	skipped := []string{}
	for _, question := range questions {
		raw := appServerRequestUserInputAnswerValues(answers[question.ID])
		if len(raw) == 0 || (len(raw) == 1 && strings.TrimSpace(raw[0]) == "__tomako_skipped__") {
			skipped = append(skipped, question.ID)
			continue
		}
		switch question.InputType {
		case "multi_choice":
			values[question.ID] = raw
		case "number":
			if number, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(raw[0]), ",", ""), 64); err == nil && !math.IsInf(number, 0) && !math.IsNaN(number) {
				values[question.ID] = number
			} else {
				values[question.ID] = raw[0]
			}
		default:
			values[question.ID] = raw[0]
		}
	}
	encoded, _ := json.Marshal(map[string]any{
		"status":  "submitted",
		"values":  values,
		"skipped": skipped,
		"note":    "These are the user's own answers. Fields in skipped were left blank on purpose: do not fill them in yourself.",
	})
	return string(encoded), true
}

// handleFormToolCall shows the form as a conversation interaction and answers
// the tool call once the user submits, skips or the session stops.
func (s *appServerSession) handleFormToolCall(rawID json.RawMessage, callID string, arguments map[string]any) {
	form, questions, err := parseFormRequest(arguments)
	if err != nil {
		s.writeDynamicToolResponse(rawID, false, err.Error())
		return
	}
	requestID := "form-" + strings.TrimSpace(callID)
	if requestID == "form-" {
		requestID = fmt.Sprintf("form-%s", strings.Trim(string(rawID), `"`))
	}
	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	rawInput := map[string]any{"form": form, "questions": questions}
	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "AskUserQuestion",
		ToolInput:    form.Title,
		ToolInputRaw: rawInput,
		Questions:    questions,
		Form:         &form,
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
		text, success := formToolResult(questions, result)
		s.writeDynamicToolResponse(rawID, success, text)
	}()
}

func stringArg(value any) string {
	text, _ := value.(string)
	return text
}

func numberArg(value any) *float64 {
	switch v := value.(type) {
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return &f
		}
	}
	return nil
}

func limitRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
