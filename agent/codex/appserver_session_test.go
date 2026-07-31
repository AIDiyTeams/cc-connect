package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerSession_ApplyThreadRuntimeState(t *testing.T) {
	s := &appServerSession{}
	effort := "xhigh"

	s.applyThreadRuntimeState("/tmp/project", "gpt-5.4", &effort)

	if got := s.GetWorkDir(); got != "/tmp/project" {
		t.Fatalf("GetWorkDir() = %q, want /tmp/project", got)
	}
	if got := s.GetModel(); got != "gpt-5.4" {
		t.Fatalf("GetModel() = %q, want gpt-5.4", got)
	}
	if got := s.GetReasoningEffort(); got != "xhigh" {
		t.Fatalf("GetReasoningEffort() = %q, want xhigh", got)
	}
}

func TestAppServerSession_FencedThreadParamsOverrideUnsafeGlobalMode(t *testing.T) {
	s := &appServerSession{
		workDir:            "/srv/tomako/workspaces/brand-42",
		mode:               "yolo",
		permissionsProfile: "tomako-brand-fence",
	}

	params := s.threadRequestParams()
	want := map[string]any{
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
		"cwd":                    "/srv/tomako/workspaces/brand-42",
		"permissions":            "tomako-brand-fence",
		"approvalPolicy":         "never",
	}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("thread params = %#v, want %#v", params, want)
	}
	if _, ok := params["sandbox"]; ok {
		t.Fatalf("fenced thread must select permissions profile instead of legacy sandbox: %#v", params)
	}
}

func TestAppServerSession_FencedTurnKeepsPermissionsProfile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		workDir:            "/srv/tomako/workspaces/brand-42",
		mode:               "yolo",
		permissionsProfile: "tomako-brand-fence",
		stdin:              stdin,
		ctx:                ctx,
		pending:            make(map[int64]chan rpcResponseEnvelope),
	}
	s.alive.Store(true)
	s.threadID.Store("thread-42")

	done := make(chan error, 1)
	go func() {
		done <- s.Send("update my files", nil, nil)
	}()

	line := waitForWrittenJSONLine(t, stdin)
	var request struct {
		ID     int64          `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(line), &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Method != "turn/start" {
		t.Fatalf("method = %q, want turn/start", request.Method)
	}
	if request.Params["permissions"] != "tomako-brand-fence" || request.Params["approvalPolicy"] != "never" {
		t.Fatalf("turn permissions = %#v", request.Params)
	}
	if request.Params["cwd"] != "/srv/tomako/workspaces/brand-42" {
		t.Fatalf("turn cwd = %#v", request.Params["cwd"])
	}

	s.handleResponse(rpcResponseEnvelope{
		ID:     request.ID,
		Result: json.RawMessage(`{"turn":{"id":"turn-42"}}`),
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Send() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Send() did not complete")
	}
}

func TestAppServerSession_HandleRateLimitsUpdatedCachesUsage(t *testing.T) {
	s := &appServerSession{}
	raw, err := json.Marshal(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{
			LimitID:   "codex",
			PlanType:  "pro",
			Primary:   &appServerRateLimitWindow{UsedPercent: 25, WindowDurationMins: 15, ResetsAt: 1730947200},
			Secondary: &appServerRateLimitWindow{UsedPercent: 42, WindowDurationMins: 60, ResetsAt: 1730950800},
			Credits:   &appServerCreditsSnapshot{HasCredits: true, Unlimited: false},
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("account/rateLimits/updated", raw)

	report, err := s.GetUsage(context.Background())
	if err != nil {
		t.Fatalf("GetUsage() returned error: %v", err)
	}
	if report.Provider != "codex" {
		t.Fatalf("provider = %q, want codex", report.Provider)
	}
	if report.Plan != "pro" {
		t.Fatalf("plan = %q, want pro", report.Plan)
	}
	if len(report.Buckets) != 1 {
		t.Fatalf("buckets = %d, want 1", len(report.Buckets))
	}
	if got := report.Buckets[0].Name; got != "codex" {
		t.Fatalf("bucket name = %q, want codex", got)
	}
	if got := report.Buckets[0].Windows[0].WindowSeconds; got != 15*60 {
		t.Fatalf("primary window seconds = %d, want %d", got, 15*60)
	}
	if got := report.Buckets[0].Windows[1].UsedPercent; got != 42 {
		t.Fatalf("secondary used percent = %d, want 42", got)
	}
	if report.Credits == nil || !report.Credits.HasCredits {
		t.Fatalf("credits = %#v, want has credits", report.Credits)
	}
}

func TestAppServerSession_HandleThreadTokenUsageUpdatedCachesContextUsage(t *testing.T) {
	s := &appServerSession{}
	raw, err := json.Marshal(appServerThreadTokenUsageNotification{
		ThreadID: "thread-1",
		TurnID:   "turn-1",
		TokenUsage: struct {
			Total              codexTokenUsage `json:"total"`
			Last               codexTokenUsage `json:"last"`
			ModelContextWindow int             `json:"modelContextWindow"`
		}{
			Total: codexTokenUsage{
				TotalTokens:           52011395,
				InputTokens:           51847383,
				CachedInputTokens:     48187904,
				OutputTokens:          164012,
				ReasoningOutputTokens: 78910,
			},
			Last: codexTokenUsage{
				TotalTokens:           41061,
				InputTokens:           40849,
				CachedInputTokens:     36864,
				OutputTokens:          212,
				ReasoningOutputTokens: 32,
			},
			ModelContextWindow: 258400,
		},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}

	s.handleNotification("thread/tokenUsage/updated", raw)

	usage := s.GetContextUsage()
	if usage == nil {
		t.Fatal("GetContextUsage() = nil, want cached context usage")
	}
	if usage.UsedTokens != 41061 {
		t.Fatalf("used tokens = %d, want 41061", usage.UsedTokens)
	}
	if usage.BaselineTokens != codexContextBaselineTokens {
		t.Fatalf("baseline tokens = %d, want %d", usage.BaselineTokens, codexContextBaselineTokens)
	}
	if usage.TotalTokens != 41061 {
		t.Fatalf("total tokens = %d, want 41061", usage.TotalTokens)
	}
	if usage.ContextWindow != 258400 {
		t.Fatalf("context window = %d, want 258400", usage.ContextWindow)
	}
	if usage.CachedInputTokens != 36864 {
		t.Fatalf("cached input tokens = %d, want 36864", usage.CachedInputTokens)
	}
	if usage.InputTokens != 40849 {
		t.Fatalf("input tokens = %d, want 40849", usage.InputTokens)
	}
}

func TestAppServerSession_HandleTurnPlanUpdatedEmitsAgentPlan(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 1)}
	raw := json.RawMessage(`{
		"turnId":"turn-1",
		"plan":[
			{"step":"明确 Reddit 帖子角度","status":"completed"},
			{"step":"生成对应封面与配图","status":"inProgress"}
		]
	}`)

	s.handleNotification("turn/plan/updated", raw)

	select {
	case event := <-s.events:
		if event.Type != core.EventPlanUpdate {
			t.Fatalf("event type = %q, want %q", event.Type, core.EventPlanUpdate)
		}
		if len(event.ProgressTasks) != 2 {
			t.Fatalf("tasks = %#v, want 2", event.ProgressTasks)
		}
		if event.ProgressTasks[0].Title != "明确 Reddit 帖子角度" || event.ProgressTasks[0].Status != core.ProgressTaskCompleted {
			t.Fatalf("first task = %#v", event.ProgressTasks[0])
		}
		if event.ProgressTasks[1].Title != "生成对应封面与配图" || event.ProgressTasks[1].Status != core.ProgressTaskInProgress {
			t.Fatalf("second task = %#v", event.ProgressTasks[1])
		}
	default:
		t.Fatal("turn/plan/updated did not emit an event")
	}
}

func TestAppServerSession_AgentMessageDeltaStreamsText(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}

	s.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"threadId":"t1","turnId":"u1","itemId":"msg-1","delta":"你好，"}`))
	s.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"threadId":"t1","turnId":"u1","itemId":"msg-1","delta":"这是流式文字"}`))

	first := <-s.events
	if first.Type != core.EventText || first.Content != "你好，" {
		t.Fatalf("first delta event = %#v", first)
	}
	second := <-s.events
	if second.Type != core.EventText || second.Content != "这是流式文字" {
		t.Fatalf("second delta event = %#v", second)
	}

	// item/completed for a streamed item must not re-buffer text into
	// pendingMsgs (which would duplicate it or demote it to thinking).
	s.handleItemCompleted(map[string]any{
		"type": "agentMessage",
		"id":   "msg-1",
		"text": "你好，这是流式文字",
	})
	select {
	case extra := <-s.events:
		t.Fatalf("streamed item completion should not emit again, got %#v", extra)
	default:
	}
	s.stateMu.Lock()
	pending := len(s.pendingMsgs)
	s.stateMu.Unlock()
	if pending != 0 {
		t.Fatalf("pendingMsgs = %d, want 0 for streamed item", pending)
	}
}

func TestAppServerSession_ToolEventsKeepStableTraceID(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4)}
	s.handleItemStarted(map[string]any{"type": "webSearch", "id": "search-42", "query": "Tomako AI growth"})
	started := <-s.events
	if started.Type != core.EventToolUse || started.TraceID != "search-42" || started.ToolInput != "Tomako AI growth" {
		t.Fatalf("started trace = %#v", started)
	}
	s.handleItemCompleted(map[string]any{"type": "webSearch", "id": "search-42", "query": "Tomako AI growth"})
	completed := <-s.events
	if completed.Type != core.EventToolResult || completed.TraceID != "search-42" {
		t.Fatalf("completed trace = %#v", completed)
	}
}

func TestAppServerSession_AgentMessageDeltaEmitsMissingTailOnCompletion(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}

	s.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"msg-1","delta":"部分"}`))
	<-s.events

	s.handleItemCompleted(map[string]any{
		"type": "agentMessage",
		"id":   "msg-1",
		"text": "部分文字被跳过",
	})
	tail := <-s.events
	if tail.Type != core.EventText || tail.Content != "文字被跳过" {
		t.Fatalf("tail event = %#v", tail)
	}
}

func TestAppServerSession_AgentMessageSeparatorBetweenStreamedItems(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}

	s.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"msg-1","delta":"第一段"}`))
	<-s.events
	s.handleNotification("item/agentMessage/delta",
		json.RawMessage(`{"itemId":"msg-2","delta":"第二段"}`))

	event := <-s.events
	if event.Content != "\n\n第二段" {
		t.Fatalf("second item first delta = %q, want paragraph separator prefix", event.Content)
	}
}

func TestAppServerSession_UnstreamedAgentMessageStillBuffers(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}

	s.handleItemCompleted(map[string]any{
		"type": "agentMessage",
		"id":   "msg-9",
		"text": "fallback message",
	})
	s.stateMu.Lock()
	pending := len(s.pendingMsgs)
	s.stateMu.Unlock()
	if pending != 1 {
		t.Fatalf("pendingMsgs = %d, want 1 for unstreamed item", pending)
	}
}

func TestMapAppServerRateLimits_PrefersMultiBucketView(t *testing.T) {
	report := mapAppServerRateLimits(appServerRateLimitsResponse{
		RateLimits: appServerRateLimitSnapshot{
			LimitID:  "legacy",
			PlanType: "team",
			Primary:  &appServerRateLimitWindow{UsedPercent: 99, WindowDurationMins: 15},
		},
		RateLimitsByLimitID: map[string]appServerRateLimitSnapshot{
			"codex": {
				LimitID:   "codex",
				LimitName: "Codex",
				PlanType:  "team",
				Primary:   &appServerRateLimitWindow{UsedPercent: 10, WindowDurationMins: 15},
			},
			"codex_other": {
				LimitID:  "codex_other",
				PlanType: "team",
				Primary:  &appServerRateLimitWindow{UsedPercent: 20, WindowDurationMins: 60},
			},
		},
	})

	if report.Plan != "team" {
		t.Fatalf("plan = %q, want team", report.Plan)
	}
	if len(report.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(report.Buckets))
	}
	if report.Buckets[0].Name != "Codex" {
		t.Fatalf("first bucket = %q, want Codex", report.Buckets[0].Name)
	}
	if report.Buckets[1].Name != "codex_other" {
		t.Fatalf("second bucket = %q, want codex_other", report.Buckets[1].Name)
	}
}

func TestAppServerSession_HandleRequestUserInputEmitsAskQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		events:           make(chan core.Event, 4),
		ctx:              ctx,
		pendingApprovals: make(map[string]chan core.PermissionResult),
		stdin:            stdin,
	}

	s.handleServerRequest(serverRequestProbe(t, `"rui-1"`, "item/tool/requestUserInput", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "call-1",
		"questions": []any{
			map[string]any{
				"id":       "database",
				"header":   "Database",
				"question": "Which database should we use?",
				"isOther":  true,
				"isSecret": false,
				"options": []any{
					map[string]any{"label": "Postgres", "description": "Use the existing relational database"},
					map[string]any{"label": "SQLite", "description": "Keep it embedded"},
				},
			},
		},
	}))

	var event core.Event
	select {
	case event = <-s.events:
	case <-time.After(time.Second):
		t.Fatal("expected AskUserQuestion event")
	}
	if event.Type != core.EventPermissionRequest {
		t.Fatalf("event type = %s, want %s", event.Type, core.EventPermissionRequest)
	}
	if event.ToolName != "AskUserQuestion" {
		t.Fatalf("tool name = %q, want AskUserQuestion", event.ToolName)
	}
	if event.RequestID != `"rui-1"` {
		t.Fatalf("request id = %q, want raw JSON id", event.RequestID)
	}
	if len(event.Questions) != 1 {
		t.Fatalf("questions = %d, want 1", len(event.Questions))
	}
	q := event.Questions[0]
	if q.ID != "database" {
		t.Fatalf("question id = %q, want database", q.ID)
	}
	if q.Question != "Which database should we use?" || q.Header != "Database" {
		t.Fatalf("question = %#v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Label != "Postgres" || q.Options[1].Description != "Keep it embedded" {
		t.Fatalf("options = %#v", q.Options)
	}
	if q.Options[0].ID != "database-option-1" || q.Options[1].ID != "database-option-2" {
		t.Fatalf("option ids = %#v", q.Options)
	}
	if stdin.String() != "" {
		t.Fatalf("request_user_input should not write before the answer, got %q", stdin.String())
	}
}

func TestAppServerSession_HandleRequestUserInputWritesCodexResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stdin := &lockedWriteCloser{}
	s := &appServerSession{
		events:           make(chan core.Event, 4),
		ctx:              ctx,
		pendingApprovals: make(map[string]chan core.PermissionResult),
		stdin:            stdin,
	}

	s.handleServerRequest(serverRequestProbe(t, `"rui-2"`, "item/tool/requestUserInput", map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"itemId":   "call-2",
		"questions": []any{
			map[string]any{
				"id":       "database",
				"header":   "Database",
				"question": "Which database should we use?",
				"options": []any{
					map[string]any{"label": "Postgres", "description": "Use the existing relational database"},
					map[string]any{"label": "SQLite", "description": "Keep it embedded"},
				},
			},
		},
	}))

	var event core.Event
	select {
	case event = <-s.events:
	case <-time.After(time.Second):
		t.Fatal("expected AskUserQuestion event")
	}
	if err := s.RespondPermission(event.RequestID, core.PermissionResult{
		Behavior: "allow",
		UpdatedInput: map[string]any{
			"answers": map[string]any{
				"Which database should we use?": "Postgres",
			},
		},
	}); err != nil {
		t.Fatalf("RespondPermission() error = %v", err)
	}

	line := waitForWrittenJSONLine(t, stdin)
	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Result  struct {
			Answers map[string]struct {
				Answers []string `json:"answers"`
			} `json:"answers"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	if envelope.JSONRPC != "2.0" || envelope.ID != "rui-2" {
		t.Fatalf("envelope = %#v", envelope)
	}
	got := envelope.Result.Answers["database"].Answers
	if len(got) != 1 || got[0] != "Postgres" {
		t.Fatalf("answers[database] = %#v, want [Postgres]", got)
	}
}

var _ interface {
	GetUsage(context.Context) (*core.UsageReport, error)
} = (*appServerSession)(nil)

var _ interface {
	GetContextUsage() *core.ContextUsage
} = (*appServerSession)(nil)

type lockedWriteCloser struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriteCloser) Close() error { return nil }

func (w *lockedWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

var _ io.WriteCloser = (*lockedWriteCloser)(nil)

func serverRequestProbe(t *testing.T, idJSON, method string, params any) map[string]json.RawMessage {
	t.Helper()
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	methodJSON, err := json.Marshal(method)
	if err != nil {
		t.Fatalf("marshal method: %v", err)
	}
	return map[string]json.RawMessage{
		"id":     json.RawMessage(idJSON),
		"method": methodJSON,
		"params": paramsJSON,
	}
}

func waitForWrittenJSONLine(t *testing.T, w *lockedWriteCloser) string {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for JSON response, buffer=%q", w.String())
		case <-ticker.C:
			for _, line := range strings.Split(w.String(), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					return line
				}
			}
		}
	}
}
