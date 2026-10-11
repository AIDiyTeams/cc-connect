package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/chenhg5/cc-connect/core"
)

// A conversation hands work to the product's AI employees through Portal's CMO
// API. The Agent decides whether, to whom and what; this tool only validates the
// structured arguments and runs the Skills-OL helper outside the Agent sandbox
// with the turn's own authority, which never passes through model arguments.
const teamCoordinationToolName = "tomako_team_coordination"

// The helper bounds its Portal request to 30 seconds; this also covers Node startup.
var teamCoordinationTimeout = 45 * time.Second

const (
	teamCoordinationOutputLimit = 256 * 1024
	teamCoordinationErrorLimit  = 16 * 1024
	// Codex writes the base instructions and every dynamic tool schema into
	// the first rollout line; a larger line is treated as unreadable.
	rolloutSessionMetaLimit = 8 * 1024 * 1024
)

var (
	// The same shapes the helper and Portal accept.
	teamCoordinationIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	teamCoordinationPlanPattern = regexp.MustCompile(`^wp-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// Portal writes the CMO API with raw scope IDs and the helper compares them
	// URI-encoded, so only IDs that need no encoding can match on both sides.
	teamCoordinationScopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
)

// The fields each operation carries to the helper; every other schema field
// must be absent or empty for that operation.
var teamCoordinationOperationFields = map[string]map[string]bool{
	"context": {},
	"handoff": {"sourceMessageId": true, "contextMessageIds": true, "employeeId": true, "executionKind": true, "title": true, "brief": true},
	"status":  {"planId": true},
	"message": {"planId": true, "sourceMessageId": true},
}

var teamCoordinationSchemaFields = map[string]bool{
	"employeeId": true, "planId": true, "executionKind": true, "title": true, "brief": true,
	"sourceMessageId": true, "contextMessageIds": true,
}

// A thread keeps the dynamic tools it started with: Codex restores them from the
// thread's session metadata on resume and ignores new ones. A conversation that
// began before this tool existed reaches the same operations through the helper
// the tool wraps, and is told so only while that can be the case.
const teamCoordinationHelperFallback = "Team coordination fallback: if " + teamCoordinationToolName +
	" is not among your tools in this conversation (a conversation keeps the tools it started with), " +
	"perform the same CMO operations with the Skills-OL helper instead: " +
	"CMO_API='<trusted [CMO_API=...] value of this turn>' node \"${SKILLS_OL_DIR:?}/cmo-handoff.mjs\" " +
	"context|handoff|status <planId>|message <planId>. The helper attaches current authority: never read, print " +
	"or pass tokens or nonces, or hand-write HTTP. handoff takes {sourceMessageId,contextMessageIds,employeeId," +
	"executionKind,title,brief} and message takes {sourceMessageId} as one JSON object on stdin, built with a " +
	"JSON library or quoted heredoc; never shell-interpolate user text. Exit 0 prints the receipt; otherwise " +
	"stderr gives the HTTP status, code and reason."

func teamCoordinationDynamicTool() map[string]any {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return map[string]any{
		"type": "function", "name": teamCoordinationToolName, "deferLoading": false,
		"description": "Coordinate this product's AI employees from this conversation. operation=context returns the hired team " +
			"(each employee's id, kind, title and capabilities), the user source message ids and current handoff receipts. " +
			"operation=handoff submits one task the user explicitly asked an employee to do and returns a receipt " +
			"(accepted means queued, not started or finished). operation=status reads a handoff by planId; " +
			"operation=message forwards a later user message to that handoff. Credentials and request signing are handled by the platform.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"operation"},
			"properties": map[string]any{
				"operation":       map[string]any{"type": "string", "enum": []string{"context", "handoff", "status", "message"}},
				"employeeId":      text("handoff: the employee id from operation=context."),
				"planId":          text("status and message: the wp-<uuid> planId from a receipt."),
				"executionKind":   map[string]any{"type": "string", "enum": []string{"conversation", "content", "kol_discovery"}},
				"title":           text("handoff: short task title."),
				"brief":           text("Complete task brief in the conversation language"),
				"sourceMessageId": text("handoff and message: the user source message id."),
				"contextMessageIds": map[string]any{"type": "array", "maxItems": 30,
					"items": map[string]any{"type": "string"}, "description": "handoff: other relevant user source message ids."},
			},
		},
	}
}

// cmoCoordinationAuthorized: Portal sets the CMO API exactly where it issues the
// cmo.coordinate capability, so both must be present and agree with the task scope.
func cmoCoordinationAuthorized(runtime core.SessionRuntime) bool {
	return runtime.TaskID != "" && strings.TrimSpace(runtime.ImageCapabilityToken) != "" && cmoAPIMatchesScope(runtime)
}

func cmoAPIMatchesScope(runtime core.SessionRuntime) bool {
	if !teamCoordinationScopePattern.MatchString(runtime.WorkspaceID) || !teamCoordinationScopePattern.MatchString(runtime.BrandID) {
		return false
	}
	api, err := url.Parse(runtime.CmoAPI)
	return err == nil && api.Scheme == "https" && api.Host != "" && api.User == nil && api.Opaque == "" &&
		api.RawQuery == "" && !api.ForceQuery && api.Fragment == "" && api.RawFragment == "" &&
		api.EscapedPath() == "/api/workspaces/"+runtime.WorkspaceID+"/products/"+runtime.BrandID+"/cmo"
}

// teamCoordinationScript returns Skills-OL's CMO helper; without it the tool is not offered.
func (s *appServerSession) teamCoordinationScript() string {
	return s.webReaderScript("cmo-handoff.mjs")
}

// Dedicated analysis workflows keep exactly the tools they were designed with.
func (s *appServerSession) teamCoordinationToolAvailable() bool {
	if s.isBrandAnalysisRuntime() || s.isUserVoiceArchiveRuntime() || s.isUserVoiceJudgmentRuntime() {
		return false
	}
	s.runtimeMu.RLock()
	authorized := cmoCoordinationAuthorized(s.runtime)
	s.runtimeMu.RUnlock()
	return authorized && s.teamCoordinationScript() != ""
}

func dynamicToolsInclude(params map[string]any, name string) bool {
	tools, _ := params["dynamicTools"].([]map[string]any)
	for _, tool := range tools {
		if tool["name"] == name {
			return true
		}
	}
	return false
}

func (s *appServerSession) resumedThreadHasTeamTool(threadID, rolloutPath string) bool {
	if rolloutPath == "" && threadID != "" {
		rolloutPath = findSessionFile(threadID, s.codexHome)
	}
	return rolloutHasDynamicTool(rolloutPath, teamCoordinationToolName)
}

// rolloutHasDynamicTool reports whether the thread's session metadata, which a
// resume restores, holds the named top-level function. Unreadable means no.
func rolloutHasDynamicTool(path, name string) bool {
	if path == "" {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, rolloutSessionMetaLimit)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			DynamicTools []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"dynamic_tools"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "session_meta" {
		return false
	}
	for _, tool := range entry.Payload.DynamicTools {
		// Canonical specs say "function"; legacy flat specs have no type and no namespace.
		if tool.Name == name && (tool.Type == "function" || (tool.Type == "" && tool.Namespace == "")) {
			return true
		}
	}
	return false
}

type teamCoordinationHandoff struct {
	SourceMessageID   string   `json:"sourceMessageId"`
	ContextMessageIDs []string `json:"contextMessageIds"`
	EmployeeID        string   `json:"employeeId"`
	ExecutionKind     string   `json:"executionKind"`
	Title             string   `json:"title"`
	Brief             string   `json:"brief"`
}

// teamCoordinationRequest maps validated arguments onto the helper's command
// line and stdin. It checks structure only; Portal owns authorization.
func teamCoordinationRequest(arguments map[string]any) (operation string, args []string, stdin []byte, err error) {
	operation, _ = arguments["operation"].(string)
	fields, known := teamCoordinationOperationFields[operation]
	if !known {
		return "", nil, nil, fmt.Errorf("operation must be context, handoff, status or message")
	}
	for key, value := range arguments {
		if key == "operation" {
			continue
		}
		if !teamCoordinationSchemaFields[key] {
			return "", nil, nil, fmt.Errorf("unsupported argument: %s", key)
		}
		if !fields[key] && !emptyToolArgument(value) {
			return "", nil, nil, fmt.Errorf("%s is not used by operation=%s", key, operation)
		}
	}
	switch operation {
	case "context":
		return operation, nil, nil, nil
	case "status":
		plan, err := teamCoordinationPlanID(arguments["planId"])
		return operation, []string{plan}, nil, err
	case "message":
		plan, err := teamCoordinationPlanID(arguments["planId"])
		if err != nil {
			return "", nil, nil, err
		}
		source, err := teamCoordinationID(arguments["sourceMessageId"], "sourceMessageId")
		if err != nil {
			return "", nil, nil, err
		}
		stdin, _ = json.Marshal(map[string]string{"sourceMessageId": source})
		return operation, []string{plan}, stdin, nil
	}
	var handoff teamCoordinationHandoff
	if handoff.SourceMessageID, err = teamCoordinationID(arguments["sourceMessageId"], "sourceMessageId"); err != nil {
		return "", nil, nil, err
	}
	if handoff.EmployeeID, err = teamCoordinationID(arguments["employeeId"], "employeeId"); err != nil {
		return "", nil, nil, err
	}
	handoff.ExecutionKind, _ = arguments["executionKind"].(string)
	if handoff.ExecutionKind != "conversation" && handoff.ExecutionKind != "content" && handoff.ExecutionKind != "kol_discovery" {
		return "", nil, nil, fmt.Errorf("executionKind must be conversation, content or kol_discovery")
	}
	if handoff.Title, err = teamCoordinationText(arguments["title"], "title", 200); err != nil {
		return "", nil, nil, err
	}
	if handoff.Brief, err = teamCoordinationText(arguments["brief"], "brief", 32_000); err != nil {
		return "", nil, nil, err
	}
	handoff.ContextMessageIDs = []string{}
	if value := arguments["contextMessageIds"]; value != nil {
		ids, ok := value.([]any)
		if !ok || len(ids) > 30 {
			return "", nil, nil, fmt.Errorf("contextMessageIds must be an array of at most 30 IDs")
		}
		for _, raw := range ids {
			id, err := teamCoordinationID(raw, "contextMessageIds[]")
			if err != nil {
				return "", nil, nil, err
			}
			handoff.ContextMessageIDs = append(handoff.ContextMessageIDs, id)
		}
	}
	stdin, _ = json.Marshal(handoff)
	return operation, nil, stdin, nil
}

func emptyToolArgument(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	}
	return false
}

func teamCoordinationID(value any, field string) (string, error) {
	id, _ := value.(string)
	id = strings.TrimSpace(id)
	if !teamCoordinationIDPattern.MatchString(id) {
		return "", fmt.Errorf("%s must be an ID from operation=context or this turn", field)
	}
	return id, nil
}

func teamCoordinationPlanID(value any) (string, error) {
	plan, _ := value.(string)
	plan = strings.TrimSpace(plan)
	if !teamCoordinationPlanPattern.MatchString(plan) {
		return "", fmt.Errorf("planId must be a receipt planId (wp-<uuid>)")
	}
	return plan, nil
}

// Same bound as the helper: JavaScript string length, i.e. UTF-16 code units.
func teamCoordinationText(value any, field string, max int) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" || len(utf16.Encode([]rune(text))) > max {
		return "", fmt.Errorf("%s must be non-empty text up to %d characters", field, max)
	}
	return text, nil
}

type teamCoordinationCall struct {
	cmd       *exec.Cmd
	ctx       context.Context
	cancel    context.CancelFunc
	cleanup   func()
	operation string
	secrets   []string
}

// prepareTeamCoordination snapshots the turn's authority before any work starts:
// a later turn may rotate the session file but never changes this call's owner.
func (s *appServerSession) prepareTeamCoordination(arguments map[string]any) (*teamCoordinationCall, error) {
	unavailable := fmt.Errorf("team coordination is not available for this turn; state that blocker instead of retrying")
	if !s.teamCoordinationToolAvailable() {
		return nil, unavailable
	}
	operation, args, stdin, err := teamCoordinationRequest(arguments)
	if err != nil {
		return nil, err
	}
	script := s.teamCoordinationScript()
	s.runtimeMu.RLock()
	runtime := s.runtime
	s.runtimeMu.RUnlock()
	if script == "" || !cmoCoordinationAuthorized(runtime) {
		return nil, unavailable
	}
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil || path == "" {
		return nil, fmt.Errorf("current team coordination authority is unavailable")
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, teamCoordinationTimeout)
	cmd := exec.CommandContext(ctx, "node", append([]string{script, operation}, args...)...)
	// The helper's own trusted directory, never a workspace the Agent can write.
	cmd.Dir = filepath.Dir(script)
	cmd.Env = core.MergeEnv(os.Environ(), append(append([]string(nil), s.extraEnv...),
		"TOMAKO_TASK_ENV_FILE="+path, "CMO_API="+runtime.CmoAPI))
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.WaitDelay = 2 * time.Second
	var secrets []string
	for _, value := range []string{runtime.MachineCapabilityToken, runtime.ImageCapabilityToken, runtime.DocumentCapabilityToken,
		runtime.EmployeeCommandCapabilityToken, runtime.ProductUpdateCapabilityToken, runtime.TaskAuthorityEnvelopeB64} {
		if value = strings.TrimSpace(value); len(value) >= 8 {
			secrets = append(secrets, value)
		}
	}
	return &teamCoordinationCall{cmd: cmd, ctx: ctx, cancel: cancel, cleanup: func() { removeTaskRuntimeEnv(path) },
		operation: operation, secrets: secrets}, nil
}

// run returns the helper's JSON receipt, or a concise error with Portal's HTTP
// status, code and reason. Neither ever carries the turn's credentials.
func (c *teamCoordinationCall) run() (string, error) {
	defer c.cancel()
	defer c.cleanup()
	stdout := &limitedBuffer{limit: teamCoordinationOutputLimit}
	stderr := &limitedBuffer{limit: teamCoordinationErrorLimit}
	c.cmd.Stdout = stdout
	c.cmd.Stderr = stderr
	if err := c.cmd.Run(); err != nil {
		if errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("team coordination %s timed out before Portal answered. Reads can be repeated; "+
				"a handoff or message may already be accepted, so read status or retry it once with the same IDs "+
				"(Portal returns the same plan)", c.operation)
		}
		if c.ctx.Err() != nil {
			return "", fmt.Errorf("team coordination %s was interrupted before a receipt arrived", c.operation)
		}
		return "", c.failure(stderr.Bytes())
	}
	if stdout.truncated {
		return "", fmt.Errorf("team coordination %s receipt exceeds the tool output limit", c.operation)
	}
	receipt := lastJSONLine(stdout.Bytes())
	if receipt == nil {
		return "", fmt.Errorf("team coordination %s returned no receipt; treat it as not done", c.operation)
	}
	return c.redact(string(receipt)), nil
}

func (c *teamCoordinationCall) failure(stderr []byte) error {
	var receipt struct {
		Status  any    `json:"status"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	line := lastJSONLine(stderr)
	if line == nil || json.Unmarshal(line, &receipt) != nil || strings.TrimSpace(receipt.Message) == "" {
		return fmt.Errorf("team coordination %s failed without a receipt; treat it as not done and state the blocker", c.operation)
	}
	var facts []string
	if receipt.Status != nil {
		facts = append(facts, fmt.Sprintf("HTTP %v", receipt.Status))
	}
	if receipt.Code != nil {
		facts = append(facts, fmt.Sprintf("code %v", receipt.Code))
	}
	message := limitRunes(c.redact(strings.Join(strings.Fields(receipt.Message), " ")), 500)
	if len(facts) == 0 {
		return fmt.Errorf("team coordination %s failed: %s", c.operation, message)
	}
	return fmt.Errorf("team coordination %s failed (%s): %s", c.operation, c.redact(strings.Join(facts, ", ")), message)
}

func (c *teamCoordinationCall) redact(text string) string {
	for _, secret := range c.secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}

func lastJSONLine(output []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(output), []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if line := bytes.TrimSpace(lines[i]); len(line) > 0 && json.Valid(line) {
			return line
		}
	}
	return nil
}
