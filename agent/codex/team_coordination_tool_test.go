package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const (
	testCmoAPI      = "https://test.tomako.ai/api/workspaces/ws-a/products/prod-a/cmo"
	testCmoToken    = "cmo-image-capability-secret"
	testPlanID      = "wp-0123abcd-4567-89ab-cdef-0123456789ab"
	fakeHelperProbe = `import {readFileSync} from 'node:fs';
let stdin = '';
for await (const chunk of process.stdin) stdin += chunk;
const file = readFileSync(process.env.TOMAKO_TASK_ENV_FILE, 'utf8');
console.log(JSON.stringify({argv: process.argv.slice(2), cmoApi: process.env.CMO_API, stdin,
  cwd: process.cwd(), authorityFile: file.includes("IMAGE_CAPABILITY_TOKEN='` + testCmoToken + `'")}));`
)

func cmoRuntime() core.SessionRuntime {
	return core.SessionRuntime{TaskID: "cmsg-cmo", WorkspaceID: "ws-a", BrandID: "prod-a", ChatSessionID: "csess-cmo",
		ImageCapabilityToken: testCmoToken, CmoAPI: testCmoAPI}
}

func teamToolSession(t *testing.T, helper string) *appServerSession {
	t.Helper()
	dir := t.TempDir()
	if helper != "" {
		if err := os.WriteFile(filepath.Join(dir, "cmo-handoff.mjs"), []byte(helper), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &appServerSession{ctx: ctx, cancel: cancel, workDir: t.TempDir(), extraEnv: []string{"SKILLS_OL_DIR=" + dir},
		events: make(chan core.Event, 8), runtime: cmoRuntime()}
	s.alive.Store(true)
	return s
}

func toolNames(params map[string]any) []string {
	tools, _ := params["dynamicTools"].([]map[string]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool["name"].(string))
	}
	return names
}

func TestTeamCoordinationToolOfferedOnlyWithTrustedCmoAuthority(t *testing.T) {
	s := teamToolSession(t, "// helper")
	if !dynamicToolsInclude(s.threadRequestParams(), teamCoordinationToolName) {
		t.Fatalf("CMO turn did not receive the team tool: %v", toolNames(s.threadRequestParams()))
	}
	schema := teamCoordinationDynamicTool()["inputSchema"].(map[string]any)
	operation := schema["properties"].(map[string]any)["operation"].(map[string]any)
	if strings.Join(operation["enum"].([]string), ",") != "context,handoff,status,message,outcome,review,report" ||
		strings.Join(schema["required"].([]string), ",") != "operation" {
		t.Fatalf("schema drifted from the documented operations: %#v", schema)
	}
	for name, mutate := range map[string]func(*core.SessionRuntime){
		"no CMO API":        func(r *core.SessionRuntime) { r.CmoAPI = "" },
		"no capability":     func(r *core.SessionRuntime) { r.ImageCapabilityToken = "" },
		"another product":   func(r *core.SessionRuntime) { r.CmoAPI = strings.Replace(testCmoAPI, "prod-a", "prod-b", 1) },
		"another workspace": func(r *core.SessionRuntime) { r.WorkspaceID = "ws-b" },
		"plain HTTP":        func(r *core.SessionRuntime) { r.CmoAPI = strings.Replace(testCmoAPI, "https:", "http:", 1) },
		"query":             func(r *core.SessionRuntime) { r.CmoAPI += "?as=other" },
		"credentials":       func(r *core.SessionRuntime) { r.CmoAPI = strings.Replace(testCmoAPI, "https://", "https://u:p@", 1) },
		"another API":       func(r *core.SessionRuntime) { r.CmoAPI = strings.TrimSuffix(testCmoAPI, "/cmo") + "/agent-update" },
		"no task":           func(r *core.SessionRuntime) { r.TaskID = "" },
		"brand analysis":    func(r *core.SessionRuntime) { r.Scene = "brand_analysis" },
		"encoded scope": func(r *core.SessionRuntime) {
			r.BrandID = "a:b"
			r.CmoAPI = strings.Replace(testCmoAPI, "prod-a", "a:b", 1)
		},
		"traversal in the API": func(r *core.SessionRuntime) { r.CmoAPI = strings.Replace(testCmoAPI, "/cmo", "/cmo/../cmo", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			s := teamToolSession(t, "// helper")
			mutate(&s.runtime)
			if dynamicToolsInclude(s.threadRequestParams(), teamCoordinationToolName) {
				t.Fatal("team tool offered without matching trusted CMO authority")
			}
			if _, err := s.prepareTeamCoordination(map[string]any{"operation": "context"}); err == nil {
				t.Fatal("a thread that still lists the tool must not execute without current authority")
			}
		})
	}
	if missing := teamToolSession(t, ""); dynamicToolsInclude(missing.threadRequestParams(), teamCoordinationToolName) {
		t.Fatal("team tool offered without the installed helper")
	}
}

func TestTeamCoordinationArgumentsMapToTheHelperContract(t *testing.T) {
	brief := "为 X 写三条发布推文，预算 $200，截止周五。'$(touch must-not-run)`literal`"
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		args      []string
		stdin     string
	}{
		{"context ignores empty fields", map[string]any{"operation": "context", "planId": "", "contextMessageIds": []any{}, "brief": nil}, nil, ""},
		{"handoff", map[string]any{"operation": "handoff", "sourceMessageId": "cmsg-user", "contextMessageIds": []any{"cmsg-1", " cmsg-2 "},
			"employeeId": "emp-content", "executionKind": "content", "title": "Launch posts", "brief": brief}, nil,
			`{"sourceMessageId":"cmsg-user","contextMessageIds":["cmsg-1","cmsg-2"],"employeeId":"emp-content","executionKind":"content","title":"Launch posts","brief":` + mustJSON(brief) + `}`},
		{"handoff without context ids", map[string]any{"operation": "handoff", "sourceMessageId": "cmsg-user", "employeeId": "emp-kol",
			"executionKind": "kol_discovery", "title": "YouTube creators", "brief": "Find creators"}, nil,
			`{"sourceMessageId":"cmsg-user","contextMessageIds":[],"employeeId":"emp-kol","executionKind":"kol_discovery","title":"YouTube creators","brief":"Find creators"}`},
		{"status", map[string]any{"operation": "status", "planId": testPlanID}, []string{testPlanID}, ""},
		{"outcome", map[string]any{"operation": "outcome", "planId": testPlanID, "outcomeId": "art-650c969ecb0b4227"},
			[]string{testPlanID, "art-650c969ecb0b4227"}, ""},
		{"message", map[string]any{"operation": "message", "planId": testPlanID, "sourceMessageId": "cmsg-later"}, []string{testPlanID},
			`{"sourceMessageId":"cmsg-later"}`},
		{"coordinator note", map[string]any{"operation": "message", "planId": testPlanID, "note": "Budget is 1.5M USD", "sourceMessageId": ""}, []string{testPlanID},
			`{"note":"Budget is 1.5M USD"}`},
		{"note with forwarded message", map[string]any{"operation": "message", "planId": testPlanID, "note": "Also this", "sourceMessageId": "cmsg-later"}, []string{testPlanID},
			`{"note":"Also this","sourceMessageId":"cmsg-later"}`},
		{"handoff with deliverables", map[string]any{"operation": "handoff", "sourceMessageId": "cmsg-user", "employeeId": "emp-content",
			"executionKind": "content", "title": "Launch posts", "brief": "Three posts", "deliverables": []any{" 3 X 帖子 ", "每条 1 张配图"}}, nil,
			`{"sourceMessageId":"cmsg-user","contextMessageIds":[],"employeeId":"emp-content","executionKind":"content","title":"Launch posts","brief":"Three posts","deliverables":["3 X 帖子","每条 1 张配图"]}`},
		{"accepted review", map[string]any{"operation": "review", "planId": testPlanID, "verdict": "accepted", "delivered": float64(3)},
			[]string{testPlanID}, `{"delivered":3,"verdict":"accepted"}`},
		{"returned review", map[string]any{"operation": "review", "planId": testPlanID, "verdict": "returned", "note": "缺配图"},
			[]string{testPlanID}, `{"note":"缺配图","verdict":"returned"}`},
		{"report with a decision", map[string]any{"operation": "report", "sourceMessageId": "cmsg-user",
			"asks": []any{map[string]any{"question": "预算封顶 $2,000？", "options": []any{"同意", "提高到 $3,000"}}}}, nil,
			`{"sourceMessageId":"cmsg-user","asks":[{"question":"预算封顶 $2,000？","options":["同意","提高到 $3,000"]}]}`},
		{"report closing clears decisions", map[string]any{"operation": "report", "sourceMessageId": "cmsg-user", "closing": "四项都齐了。"}, nil,
			`{"sourceMessageId":"cmsg-user","asks":[],"closing":"四项都齐了。"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operation, args, stdin, err := teamCoordinationRequest(tc.arguments)
			if err != nil {
				t.Fatal(err)
			}
			if operation != tc.arguments["operation"] || strings.Join(args, " ") != strings.Join(tc.args, " ") || string(stdin) != tc.stdin {
				t.Fatalf("operation=%q args=%v stdin=%s", operation, args, stdin)
			}
		})
	}
	tooMany := make([]any, 31)
	for i := range tooMany {
		tooMany[i] = "cmsg-x"
	}
	valid := func(extra map[string]any) map[string]any {
		arguments := map[string]any{"operation": "handoff", "sourceMessageId": "cmsg-user", "employeeId": "emp-a",
			"executionKind": "conversation", "title": "Review", "brief": "Review the plan"}
		for key, value := range extra {
			arguments[key] = value
		}
		return arguments
	}
	for name, arguments := range map[string]map[string]any{
		"unknown operation":       {"operation": "hire"},
		"missing operation":       {},
		"credential argument":     {"operation": "context", "capability": "token"},
		"API argument":            {"operation": "context", "cmoApi": "https://evil.example/cmo"},
		"field of another op":     {"operation": "context", "planId": testPlanID},
		"status without plan":     {"operation": "status"},
		"outcome without id":      {"operation": "outcome", "planId": testPlanID},
		"outcome id as a path":    {"operation": "outcome", "planId": testPlanID, "outcomeId": "art/../context"},
		"outcome id on status":    {"operation": "status", "planId": testPlanID, "outcomeId": "art-1"},
		"plan not from a receipt": {"operation": "status", "planId": "wp-../../context"},
		"message without source":  {"operation": "message", "planId": testPlanID},
		"blank note":              {"operation": "message", "planId": testPlanID, "note": "   "},
		"long note":               {"operation": "message", "planId": testPlanID, "note": strings.Repeat("字", 4001)},
		"note on handoff":         valid(map[string]any{"note": "smuggled"}),
		"handoff without target":  valid(map[string]any{"employeeId": ""}),
		"handoff id with spaces":  valid(map[string]any{"employeeId": "emp a"}),
		"unknown execution kind":  valid(map[string]any{"executionKind": "publish"}),
		"blank brief":             valid(map[string]any{"brief": "  "}),
		"long title":              valid(map[string]any{"title": strings.Repeat("标", 201)}),
		"too many context ids":    valid(map[string]any{"contextMessageIds": tooMany}),
		"context ids not a list":  valid(map[string]any{"contextMessageIds": "cmsg-1"}),
		"handoff with a plan":     valid(map[string]any{"planId": testPlanID}),
		"too many deliverables":   valid(map[string]any{"deliverables": []any{"1", "2", "3", "4", "5", "6", "7", "8", "9"}}),
		"blank deliverable":       valid(map[string]any{"deliverables": []any{"posts", " "}}),
		"review without plan":     {"operation": "review", "verdict": "accepted"},
		"unknown verdict":         {"operation": "review", "planId": testPlanID, "verdict": "done"},
		"returned without reason": {"operation": "review", "planId": testPlanID, "verdict": "returned"},
		"fractional count":        {"operation": "review", "planId": testPlanID, "verdict": "accepted", "delivered": 1.5},
		"count beyond the bound":  {"operation": "review", "planId": testPlanID, "verdict": "accepted", "delivered": float64(9)},
		"long review note":        {"operation": "review", "planId": testPlanID, "verdict": "returned", "note": strings.Repeat("字", 501)},
		"report without request":  {"operation": "report", "closing": "Done."},
		"report on a plan":        {"operation": "report", "sourceMessageId": "cmsg-user", "planId": testPlanID},
		"one-choice decision": {"operation": "report", "sourceMessageId": "cmsg-user",
			"asks": []any{map[string]any{"question": "Go?", "options": []any{"Yes"}}}},
		"extra ask field": {"operation": "report", "sourceMessageId": "cmsg-user",
			"asks": []any{map[string]any{"question": "Go?", "options": []any{"Yes", "No"}, "default": "Yes"}}},
		"four decisions": {"operation": "report", "sourceMessageId": "cmsg-user", "asks": []any{
			map[string]any{"question": "a", "options": []any{"1", "2"}}, map[string]any{"question": "b", "options": []any{"1", "2"}},
			map[string]any{"question": "c", "options": []any{"1", "2"}}, map[string]any{"question": "d", "options": []any{"1", "2"}}}},
		"verdict on a message": {"operation": "message", "planId": testPlanID, "note": "x", "verdict": "accepted"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := teamCoordinationRequest(arguments); err == nil {
				t.Fatal("invalid arguments reached the helper")
			}
		})
	}
}

func mustJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required for helper process test")
	}
}

func TestTeamCoordinationRunsTheHelperWithTheTurnsOwnAuthority(t *testing.T) {
	requireNode(t)
	s := teamToolSession(t, fakeHelperProbe)
	call, err := s.prepareTeamCoordination(map[string]any{"operation": "message", "planId": testPlanID, "sourceMessageId": "cmsg-later"})
	if err != nil {
		t.Fatal(err)
	}
	var authorityFile string
	for _, entry := range call.cmd.Env {
		if path, ok := strings.CutPrefix(entry, "TOMAKO_TASK_ENV_FILE="); ok {
			authorityFile = path
		}
	}
	// A later turn rotating the session's authority must not change this call.
	s.runtime.ImageCapabilityToken = "next-turn-token"
	s.runtime.CmoAPI = strings.Replace(testCmoAPI, "prod-a", "prod-b", 1)
	result, err := call.run()
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Argv          []string `json:"argv"`
		CmoAPI        string   `json:"cmoApi"`
		Stdin         string   `json:"stdin"`
		Cwd           string   `json:"cwd"`
		AuthorityFile bool     `json:"authorityFile"`
	}
	if err := json.Unmarshal([]byte(result), &seen); err != nil {
		t.Fatalf("receipt is not the helper's JSON: %q", result)
	}
	if strings.Join(seen.Argv, " ") != "message "+testPlanID || seen.Stdin != `{"sourceMessageId":"cmsg-later"}` {
		t.Fatalf("helper received %#v", seen)
	}
	if seen.CmoAPI != testCmoAPI || !seen.AuthorityFile {
		t.Fatalf("helper did not run with the snapshotted trusted authority: %#v", seen)
	}
	if resolved, _ := filepath.EvalSymlinks(seen.Cwd); resolved == "" || strings.HasPrefix(resolved, mustEval(t, s.workDir)) {
		t.Fatalf("helper ran inside the Agent-writable workspace: %s", seen.Cwd)
	}
	if _, err := os.Stat(filepath.Dir(authorityFile)); !os.IsNotExist(err) {
		t.Fatal("the call's private authority file outlived it")
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestTeamCoordinationFailureReportsPortalReasonWithoutCredentials(t *testing.T) {
	requireNode(t)
	for _, tc := range []struct {
		name, helper string
		want         []string
	}{
		{"Portal refusal", `process.stderr.write(JSON.stringify({operation:'handoff',ok:false,status:409,code:'EMPLOYEE_NOT_HIRED',
  message:'Employee is not hired; capability ` + testCmoToken + ` rejected'})+'\n'); process.exitCode=1;`,
			[]string{"team coordination handoff failed (HTTP 409, code EMPLOYEE_NOT_HIRED): Employee is not hired", "[redacted]"}},
		{"validation before any request", `process.stderr.write(JSON.stringify({operation:'handoff',ok:false,status:null,code:null,
  message:'brief must be non-empty text up to 32000 characters'})+'\n'); process.exitCode=1;`,
			[]string{"team coordination handoff failed: brief must be non-empty"}},
		{"crash without a receipt", `console.error('Error: ` + testCmoToken + `'); process.exit(3);`,
			[]string{"failed without a receipt; treat it as not done"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := teamToolSession(t, tc.helper)
			call, err := s.prepareTeamCoordination(map[string]any{"operation": "handoff", "sourceMessageId": "cmsg-user",
				"employeeId": "emp-a", "executionKind": "conversation", "title": "Review", "brief": "Review"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = call.run()
			if err == nil {
				t.Fatal("a failed helper reported success")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q lacks %q", err, want)
				}
			}
			if strings.Contains(err.Error(), testCmoToken) {
				t.Fatalf("credential leaked to the Agent: %q", err)
			}
		})
	}
}

func TestTeamCoordinationTimeoutKeepsRetrySafe(t *testing.T) {
	requireNode(t)
	previous := teamCoordinationTimeout
	teamCoordinationTimeout = 300 * time.Millisecond
	t.Cleanup(func() { teamCoordinationTimeout = previous })
	s := teamToolSession(t, `setTimeout(() => {}, 30000);`)
	call, err := s.prepareTeamCoordination(map[string]any{"operation": "context"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = call.run()
	if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "same IDs") {
		t.Fatalf("timeout error = %v", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("timed-out helper was not stopped")
	}
}

func TestDynamicToolCallReturnsTheReceiptToCodex(t *testing.T) {
	requireNode(t)
	s := teamToolSession(t, `console.log(JSON.stringify({members:[{employeeId:'emp-a',kindId:'content'}],receipts:[]}));`)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	s.stdin = writer
	s.handleDynamicToolCall(json.RawMessage(`7`), json.RawMessage(`{"callId":"call-1","tool":"tomako_team_coordination","arguments":{"operation":"context"}}`))
	var response struct {
		ID     int `json:"id"`
		Result struct {
			Success      bool `json:"success"`
			ContentItems []struct {
				Text string `json:"text"`
			} `json:"contentItems"`
		} `json:"result"`
	}
	done := make(chan error, 1)
	go func() { done <- json.NewDecoder(reader).Decode(&response) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no dynamic tool response")
	}
	if response.ID != 7 || !response.Result.Success || len(response.Result.ContentItems) != 1 ||
		response.Result.ContentItems[0].Text != `{"members":[{"employeeId":"emp-a","kindId":"content"}],"receipts":[]}` {
		t.Fatalf("response = %#v", response)
	}
}

func TestResumedThreadToolsComeFromTheRolloutSessionMeta(t *testing.T) {
	dir := t.TempDir()
	write := func(name, firstLine string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(firstLine+"\n{\"type\":\"response_item\",\"payload\":{\"name\":\"tomako_team_coordination\"}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name, line string
		want       bool
	}{
		{"canonical function", `{"type":"session_meta","payload":{"id":"t","dynamic_tools":[{"type":"function","name":"tomako_read_web_page"},{"type":"function","name":"tomako_team_coordination","description":"x","inputSchema":{}}]}}`, true},
		{"legacy flat spec", `{"type":"session_meta","payload":{"dynamic_tools":[{"name":"tomako_team_coordination","description":"x","inputSchema":{}}]}}`, true},
		{"started before the tool", `{"type":"session_meta","payload":{"base_instructions":{"text":"use tomako_team_coordination"},"dynamic_tools":[{"type":"function","name":"tomako_generate_image"}]}}`, false},
		{"no dynamic tools", `{"type":"session_meta","payload":{"id":"t"}}`, false},
		{"same name in a namespace", `{"type":"session_meta","payload":{"dynamic_tools":[{"type":"namespace","name":"x","tools":[{"type":"function","name":"tomako_team_coordination"}]}]}}`, false},
		{"legacy namespaced spec", `{"type":"session_meta","payload":{"dynamic_tools":[{"namespace":"x","name":"tomako_team_coordination"}]}}`, false},
		{"not session metadata", `{"type":"response_item","payload":{"dynamic_tools":[{"type":"function","name":"tomako_team_coordination"}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rolloutHasDynamicTool(write(strings.ReplaceAll(tc.name, " ", "-")+".jsonl", tc.line), teamCoordinationToolName); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if rolloutHasDynamicTool(filepath.Join(dir, "missing.jsonl"), teamCoordinationToolName) || rolloutHasDynamicTool("", teamCoordinationToolName) {
		t.Fatal("an unreadable rollout must count as lacking the tool")
	}
}

func TestHelperFallbackOnlyForCmoTurnsOnThreadsWithoutTheTool(t *testing.T) {
	s := teamToolSession(t, "// helper")
	s.threadID.Store("existing-thread")
	turn := cmoRuntime()
	if err := s.SetSessionRuntime(turn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(s.taskRuntimeEnvFile) })
	if s.CurrentSessionID() != "existing-thread" {
		t.Fatal("CMO authority must never replace the conversation's thread")
	}
	instructions := func() string {
		mode := s.turnDeveloperInstructions()
		if mode == nil {
			t.Fatal("a CMO turn must manage developer instructions")
		}
		text, _ := mode["settings"].(map[string]any)["developer_instructions"].(string)
		return text
	}
	if text := instructions(); !strings.Contains(text, teamCoordinationHelperFallback) || !strings.Contains(text, "cmo-handoff.mjs") {
		t.Fatalf("a thread without the tool lost its route to the CMO operations: %q", text)
	}
	s.threadTeamTool.Store(true)
	if strings.Contains(instructions(), "cmo-handoff.mjs") {
		t.Fatal("a thread with the tool must not be pointed at the helper")
	}
	s.threadTeamTool.Store(false)
	turn.CmoAPI = ""
	turn.DeveloperInstructions = "Current policy"
	if err := s.SetSessionRuntime(turn); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(instructions(), "cmo-handoff.mjs") {
		t.Fatal("a turn without CMO authority must not be told about team coordination")
	}
}

func TestRefreshedAuthorityCarriesTheCmoAPI(t *testing.T) {
	s := pausedTurnSession(t)
	fresh := core.SessionRuntime{TaskID: "cmsg-1", WorkspaceID: "w", BrandID: "b", ImageCapabilityToken: "new-image",
		CmoAPI: "https://test.tomako.ai/api/workspaces/w/products/b/cmo"}
	if err := s.RefreshCapabilityAuthority(fresh); err != nil {
		t.Fatal(err)
	}
	if s.runtime.CmoAPI != fresh.CmoAPI || !cmoCoordinationAuthorized(s.runtime) {
		t.Fatal("a paused CMO turn lost team coordination after its credentials were refreshed")
	}
	fresh.CmoAPI = ""
	if err := s.RefreshCapabilityAuthority(fresh); err != nil {
		t.Fatal(err)
	}
	if cmoCoordinationAuthorized(s.runtime) {
		t.Fatal("refreshed authority without CMO coordination kept it")
	}
}

// Codex fixes a thread's dynamic tools at thread/start and restores them on
// resume, so the tool must be registered when the thread starts and a resumed
// thread is judged by what it actually carries.
func TestTeamToolAcrossThreadStartAndResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake app-server")
	}
	skills := t.TempDir()
	if err := os.WriteFile(filepath.Join(skills, "cmo-handoff.mjs"), []byte("// helper"), 0o600); err != nil {
		t.Fatal(err)
	}
	rolloutWith := func(tools ...string) string {
		specs := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			specs = append(specs, map[string]any{"type": "function", "name": tool, "description": "d", "inputSchema": map[string]any{}})
		}
		line, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "thread-old", "dynamic_tools": specs}})
		path := filepath.Join(t.TempDir(), "rollout-thread-old.jsonl")
		if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name, resumeID, rollout string
		wantStartTool           bool
		wantFallback            bool
	}{
		{"new thread", "", "", true, false},
		{"resumed thread started with the tool", "thread-old", rolloutWith("tomako_read_web_page", teamCoordinationToolName), false, false},
		{"resumed thread from before the tool", "thread-old", rolloutWith("tomako_read_web_page"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			requests := filepath.Join(workDir, "requests.jsonl")
			script := `#!/bin/sh
REQUESTS=` + shellSingleQuote(requests) + `
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$REQUESTS"
  id=$(printf '%s' "$line" | sed -n 's/.*"id":[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) result='{}' ;;
    *'"method":"thread/start"'*) result='{"thread":{"id":"thread-new"}}' ;;
    *'"method":"thread/resume"'*) result='{"thread":{"id":"thread-old","path":` + mustJSON(tc.rollout) + `}}' ;;
    *'"method":"account/rateLimits/read"'*) result='{}' ;;
    *'"method":"turn/start"'*) result='{"turn":{"id":"test-turn"}}' ;;
    *) continue ;;
  esac
  printf '{"id":%s,"result":%s}\n' "$id" "$result"
done
`
			writeFakeCodexScript(t, workDir, script, "")
			t.Setenv("PATH", workDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			startup := cmoRuntime()
			s, err := newAppServerSession(context.Background(), "", workDir, "test-model", "low", "", "", tc.resumeID, "", "",
				[]string{"SKILLS_OL_DIR=" + skills}, "", startup)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Send("Ask the content employee for launch posts", nil, nil); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(requests)
			if err != nil {
				t.Fatal(err)
			}
			var startTool, sawTurn bool
			for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				var request struct {
					Method string         `json:"method"`
					Params map[string]any `json:"params"`
				}
				if json.Unmarshal([]byte(line), &request) != nil {
					continue
				}
				switch request.Method {
				case "thread/start":
					raw, _ := json.Marshal(request.Params["dynamicTools"])
					startTool = strings.Contains(string(raw), `"name":"`+teamCoordinationToolName+`"`)
				case "turn/start":
					sawTurn = true
					raw, _ := json.Marshal(request.Params["collaborationMode"])
					if got := strings.Contains(string(raw), "cmo-handoff.mjs"); got != tc.wantFallback {
						t.Fatalf("helper fallback = %v, want %v: %s", got, tc.wantFallback, raw)
					}
				}
			}
			if startTool != tc.wantStartTool || !sawTurn {
				t.Fatalf("thread/start tool = %v, want %v (turn seen %v)", startTool, tc.wantStartTool, sawTurn)
			}
		})
	}
}
