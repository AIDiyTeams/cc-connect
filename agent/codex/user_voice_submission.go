package codex

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Only transports a submission. Portal remains the owner of schema/evidence validation.
type userVoiceSubmission struct {
	mu        sync.Mutex
	taskID    string
	startedAt string
	result    json.RawMessage
}

func (s *appServerSession) isUserVoiceJudgmentRuntime() bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtime.Scene == "growth_opportunity_user_voice_judge" &&
		strings.EqualFold(s.runtime.LogicalModel, "DEEPSEEK_V4_FLASH_SEARCH") && len(s.runtime.OutputSchema) > 0
}

func (s *appServerSession) userVoiceJudgmentTools() []map[string]any {
	var schema map[string]any
	_ = json.Unmarshal(s.outputSchema(), &schema)
	// Codex's dynamic schema serializer omits length/item bounds. Preserve their
	// meaning in descriptions; Portal still enforces the original full schema.
	describeUserVoiceSchemaLimits(schema)
	return []map[string]any{{"type": "function", "name": "submit_reviewed_user_voice_judgments", "deferLoading": false,
		"description": "After reading every supplied original, submit this frozen judgment batch. Calling this tool attests that you read the whole batch. Portal validates the submission; this does not publish or contact anyone. Call once, then finish without repeating the JSON.",
		"inputSchema": schema}}
}

func describeUserVoiceSchemaLimits(value any) {
	switch node := value.(type) {
	case map[string]any:
		var limits []string
		for _, key := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
			if limit, ok := node[key]; ok {
				limits = append(limits, fmt.Sprintf("%s=%v", key, limit))
			}
		}
		if len(limits) > 0 {
			description, _ := node["description"].(string)
			node["description"] = strings.TrimSpace(description + " Required bounds: " + strings.Join(limits, ", ") + ".")
		}
		for _, child := range node {
			describeUserVoiceSchemaLimits(child)
		}
	case []any:
		for _, child := range node {
			describeUserVoiceSchemaLimits(child)
		}
	}
}

func (s *appServerSession) recordUserVoiceJudgment(arguments map[string]any) error {
	if !s.isUserVoiceJudgmentRuntime() {
		return fmt.Errorf("judgment submission unavailable")
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > 1024*1024 {
		return fmt.Errorf("invalid or oversized judgment submission")
	}
	s.voiceSubmission.mu.Lock()
	defer s.voiceSubmission.mu.Unlock()
	if len(s.voiceSubmission.result) > 0 {
		return fmt.Errorf("batch already submitted; finish this turn")
	}
	if s.voiceSubmission.taskID == "" {
		return fmt.Errorf("missing task identity")
	}
	s.voiceSubmission.result = encoded
	return nil
}

func (s *appServerSession) userVoiceJudgmentResult() (string, error) {
	s.voiceSubmission.mu.Lock()
	defer s.voiceSubmission.mu.Unlock()
	if len(s.voiceSubmission.result) == 0 {
		return "", fmt.Errorf("USER_VOICE_SUBMISSION_MISSING: judgment ended without the required reviewed-submission tool call")
	}
	result, err := json.Marshal(map[string]any{"result": s.voiceSubmission.result, "submission": map[string]any{
		"tool": "submit_reviewed_user_voice_judgments", "taskId": s.voiceSubmission.taskID, "reviewStartedAt": s.voiceSubmission.startedAt,
	}})
	return string(result), err
}

func (s *appServerSession) resetUserVoiceSubmission(taskID string) {
	s.voiceSubmission.mu.Lock()
	defer s.voiceSubmission.mu.Unlock()
	s.voiceSubmission.taskID = taskID
	s.voiceSubmission.startedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.voiceSubmission.result = nil
}
