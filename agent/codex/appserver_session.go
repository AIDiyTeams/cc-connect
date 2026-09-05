package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type rpcResponseEnvelope struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcNotificationEnvelope struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initResponse struct {
	ProtocolVersion string `json:"protocolVersion"`
}

type threadStartResponse struct {
	Cwd             string  `json:"cwd"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	Thread          struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type threadResumeResponse struct {
	Cwd             string  `json:"cwd"`
	Model           string  `json:"model"`
	ReasoningEffort *string `json:"reasoningEffort"`
	Thread          struct {
		ID string `json:"id"`
	} `json:"thread"`
}

type turnStartResponse struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

type turnNotification struct {
	ThreadID string `json:"threadId"`
	Turn     struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"turn"`
}

type itemNotification struct {
	ThreadID string         `json:"threadId"`
	TurnID   string         `json:"turnId"`
	Item     map[string]any `json:"item"`
}

type errorNotification struct {
	// Current app-server versions nest the message in error. Keep the legacy
	// top-level message for older transports, but never terminate on a retry.
	Message   string `json:"message"`
	ThreadID  string `json:"threadId"`
	TurnID    string `json:"turnId"`
	WillRetry bool   `json:"willRetry"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type appServerRateLimitsResponse struct {
	RateLimits          appServerRateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]appServerRateLimitSnapshot `json:"rateLimitsByLimitId"`
}

type appServerRateLimitSnapshot struct {
	LimitID   string                    `json:"limitId"`
	LimitName string                    `json:"limitName"`
	PlanType  string                    `json:"planType"`
	Primary   *appServerRateLimitWindow `json:"primary"`
	Secondary *appServerRateLimitWindow `json:"secondary"`
	Credits   *appServerCreditsSnapshot `json:"credits"`
}

type appServerRateLimitWindow struct {
	UsedPercent        int   `json:"usedPercent"`
	WindowDurationMins int   `json:"windowDurationMins"`
	ResetsAt           int64 `json:"resetsAt"`
}

type appServerCreditsSnapshot struct {
	Balance    *string `json:"balance"`
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
}

type appServerRequestUserInputParams struct {
	ThreadID  string                              `json:"threadId"`
	TurnID    string                              `json:"turnId"`
	ItemID    string                              `json:"itemId"`
	Questions []appServerRequestUserInputQuestion `json:"questions"`
}

type appServerRequestUserInputQuestion struct {
	ID       string                            `json:"id"`
	Header   string                            `json:"header"`
	Question string                            `json:"question"`
	IsOther  bool                              `json:"isOther"`
	IsSecret bool                              `json:"isSecret"`
	Options  []appServerRequestUserInputOption `json:"options"`
}

type appServerRequestUserInputOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type appServerRequestUserInputResponse struct {
	Answers map[string]appServerRequestUserInputAnswer `json:"answers"`
}

type appServerRequestUserInputAnswer struct {
	Answers []string `json:"answers"`
}

type appServerSession struct {
	url                string
	workDir            string
	model              string
	effort             string
	webSearch          string
	mode               string
	permissionsProfile string
	baseURL            string
	modelProvider      string
	extraEnv           []string
	codexHome          string

	events chan core.Event

	ctx    context.Context
	cancel context.CancelFunc

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	procMu  sync.Mutex
	writeMu sync.Mutex

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[int64]chan rpcResponseEnvelope
	threadMu  sync.Mutex
	resumeID  string

	approvalsMu      sync.Mutex
	pendingApprovals map[string]chan core.PermissionResult

	threadID atomic.Value
	alive    atomic.Bool
	// threadBrandTools records whether the current app-server thread was
	// created with the brand-analysis dynamic tool set. Dynamic tools are a
	// thread-start capability, so a reused thread must be replaced when a turn
	// crosses the brand-analysis boundary.
	threadBrandTools bool

	closeOnce sync.Once
	wg        sync.WaitGroup

	stateMu     sync.Mutex
	pendingMsgs []string
	currentTurn string
	// streamedItems tracks agentMessage items already delivered live via
	// item/agentMessage/delta (itemID → accumulated streamed text), so
	// item/completed does not re-emit or re-classify them as thinking.
	streamedItems    map[string]string
	lastStreamedItem string

	runtimeMu          sync.RWMutex
	usage              *core.UsageReport
	context            *core.ContextUsage
	runtime            core.SessionRuntime
	taskRuntimeEnvFile string

	brandFlowMu sync.Mutex
	brandFlow   brandAnalysisFlow
}

type brandAnalysisFlow struct {
	evidenceRunning       bool
	evidenceReady         bool
	corePublishing        bool
	corePublished         bool
	searchTraceID         string
	searchAttempts        int
	searchCompleted       bool
	competitorsPublishing bool
	competitorsPublished  bool
}

const (
	appServerRequestTimeout      = 120 * time.Second
	appServerUsageRefreshTimeout = 1500 * time.Millisecond
)

func newAppServerSession(ctx context.Context, url, workDir, model, effort, mode, permissionsProfile, resumeID, baseURL, modelProvider string, extraEnv []string, codexHome string) (*appServerSession, error) {
	sessionStartedAt := time.Now()
	sessionCtx, cancel := context.WithCancel(ctx)
	s := &appServerSession{
		url:                url,
		workDir:            workDir,
		model:              model,
		effort:             effort,
		mode:               mode,
		permissionsProfile: strings.TrimSpace(permissionsProfile),
		baseURL:            baseURL,
		modelProvider:      modelProvider,
		extraEnv:           append([]string(nil), extraEnv...),
		codexHome:          strings.TrimSpace(codexHome),
		events:             make(chan core.Event, 128),
		ctx:                sessionCtx,
		cancel:             cancel,
		pending:            make(map[int64]chan rpcResponseEnvelope),
		pendingApprovals:   make(map[string]chan core.PermissionResult),
		resumeID:           resumeID,
	}
	s.alive.Store(true)
	// Bind a stable, initially empty authority file before the first start or
	// eager resume. Codex ignores config overrides when resuming a loaded
	// thread; a second resume cannot add the shell environment after the fact.
	var err error
	s.taskRuntimeEnvFile, err = createTaskRuntimeEnv(workDir, permissionsProfile)
	if err != nil {
		cancel()
		return nil, err
	}

	connectStartedAt := time.Now()
	if err := s.connect(); err != nil {
		removeTaskRuntimeEnv(s.taskRuntimeEnvFile)
		cancel()
		return nil, err
	}
	s.emitLifecycle("app_server_process_started", time.Since(connectStartedAt))

	initializeStartedAt := time.Now()
	if err := s.initialize(); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.emitLifecycle("app_server_initialized", time.Since(initializeStartedAt))
	s.emitLifecycle("agent_session_started", time.Since(sessionStartedAt))

	// Existing sessions must be resumed eagerly so a stale thread ID can still
	// trigger the engine's normal fresh-session fallback. New threads are
	// created on first Send, after the trusted per-task runtime has arrived.
	if resumeID != "" && resumeID != core.ContinueSession {
		if err := s.ensureThread(resumeID); err != nil {
			_ = s.Close()
			return nil, err
		}
		s.resumeID = ""
		if err := s.refreshUsage(context.Background()); err != nil {
			slog.Debug("codex app-server: initial rate limit fetch failed", "error", err)
		}
	}

	return s, nil
}

func (s *appServerSession) connect() error {
	args := []string{"app-server"}
	if strings.TrimSpace(s.url) != "" {
		args = append(args, "--listen", strings.TrimSpace(s.url))
	}
	if model := strings.TrimSpace(s.model); model != "" {
		args = append(args, "-c", fmt.Sprintf("model=%q", model))
	}
	if effort := strings.TrimSpace(s.effort); effort != "" {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", effort))
	}
	if provider := strings.TrimSpace(s.modelProvider); provider != "" {
		args = append(args, "-c", fmt.Sprintf("model_provider=%q", provider))
	}
	if baseURL := strings.TrimSpace(s.baseURL); baseURL != "" {
		args = append(args, "-c", fmt.Sprintf("openai_base_url=%q", baseURL))
	}
	cmd := exec.CommandContext(s.ctx, "codex", args...)
	cmd.Dir = s.workDir
	env := append([]string(nil), s.extraEnv...)
	// Node/MCP child tools inherit the app-server environment, not the shell
	// policy passed to thread/start. Bind the same non-secret stable path before
	// process startup so every tool runtime can load the current turn authority.
	if envFile := s.currentTaskRuntimeEnvFile(); envFile != "" {
		env = append(env, "TOMAKO_TASK_ENV_FILE="+envFile)
	}
	if s.codexHome != "" {
		env = append(env, "CODEX_HOME="+s.codexHome)
	}
	if len(env) > 0 {
		cmd.Env = core.MergeEnv(os.Environ(), env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("codex app-server stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("codex app-server start: %w", err)
	}

	s.procMu.Lock()
	s.cmd = cmd
	s.stdin = stdin
	s.procMu.Unlock()

	slog.Info("codex app-server session started", "transport", "stdio", "pid", cmd.Process.Pid, "work_dir", s.workDir)

	s.wg.Add(3)
	go s.readLoop(stdout)
	go s.stderrLoop(stderr)
	go s.waitLoop()
	return nil
}
func (s *appServerSession) initialize() error {
	params := map[string]any{
		"clientInfo": map[string]any{
			"name":    "cc-connect-codex-agent",
			"title":   "CC Connect Codex Agent",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{
			"experimentalApi": true,
			// item/agentMessage/delta is deliberately NOT opted out: Studio
			// streams assistant text token-by-token through EventText so the
			// user sees prose forming live instead of one blob at turn end.
			"optOutNotificationMethods": []string{
				"command/exec/outputDelta",
				"item/plan/delta",
				"item/fileChange/outputDelta",
				"item/reasoning/summaryTextDelta",
				"item/reasoning/textDelta",
			},
		},
	}

	var resp initResponse
	if err := s.request("initialize", params, &resp); err != nil {
		return fmt.Errorf("codex app-server initialize: %w", err)
	}
	if err := s.notify("initialized", nil); err != nil {
		return fmt.Errorf("codex app-server initialized notify: %w", err)
	}
	return nil
}

func (s *appServerSession) ensureThread(resumeID string) error {
	startedAt := time.Now()
	if resumeID != "" && resumeID != core.ContinueSession {
		params := s.threadRequestParams()
		params["threadId"] = resumeID
		params["persistExtendedHistory"] = true

		var resp threadResumeResponse
		if err := s.request("thread/resume", params, &resp); err != nil {
			return err
		}
		if resp.Thread.ID == "" {
			return fmt.Errorf("codex app-server resume returned empty thread id")
		}
		s.applyThreadRuntimeState(resp.Cwd, resp.Model, resp.ReasoningEffort)
		s.threadID.Store(resp.Thread.ID)
		s.threadBrandTools = s.isBrandAnalysisRuntime()
		slog.Info("codex app-server thread resumed", "thread_id", resp.Thread.ID)
		s.emitLifecycle("agent_thread_ready", time.Since(startedAt))
		return nil
	}

	var resp threadStartResponse
	if err := s.request("thread/start", s.threadRequestParams(), &resp); err != nil {
		return err
	}
	if resp.Thread.ID == "" {
		return fmt.Errorf("codex app-server start returned empty thread id")
	}
	s.applyThreadRuntimeState(resp.Cwd, resp.Model, resp.ReasoningEffort)
	s.threadID.Store(resp.Thread.ID)
	s.threadBrandTools = s.isBrandAnalysisRuntime()
	slog.Info("codex app-server thread started", "thread_id", resp.Thread.ID)
	s.emitLifecycle("agent_thread_ready", time.Since(startedAt))
	return nil
}

func (s *appServerSession) ensureThreadForSend() error {
	if s.CurrentSessionID() != "" {
		return nil
	}
	s.threadMu.Lock()
	defer s.threadMu.Unlock()
	if s.CurrentSessionID() != "" {
		return nil
	}
	if err := s.ensureThread(s.resumeID); err != nil {
		return fmt.Errorf("codex app-server initialize thread: %w", err)
	}
	s.resumeID = ""
	if err := s.refreshUsage(context.Background()); err != nil {
		slog.Debug("codex app-server: initial rate limit fetch failed", "error", err)
	}
	return nil
}

func (s *appServerSession) threadRequestParams() map[string]any {
	config := map[string]any{
		"features.default_mode_request_user_input": true,
	}
	if envFile := s.currentTaskRuntimeEnvFile(); envFile != "" {
		config["shell_environment_policy.set.TOMAKO_TASK_ENV_FILE"] = envFile
	}
	params := map[string]any{
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
		"cwd":                    s.workDir,
		"config":                 config,
	}
	if model := s.GetModel(); model != "" {
		params["model"] = model
	}
	if searchMode := s.getWebSearch(); searchMode != "" {
		config["web_search"] = searchMode
	}
	if effort := s.GetReasoningEffort(); effort != "" {
		config["model_reasoning_effort"] = effort
	}
	if s.isBrandAnalysisRuntime() {
		params["dynamicTools"] = brandAnalysisDynamicTools()
	}
	if profile := strings.TrimSpace(s.permissionsProfile); profile != "" {
		params["permissions"] = profile
		params["approvalPolicy"] = "never"
	} else if approval, sandbox := appServerModeSettings(s.mode); approval != "" {
		params["approvalPolicy"] = approval
		if sandbox != "" {
			params["sandbox"] = sandbox
		}
	}
	return params
}

func appServerModeSettings(mode string) (approval string, sandbox string) {
	switch normalizeMode(mode) {
	case "auto-edit", "full-auto":
		return "never", "workspace-write"
	case "yolo":
		return "never", "danger-full-access"
	default:
		return "on-request", "read-only"
	}
}

func (s *appServerSession) applyThreadRuntimeState(workDir, model string, effort *string) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if dir := strings.TrimSpace(workDir); dir != "" {
		s.workDir = dir
	}
	if m := strings.TrimSpace(model); m != "" {
		s.model = m
	}
	if forced := strings.TrimSpace(s.runtime.ReasoningEffort); forced != "" {
		s.effort = normalizeRuntimeReasoningEffort(forced)
	} else {
		s.effort = normalizeRuntimeReasoningEffort(stringValue(effort))
	}
}

func (s *appServerSession) refreshUsage(ctx context.Context) error {
	timeout := appServerUsageRefreshTimeout
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			if until := time.Until(deadline); until > 0 && until < timeout {
				timeout = until
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if timeout <= 0 {
		return context.DeadlineExceeded
	}

	var resp appServerRateLimitsResponse
	if err := s.requestWithTimeout("account/rateLimits/read", map[string]any{}, &resp, timeout); err != nil {
		return err
	}
	s.storeUsage(mapAppServerRateLimits(resp))
	return nil
}

func (s *appServerSession) cachedUsage() *core.UsageReport {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return cloneUsageReport(s.usage)
}

func (s *appServerSession) cachedContextUsage() *core.ContextUsage {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return cloneContextUsage(s.context)
}

func (s *appServerSession) storeUsage(report *core.UsageReport) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.usage = cloneUsageReport(report)
}

func (s *appServerSession) storeContextUsage(usage *core.ContextUsage) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.context = cloneContextUsage(usage)
}

func (s *appServerSession) Send(prompt string, images []core.ImageAttachment, files []core.FileAttachment) error {
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	if len(files) > 0 {
		filePaths := core.SaveFilesToDisk(s.workDir, files)
		prompt = core.AppendFileRefs(prompt, filePaths)
	}

	prompt, imagePaths, err := s.stageImages(prompt, images)
	if err != nil {
		return err
	}
	if err := s.ensureThreadForSend(); err != nil {
		return err
	}

	threadID := s.CurrentSessionID()
	if threadID == "" {
		return fmt.Errorf("codex app-server thread id is empty")
	}

	input := make([]map[string]any, 0, 1+len(imagePaths))
	input = append(input, map[string]any{
		"type":          "text",
		"text":          prompt,
		"text_elements": []any{},
	})
	for _, path := range imagePaths {
		input = append(input, map[string]any{
			"type": "localImage",
			"path": path,
		})
	}

	params := map[string]any{
		"threadId": threadID,
		"input":    input,
		"cwd":      s.workDir,
	}
	if model := s.GetModel(); model != "" {
		params["model"] = model
	}
	if effort := s.GetReasoningEffort(); effort != "" {
		params["effort"] = effort
	}
	if schema := s.outputSchema(); len(schema) > 0 {
		params["outputSchema"] = schema
	}
	if metadata := s.responsesAPIClientMetadata(); len(metadata) > 0 {
		params["responsesapiClientMetadata"] = metadata
	}
	if profile := strings.TrimSpace(s.permissionsProfile); profile != "" {
		params["permissions"] = profile
		params["approvalPolicy"] = "never"
	} else if approval, _ := appServerModeSettings(s.mode); approval != "" {
		params["approvalPolicy"] = approval
	}

	turnStartedAt := time.Now()
	var resp turnStartResponse
	if err := s.request("turn/start", params, &resp); err != nil {
		return fmt.Errorf("codex app-server turn/start: %w", err)
	}
	if resp.Turn.ID == "" {
		return fmt.Errorf("codex app-server turn/start returned empty turn id")
	}
	s.emitLifecycle("agent_turn_started", time.Since(turnStartedAt))

	s.stateMu.Lock()
	s.currentTurn = resp.Turn.ID
	s.pendingMsgs = s.pendingMsgs[:0]
	s.streamedItems = nil
	s.lastStreamedItem = ""
	s.stateMu.Unlock()

	return nil
}

// SetSessionRuntime applies a control-plane snapshot to this app-server
// session only. turn/start repeats the model on every turn, so concurrent
// sessions never depend on the Agent's process-wide provider switcher.
func (s *appServerSession) SetSessionRuntime(runtime core.SessionRuntime) error {
	if !s.alive.Load() {
		return fmt.Errorf("session is closed")
	}
	if err := core.ValidateOutputSchema(runtime.OutputSchema); err != nil {
		return err
	}
	runtime.OutputSchema = append(json.RawMessage(nil), runtime.OutputSchema...)
	s.runtimeMu.Lock()
	envFile, err := updateTaskRuntimeEnv(s.taskRuntimeEnvFile, runtime)
	if err != nil {
		s.runtimeMu.Unlock()
		return err
	}
	s.taskRuntimeEnvFile = envFile
	searchChanged := s.webSearch != normalizeWebSearch(runtime.WebSearch)
	s.runtime = runtime
	if model := strings.TrimSpace(runtime.GatewayModel); model != "" {
		s.model = model
	}
	if effort := strings.TrimSpace(runtime.ReasoningEffort); effort != "" {
		s.effort = normalizeRuntimeReasoningEffort(effort)
	}
	s.webSearch = normalizeWebSearch(runtime.WebSearch)
	s.runtimeMu.Unlock()

	// The thread retains its original shell policy. Only the protected file's
	// contents rotate between turns, including revocation on an unscoped turn.
	// Each bridge task is a new Agent turn. Do not leak evidence/search guards
	// from an earlier brand-analysis task that happened to share the same
	// workspace session.
	s.brandFlowMu.Lock()
	s.brandFlow = brandAnalysisFlow{}
	s.brandFlowMu.Unlock()

	// App-server dynamic tools are fixed at thread/start (or thread/resume), not
	// turn/start. Replace a reused thread when entering or leaving the dedicated
	// brand-analysis tool mode so the model sees exactly the tools for this turn.
	wantsBrandTools := strings.EqualFold(strings.TrimSpace(runtime.Scene), "brand_analysis")
	s.threadMu.Lock()
	if s.CurrentSessionID() != "" && (s.threadBrandTools != wantsBrandTools || searchChanged) {
		s.threadID.Store("")
		s.resumeID = ""
	}
	s.threadMu.Unlock()
	return nil
}

func (s *appServerSession) currentTaskRuntimeEnvFile() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.taskRuntimeEnvFile
}

func (s *appServerSession) SupportsOutputSchema() bool { return true }

func (s *appServerSession) SupportsToolAuthority() bool { return true }

func (s *appServerSession) outputSchema() json.RawMessage {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return append(json.RawMessage(nil), s.runtime.OutputSchema...)
}

func (s *appServerSession) getWebSearch() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.TrimSpace(s.webSearch)
}

func (s *appServerSession) responsesAPIClientMetadata() map[string]string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	runtime := s.runtime
	metadata := make(map[string]string, 12)
	put := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			metadata[key] = value
		}
	}
	put("tomako.logical_model", runtime.LogicalModel)
	put("tomako.route_policy_id", runtime.RoutePolicyID)
	if runtime.RoutePolicyVersion > 0 {
		metadata["tomako.route_policy_version"] = fmt.Sprintf("%d", runtime.RoutePolicyVersion)
	}
	put("tomako.inference_request_id", runtime.InferenceRequestID)
	put("tomako.workspace_id", runtime.WorkspaceID)
	put("tomako.brand_id", runtime.BrandID)
	put("tomako.user_id", runtime.UserID)
	put("tomako.chat_session_id", runtime.ChatSessionID)
	put("tomako.task_id", runtime.TaskID)
	if runtime.TurnNo > 0 {
		metadata["tomako.turn_no"] = fmt.Sprintf("%d", runtime.TurnNo)
	}
	if len(runtime.RequiredModalities) > 0 {
		metadata["tomako.required_modalities"] = strings.Join(runtime.RequiredModalities, ",")
	}
	return metadata
}

func (s *appServerSession) stageImages(prompt string, images []core.ImageAttachment) (string, []string, error) {
	if len(images) == 0 {
		return prompt, nil, nil
	}

	imgDir := filepath.Join(s.workDir, ".cc-connect", "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("codex app-server: create image dir: %w", err)
	}

	imagePaths := make([]string, 0, len(images))
	for i, img := range images {
		ext := codexImageExt(img.MimeType)
		fname := fmt.Sprintf("img_%d_%d%s", time.Now().UnixMilli(), i, ext)
		fpath := filepath.Join(imgDir, fname)
		if err := os.WriteFile(fpath, img.Data, 0o644); err != nil {
			return "", nil, fmt.Errorf("codex app-server: save image: %w", err)
		}
		imagePaths = append(imagePaths, fpath)
	}

	if strings.TrimSpace(prompt) == "" {
		prompt = "Please analyze the attached image(s)."
	}

	return prompt, imagePaths, nil
}

func (s *appServerSession) RespondPermission(requestID string, result core.PermissionResult) error {
	s.approvalsMu.Lock()
	ch := s.pendingApprovals[requestID]
	s.approvalsMu.Unlock()
	if ch == nil {
		return fmt.Errorf("codex app-server: no pending approval for request %s", requestID)
	}
	select {
	case ch <- result:
	default:
	}
	return nil
}

func (s *appServerSession) handleServerRequest(probe map[string]json.RawMessage) {
	rawID := probe["id"]
	var method string
	if err := json.Unmarshal(probe["method"], &method); err != nil {
		return
	}
	params := probe["params"]

	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		s.handleApprovalRequest(rawID, method, params)
	case "item/permissions/requestApproval":
		s.handlePermissionsApproval(rawID, params)
	case "item/tool/requestUserInput":
		s.handleRequestUserInput(rawID, params)
	case "item/tool/call":
		s.handleDynamicToolCall(rawID, params)
	default:
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"error": map[string]any{"code": -32601, "message": "method not found"},
		})
	}
}

func (s *appServerSession) handleApprovalRequest(rawID json.RawMessage, method string, paramsRaw json.RawMessage) {
	requestID := string(rawID)
	var params map[string]any
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return
	}

	toolName, toolInput := method, appServerJSON(params)
	switch method {
	case "item/commandExecution/requestApproval":
		toolName = "Bash"
		if cmd, _ := params["command"].(string); cmd != "" {
			toolInput = cmd
			if cwd, _ := params["cwd"].(string); cwd != "" {
				toolInput += "\n(in " + cwd + ")"
			}
		}
	case "item/fileChange/requestApproval":
		toolName = "Patch"
		if reason, _ := params["reason"].(string); reason != "" {
			toolInput = reason
		}
	}

	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     toolName,
		ToolInput:    toolInput,
		ToolInputRaw: params,
	})

	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		case <-timer.C:
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()

		decision := "decline"
		if strings.EqualFold(result.Behavior, "allow") {
			decision = "accept"
		}
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"result": map[string]any{"decision": decision},
		})
	}()
}

func (s *appServerSession) handlePermissionsApproval(rawID json.RawMessage, paramsRaw json.RawMessage) {
	requestID := string(rawID)
	var params map[string]any
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		return
	}

	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "Permissions",
		ToolInput:    appServerJSON(params),
		ToolInputRaw: params,
	})

	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		case <-timer.C:
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()

		if strings.EqualFold(result.Behavior, "allow") {
			perms := params["permissions"]
			if perms == nil {
				perms = map[string]any{}
			}
			_ = s.writeJSON(map[string]any{
				"jsonrpc": "2.0", "id": rawID,
				"result": map[string]any{"permissions": perms, "scope": "turn"},
			})
		} else {
			_ = s.writeJSON(map[string]any{
				"jsonrpc": "2.0", "id": rawID,
				"result": map[string]any{"permissions": map[string]any{}},
			})
		}
	}()
}

func (s *appServerSession) handleRequestUserInput(rawID json.RawMessage, paramsRaw json.RawMessage) {
	requestID := string(rawID)
	var params appServerRequestUserInputParams
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"error": map[string]any{"code": -32602, "message": "invalid params"},
		})
		return
	}

	questions := appServerRequestUserInputQuestions(params.Questions)
	if len(questions) == 0 {
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"result": appServerRequestUserInputResponse{Answers: map[string]appServerRequestUserInputAnswer{}},
		})
		return
	}

	rawInput := appServerRequestUserInputRawInput(params)
	ch := make(chan core.PermissionResult, 1)
	s.approvalsMu.Lock()
	s.pendingApprovals[requestID] = ch
	s.approvalsMu.Unlock()

	s.flushPendingAsThinking()
	s.emit(core.Event{
		Type:         core.EventPermissionRequest,
		RequestID:    requestID,
		ToolName:     "AskUserQuestion",
		ToolInput:    appServerJSON(rawInput),
		ToolInputRaw: rawInput,
		Questions:    questions,
	})

	go func() {
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		var result core.PermissionResult
		select {
		case result = <-ch:
		case <-s.ctx.Done():
			result = core.PermissionResult{Behavior: "deny"}
		case <-timer.C:
			result = core.PermissionResult{Behavior: "deny"}
		}
		s.approvalsMu.Lock()
		delete(s.pendingApprovals, requestID)
		s.approvalsMu.Unlock()

		response := appServerRequestUserInputResponseFromResult(params.Questions, result)
		_ = s.writeJSON(map[string]any{
			"jsonrpc": "2.0", "id": rawID,
			"result": response,
		})
	}()
}

func (s *appServerSession) handleDynamicToolCall(rawID json.RawMessage, paramsRaw json.RawMessage) {
	var params struct {
		CallID    string         `json:"callId"`
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(paramsRaw, &params); err != nil {
		s.writeDynamicToolResponse(rawID, false, "invalid tool arguments")
		return
	}
	if !s.isBrandAnalysisRuntime() {
		s.writeDynamicToolResponse(rawID, false, "tool not available for this task")
		return
	}
	go func() {
		switch params.Tool {
		case "collect_brand_evidence":
			if err := s.beginBrandEvidenceCollection(); err != nil {
				s.writeDynamicToolResponse(rawID, false, err.Error())
				return
			}
			result, err := s.collectBrandEvidence(params.Arguments)
			if err != nil {
				s.finishBrandEvidenceCollection(false)
				s.writeDynamicToolResponse(rawID, false, err.Error())
				return
			}
			if err := s.publishStructuredResult("evidence", result); err != nil {
				s.finishBrandEvidenceCollection(false)
				s.writeDynamicToolResponse(rawID, false, err.Error())
				return
			}
			s.finishBrandEvidenceCollection(true)
			encoded, _ := json.Marshal(brandEvidenceForModel(result))
			s.writeDynamicToolResponse(rawID, true, string(encoded))
		case "publish_brand_analysis_stage":
			stage, result, err := validateBrandAnalysisStage(params.Arguments)
			if err != nil {
				s.writeDynamicToolResponse(rawID, false, err.Error())
				return
			}
			if err := s.beginBrandAnalysisStagePublication(stage, result); err != nil {
				s.writeDynamicToolResponse(rawID, false, err.Error())
				return
			}
			if err := s.publishStructuredResult(stage, result); err != nil {
				s.finishBrandAnalysisStagePublication(stage, false)
				s.writeDynamicToolResponse(rawID, false, err.Error())
				return
			}
			s.finishBrandAnalysisStagePublication(stage, true)
			s.writeDynamicToolResponse(rawID, true, "stage accepted for persistence")
		default:
			s.writeDynamicToolResponse(rawID, false, "unknown dynamic tool")
		}
	}()
}

func (s *appServerSession) publishStructuredResult(stage string, result map[string]any) error {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ack := make(chan error, 1)
	event := core.Event{
		Type:        core.EventStructuredResult,
		Metadata:    map[string]any{"stage": stage, "result": result},
		DeliveryAck: ack,
	}
	select {
	case s.events <- event:
	case <-deliveryCtx.Done():
		return fmt.Errorf("structured result delivery unavailable: %w", deliveryCtx.Err())
	}
	select {
	case err := <-ack:
		if err != nil {
			return fmt.Errorf("structured result delivery failed: %w", err)
		}
		return nil
	case <-deliveryCtx.Done():
		return fmt.Errorf("structured result delivery not acknowledged: %w", deliveryCtx.Err())
	}
}

func (s *appServerSession) beginBrandEvidenceCollection() error {
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	if s.brandFlow.evidenceReady {
		return fmt.Errorf("brand evidence has already been collected")
	}
	if s.brandFlow.evidenceRunning {
		return fmt.Errorf("brand evidence collection is already running")
	}
	if s.brandFlow.corePublished || s.brandFlow.competitorsPublished {
		return fmt.Errorf("brand evidence cannot be collected after analysis publication")
	}
	s.brandFlow.evidenceRunning = true
	return nil
}

func (s *appServerSession) finishBrandEvidenceCollection(success bool) {
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	s.brandFlow.evidenceRunning = false
	if success {
		s.brandFlow.evidenceReady = true
	}
}

func (s *appServerSession) beginBrandAnalysisStagePublication(stage string, result map[string]any) error {
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	switch stage {
	case "core":
		if !s.brandFlow.evidenceReady {
			return fmt.Errorf("core cannot be published before brand evidence is ready")
		}
		if s.brandFlow.searchAttempts > 0 {
			return fmt.Errorf("core cannot be published after native web search has started")
		}
		if s.brandFlow.corePublished || s.brandFlow.corePublishing {
			return fmt.Errorf("core has already been published")
		}
		s.brandFlow.corePublishing = true
	case "competitors":
		if !s.brandFlow.corePublished {
			return fmt.Errorf("competitors cannot be published before core")
		}
		if s.brandFlow.competitorsPublished || s.brandFlow.competitorsPublishing {
			return fmt.Errorf("competitors have already been published")
		}
		if s.brandFlow.searchAttempts > 1 {
			return fmt.Errorf("competitors cannot be published after more than one native web search attempt")
		}
		status, _ := result["status"].(string)
		if status != "unavailable" && !s.brandFlow.searchCompleted {
			return fmt.Errorf("ready competitors require one completed native web search")
		}
		if status == "unavailable" && s.brandFlow.searchTraceID != "" && !s.brandFlow.searchCompleted {
			return fmt.Errorf("competitors cannot be closed while native web search is running")
		}
		s.brandFlow.competitorsPublishing = true
	default:
		return fmt.Errorf("unsupported brand analysis stage")
	}
	return nil
}

func (s *appServerSession) finishBrandAnalysisStagePublication(stage string, success bool) {
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	switch stage {
	case "core":
		s.brandFlow.corePublishing = false
		if success {
			s.brandFlow.corePublished = true
		}
	case "competitors":
		s.brandFlow.competitorsPublishing = false
		if success {
			s.brandFlow.competitorsPublished = true
		}
	}
}

func (s *appServerSession) advanceBrandAnalysisStage(stage string, result map[string]any) error {
	if err := s.beginBrandAnalysisStagePublication(stage, result); err != nil {
		return err
	}
	s.finishBrandAnalysisStagePublication(stage, true)
	return nil
}

func (s *appServerSession) noteBrandWebSearchStarted(traceID string) {
	if !s.isBrandAnalysisRuntime() {
		return
	}
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	s.brandFlow.searchAttempts++
	if !s.brandFlow.corePublished {
		slog.Warn("brand analysis native web search started before core", "trace_id", traceID)
		return
	}
	if s.brandFlow.competitorsPublished {
		slog.Warn("brand analysis native web search started after competitors", "trace_id", traceID)
		return
	}
	if s.brandFlow.searchTraceID != "" {
		slog.Warn("brand analysis attempted more than one native web search",
			"trace_id", traceID, "attempt", s.brandFlow.searchAttempts)
		return
	}
	s.brandFlow.searchTraceID = traceID
}

func (s *appServerSession) noteBrandWebSearchCompleted(traceID string) {
	if !s.isBrandAnalysisRuntime() {
		return
	}
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	if traceID != "" && traceID == s.brandFlow.searchTraceID {
		s.brandFlow.searchCompleted = true
	}
}

func brandEvidenceForModel(result map[string]any) map[string]any {
	compact := make(map[string]any, 16)
	compact["workflow"] = map[string]any{
		"state": "evidence_collected", "corePersisted": false,
		"nextAction": "Call publish_brand_analysis_stage with stage=core and result containing the evidence-grounded core profile. Finish only after the tool accepts core; collecting evidence does not save the core profile.",
	}
	for _, key := range []string{
		"brandName", "productName", "canonicalUrl", "oneLiner", "description",
		"productType", "audience",
	} {
		if value, ok := result[key]; ok && value != nil {
			if text := boundedText(value, 1200); text != "" {
				compact[key] = text
			}
		}
	}
	for _, key := range []string{"categories", "targetMarkets", "keyFeatures", "slogans", "headings"} {
		if values := normalizedStringList(result[key], 12, 320); len(values) > 0 {
			compact[key] = stringsToAny(values)
		}
	}
	if meta, ok := result["meta"].(map[string]any); ok {
		compact["meta"] = boundedEvidenceObject(meta, 2)
	}
	if jsonLD := compactJSONLDEvidence(result["jsonLd"]); len(jsonLD) > 0 {
		compact["jsonLd"] = jsonLD
	}
	if pages := compactBrandEvidencePages(result["evidencePages"]); len(pages) > 0 {
		compact["evidencePages"] = pages
	}
	return compact
}

func compactBrandEvidencePages(raw any) []any {
	items, _ := raw.([]any)
	pages := make([]any, 0, min(len(items), 6))
	for _, item := range items {
		page, ok := item.(map[string]any)
		if !ok {
			continue
		}
		compact := map[string]any{}
		for _, key := range []string{"url", "role", "title", "description"} {
			if value := boundedText(page[key], map[string]int{
				"url": 2048, "role": 32, "title": 240, "description": 500,
			}[key]); value != "" {
				compact[key] = value
			}
		}
		if values := normalizedStringList(page["headings"], 8, 240); len(values) > 0 {
			compact["headings"] = stringsToAny(values)
		}
		if values := normalizedStringList(page["snippets"], 8, 360); len(values) > 0 {
			compact["snippets"] = stringsToAny(values)
		}
		if len(compact) > 0 {
			pages = append(pages, compact)
		}
		if len(pages) == 6 {
			break
		}
	}
	return pages
}

func compactJSONLDEvidence(raw any) []any {
	items, _ := raw.([]any)
	compact := make([]any, 0, min(len(items), 5))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		selected := map[string]any{}
		for _, key := range []string{
			"@type", "name", "description", "brand", "applicationCategory",
			"operatingSystem", "audience", "offers",
		} {
			if value, exists := object[key]; exists {
				selected[key] = boundedEvidenceValue(value, 2)
			}
		}
		if len(selected) > 0 {
			compact = append(compact, selected)
		}
		if len(compact) == 5 {
			break
		}
	}
	return compact
}

func boundedEvidenceObject(raw map[string]any, depth int) map[string]any {
	compact := make(map[string]any, min(len(raw), 16))
	count := 0
	for key, value := range raw {
		if count == 16 {
			break
		}
		compact[boundedText(key, 80)] = boundedEvidenceValue(value, depth)
		count++
	}
	return compact
}

func boundedEvidenceValue(raw any, depth int) any {
	if depth <= 0 {
		return boundedText(fmt.Sprint(raw), 400)
	}
	switch value := raw.(type) {
	case string:
		return boundedText(value, 500)
	case float64, int, int64, bool:
		return value
	case map[string]any:
		return boundedEvidenceObject(value, depth-1)
	case []any:
		items := make([]any, 0, min(len(value), 6))
		for _, item := range value {
			items = append(items, boundedEvidenceValue(item, depth-1))
			if len(items) == 6 {
				break
			}
		}
		return items
	default:
		return boundedText(fmt.Sprint(value), 400)
	}
}

func (s *appServerSession) writeDynamicToolResponse(rawID json.RawMessage, success bool, message string) {
	_ = s.writeJSON(map[string]any{
		"jsonrpc": "2.0", "id": rawID,
		"result": map[string]any{
			"success":      success,
			"contentItems": []map[string]any{{"type": "inputText", "text": message}},
		},
	})
}

func (s *appServerSession) isBrandAnalysisRuntime() bool {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.EqualFold(strings.TrimSpace(s.runtime.Scene), "brand_analysis")
}

func (s *appServerSession) brandCoreAwaitingPublication() bool {
	if !s.isBrandAnalysisRuntime() {
		return false
	}
	s.brandFlowMu.Lock()
	defer s.brandFlowMu.Unlock()
	return !s.brandFlow.corePublished
}

func brandAnalysisDynamicTools() []map[string]any {
	return []map[string]any{
		{
			"type":        "function",
			"name":        "collect_brand_evidence",
			"description": "Fetch the submitted official website once and return deterministic first-party brand evidence. Call this before semantic analysis.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "format": "uri"},
				},
				"required":             []string{"url"},
				"additionalProperties": false,
			},
			"deferLoading": false,
		},
		{
			"type":        "function",
			"name":        "publish_brand_analysis_stage",
			"description": "Persist one validated brand-analysis stage. Core-only onboarding must publish core before finishing; the backend starts its independent competitor task. Collecting evidence alone does not persist the core profile.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"stage":  map[string]any{"type": "string", "enum": []string{"core", "competitors"}},
					"result": map[string]any{"type": "object"},
				},
				"required":             []string{"stage", "result"},
				"additionalProperties": false,
			},
			"deferLoading": false,
		},
	}
}

func (s *appServerSession) collectBrandEvidence(arguments map[string]any) (map[string]any, error) {
	rawURL, _ := arguments["url"].(string)
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("url must be an absolute public http/https URL")
	}
	if !isPublicHostname(parsed.Hostname()) {
		return nil, fmt.Errorf("url host must be public")
	}
	script := strings.TrimSpace(os.Getenv("TOMAKO_BRAND_CRAWL_SCRIPT"))
	if script == "" {
		script = "/home/ubuntu/Skills-OL/brand-crawl.mjs"
	}
	ctx, cancel := context.WithTimeout(s.ctx, 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script, parsed.String(),
		"--onboarding-fast", "--max-screenshots=0", "--emit-platform-result")
	cmd.Dir = s.workDir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("brand evidence collection timed out")
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 600 {
			message = message[:600]
		}
		return nil, fmt.Errorf("brand evidence collection failed: %s", message)
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		var event struct {
			Type   string         `json:"type"`
			Result map[string]any `json:"result"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "platform_result" && event.Result != nil {
			return event.Result, nil
		}
	}
	return nil, fmt.Errorf("brand evidence collector returned no structured result")
}

func validateBrandAnalysisStage(arguments map[string]any) (string, map[string]any, error) {
	stage, _ := arguments["stage"].(string)
	result, _ := arguments["result"].(map[string]any)
	if result == nil {
		return "", nil, fmt.Errorf("result must be an object")
	}
	allowed := map[string]struct{}{}
	switch stage {
	case "core":
		clean, err := normalizeCoreBrandAnalysis(result)
		if err != nil {
			return "", nil, err
		}
		return stage, clean, nil
	case "competitors":
		allowed["competitors"] = struct{}{}
		allowed["status"] = struct{}{}
	default:
		return "", nil, fmt.Errorf("unsupported brand analysis stage")
	}
	clean := make(map[string]any, len(result))
	for key, value := range result {
		if _, ok := allowed[key]; ok {
			clean[key] = value
		}
	}
	if stage == "competitors" {
		clean["competitors"] = normalizeCompetitorCandidates(result["competitors"])
		if len(clean["competitors"].([]any)) == 0 {
			clean["status"] = "unavailable"
		} else {
			clean["status"] = "complete"
		}
	}
	return stage, clean, nil
}

func normalizeCoreBrandAnalysis(result map[string]any) (map[string]any, error) {
	clean := make(map[string]any, 11)
	for _, key := range []string{"productName", "brandName", "oneLiner", "description", "audience"} {
		if value := boundedText(result[key], 1200); value != "" {
			clean[key] = value
		}
	}
	productType := boundedText(result["productType"], 48)
	if _, ok := stringSet(
		"SaaS", "Software", "Hardware", "Service", "E-commerce", "Marketplace",
		"Media / Content", "Game", "API / Developer Tool", "Agency", "Other",
	)[productType]; !ok {
		return nil, fmt.Errorf("core.productType is invalid")
	}
	clean["productType"] = productType
	platforms := normalizedStringList(result["platforms"], 9, 48)
	allowedPlatforms := stringSet(
		"Web", "iOS", "Android", "WeChat Mini Program", "Desktop", "API",
		"Browser Extension", "Physical / Offline", "Other",
	)
	for _, platform := range platforms {
		if _, ok := allowedPlatforms[platform]; !ok {
			return nil, fmt.Errorf("core.platforms contains an invalid value")
		}
	}
	if len(platforms) == 0 {
		return nil, fmt.Errorf("core.platforms is required")
	}
	clean["platforms"] = stringsToAny(platforms)
	features := normalizedStringList(result["keyFeatures"], 8, 240)
	if len(features) < 3 {
		return nil, fmt.Errorf("core.keyFeatures requires at least 3 items")
	}
	clean["keyFeatures"] = stringsToAny(features)
	if categories := normalizedStringList(result["categories"], 4, 80); len(categories) > 0 {
		clean["categories"] = stringsToAny(categories)
	}
	if markets := normalizedStringList(result["targetMarkets"], 8, 80); len(markets) > 0 {
		clean["targetMarkets"] = stringsToAny(markets)
	}
	segments := normalizeAudienceSegments(result["audienceSegments"])
	if len(segments) > 0 {
		clean["audienceSegments"] = segments
	}
	if _, hasAudience := clean["audience"]; !hasAudience && len(segments) == 0 {
		return nil, fmt.Errorf("core audience evidence is required")
	}
	return clean, nil
}

func normalizeAudienceSegments(raw any) []any {
	items, _ := raw.([]any)
	clean := make([]any, 0, 4)
	seen := make(map[string]struct{})
	for _, item := range items {
		segment, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := boundedText(segment["name"], 100)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, map[string]any{
			"name":        name,
			"description": boundedText(segment["description"], 400),
		})
		if len(clean) == 4 {
			break
		}
	}
	return clean
}

func normalizeCompetitorCandidates(raw any) []any {
	items, _ := raw.([]any)
	clean := make([]any, 0, len(items))
	seenNames := map[string]struct{}{}
	seenHosts := map[string]struct{}{}
	for _, item := range items {
		candidate, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(stringFromAny(candidate["name"]))
		parsed, err := url.Parse(strings.TrimSpace(stringFromAny(candidate["websiteUrl"])))
		host := strings.ToLower(parsed.Hostname())
		if err != nil || name == "" || parsed.User != nil || host == "" || !strings.Contains(host, ".") {
			continue
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		if parsed.Port() != "" && parsed.Port() != "80" && parsed.Port() != "443" {
			continue
		}
		if !isPublicHostname(host) {
			continue
		}
		nameKey := strings.ToLower(name)
		if _, duplicate := seenNames[nameKey]; duplicate {
			continue
		}
		if _, duplicate := seenHosts[host]; duplicate {
			continue
		}
		seenNames[nameKey] = struct{}{}
		seenHosts[host] = struct{}{}
		relationship := stringFromAny(candidate["relationship"])
		if relationship != "direct" && relationship != "adjacent" && relationship != "alternative" {
			relationship = "adjacent"
		}
		confidence := stringFromAny(candidate["confidence"])
		if confidence != "medium" {
			confidence = "low"
		}
		clean = append(clean, map[string]any{
			"name":         boundedText(name, 120),
			"websiteUrl":   "https://" + host + "/",
			"businessLine": boundedText(candidate["businessLine"], 200),
			"angle":        boundedText(candidate["angle"], 400),
			"relationship": relationship,
			"confidence":   confidence,
		})
		if len(clean) == 5 {
			break
		}
	}
	return clean
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func nonEmptyAnySlice(value any) bool {
	items, ok := value.([]any)
	return ok && len(items) > 0
}
func boundedText(value any, max int) string {
	text := strings.TrimSpace(stringFromAny(value))
	if len(text) > max {
		return text[:max]
	}
	return text
}

func normalizedStringList(raw any, limit, maxLength int) []string {
	items, _ := raw.([]any)
	clean := make([]string, 0, min(len(items), limit))
	seen := make(map[string]struct{})
	for _, item := range items {
		value := boundedText(item, maxLength)
		key := strings.ToLower(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		clean = append(clean, value)
		if len(clean) == limit {
			break
		}
	}
	return clean
}

func stringsToAny(values []string) []any {
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	return items
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func isPublicHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	if address, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() &&
			!address.IsLinkLocalUnicast() && !address.IsUnspecified()
	}
	return strings.Contains(host, ".")
}

func (s *appServerSession) emitLifecycle(stage string, duration time.Duration) {
	s.emit(core.Event{
		Type:       core.EventLifecycle,
		TraceID:    fmt.Sprintf("lifecycle-%s-%d", stage, time.Now().UnixNano()),
		ToolName:   "AgentLifecycle",
		ToolInput:  stage,
		ToolStatus: "completed",
		Metadata:   map[string]any{"duration_ms": duration.Milliseconds()},
	})
}

func appServerRequestUserInputQuestions(input []appServerRequestUserInputQuestion) []core.UserQuestion {
	questions := make([]core.UserQuestion, 0, len(input))
	for _, in := range input {
		questionText := strings.TrimSpace(in.Question)
		if questionText == "" {
			continue
		}
		q := core.UserQuestion{
			ID:       strings.TrimSpace(in.ID),
			Question: questionText,
			Header:   strings.TrimSpace(in.Header),
		}
		if q.ID == "" {
			q.ID = fmt.Sprintf("question-%d", len(questions)+1)
		}
		for _, opt := range in.Options {
			q.Options = append(q.Options, core.UserQuestionOption{
				ID:          fmt.Sprintf("%s-option-%d", q.ID, len(q.Options)+1),
				Label:       strings.TrimSpace(opt.Label),
				Description: strings.TrimSpace(opt.Description),
			})
		}
		questions = append(questions, q)
	}
	return questions
}

func appServerRequestUserInputRawInput(params appServerRequestUserInputParams) map[string]any {
	questions := make([]any, 0, len(params.Questions))
	for _, in := range params.Questions {
		q := map[string]any{
			"id":       in.ID,
			"header":   in.Header,
			"question": in.Question,
			"isOther":  in.IsOther,
			"isSecret": in.IsSecret,
			"options":  appServerRequestUserInputRawOptions(in.Options),
		}
		questions = append(questions, q)
	}
	return map[string]any{
		"threadId":  params.ThreadID,
		"turnId":    params.TurnID,
		"itemId":    params.ItemID,
		"questions": questions,
	}
}

func appServerRequestUserInputRawOptions(options []appServerRequestUserInputOption) []any {
	out := make([]any, 0, len(options))
	for _, opt := range options {
		out = append(out, map[string]any{
			"label":       opt.Label,
			"description": opt.Description,
		})
	}
	return out
}

func appServerRequestUserInputResponseFromResult(questions []appServerRequestUserInputQuestion, result core.PermissionResult) appServerRequestUserInputResponse {
	response := appServerRequestUserInputResponse{Answers: map[string]appServerRequestUserInputAnswer{}}
	if !strings.EqualFold(result.Behavior, "allow") {
		return response
	}

	answersRaw, _ := result.UpdatedInput["answers"].(map[string]any)
	if len(answersRaw) == 0 {
		return response
	}

	for _, q := range questions {
		id := strings.TrimSpace(q.ID)
		text := strings.TrimSpace(q.Question)
		if id == "" || text == "" {
			continue
		}
		// Bridge-native interactions answer by the stable question ID. Older
		// adapters used the rendered question text, so retain that only as a
		// compatibility fallback. Looking up text alone silently converted a
		// valid multi-select response into {"answers":{}} for Codex.
		answer := answersRaw[id]
		if answer == nil {
			answer = answersRaw[text]
		}
		values := appServerRequestUserInputAnswerValues(answer)
		if len(values) == 0 {
			continue
		}
		response.Answers[id] = appServerRequestUserInputAnswer{Answers: values}
	}
	return response
}

func appServerRequestUserInputAnswerValues(raw any) []string {
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []string:
		values := make([]string, 0, len(v))
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				values = append(values, s)
			}
		}
		return values
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				values = append(values, s)
			}
		}
		return values
	case map[string]any:
		return appServerRequestUserInputAnswerValues(v["answers"])
	case appServerRequestUserInputAnswer:
		return appServerRequestUserInputAnswerValues(v.Answers)
	default:
		return nil
	}
}

func (s *appServerSession) rejectPendingApprovals(err error) {
	s.approvalsMu.Lock()
	defer s.approvalsMu.Unlock()
	for id, ch := range s.pendingApprovals {
		delete(s.pendingApprovals, id)
		select {
		case ch <- core.PermissionResult{Behavior: "deny"}:
		default:
		}
	}
}

func (s *appServerSession) Events() <-chan core.Event {
	return s.events
}

func (s *appServerSession) CurrentSessionID() string {
	v, _ := s.threadID.Load().(string)
	return v
}

func (s *appServerSession) GetWorkDir() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.workDir
}

func (s *appServerSession) GetModel() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.TrimSpace(s.model)
}

func (s *appServerSession) GetReasoningEffort() string {
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return strings.TrimSpace(s.effort)
}

func (s *appServerSession) GetUsage(ctx context.Context) (*core.UsageReport, error) {
	if err := s.refreshUsage(ctx); err != nil {
		if cached := s.cachedUsage(); cached != nil {
			return cached, nil
		}
		return nil, err
	}
	if cached := s.cachedUsage(); cached != nil {
		return cached, nil
	}
	return nil, fmt.Errorf("codex app-server usage unavailable")
}

func (s *appServerSession) GetContextUsage() *core.ContextUsage {
	return s.cachedContextUsage()
}

func (s *appServerSession) Alive() bool {
	return s.alive.Load()
}

func (s *appServerSession) Close() error {
	s.alive.Store(false)
	s.runtimeMu.Lock()
	removeTaskRuntimeEnv(s.taskRuntimeEnvFile)
	s.taskRuntimeEnvFile = ""
	s.runtimeMu.Unlock()
	s.cancel()

	s.procMu.Lock()
	if s.stdin != nil {
		_ = s.stdin.Close()
		s.stdin = nil
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	s.procMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	s.closeOnce.Do(func() {
		close(s.events)
	})
	return nil
}

func (s *appServerSession) readLoop(r io.Reader) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(r)
	scanBuf := make([]byte, 0, 64*1024)
	const maxLineSize = 10 * 1024 * 1024 // 10MB
	scanner.Buffer(scanBuf, maxLineSize)

	for scanner.Scan() {
		select {
		case <-s.ctx.Done():
			return
		default:
		}

		data := scanner.Bytes()

		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			slog.Debug("codex app-server: invalid JSON", "error", err)
			continue
		}

		_, hasID := probe["id"]
		_, hasMethod := probe["method"]

		switch {
		case hasID && !hasMethod:
			// Response to one of our requests.
			var resp rpcResponseEnvelope
			if err := json.Unmarshal(data, &resp); err != nil {
				slog.Debug("codex app-server: bad response envelope", "error", err)
				continue
			}
			s.handleResponse(resp)

		case hasID && hasMethod:
			// Server-initiated request that requires a response (e.g. approval).
			s.handleServerRequest(probe)

		default:
			// Notification (no id).
			var notif rpcNotificationEnvelope
			if err := json.Unmarshal(data, &notif); err != nil {
				slog.Debug("codex app-server: bad notification envelope", "error", err)
				continue
			}
			s.handleNotification(notif.Method, notif.Params)
		}
	}

	err := scanner.Err()
	if err != nil {
		if s.ctx.Err() == nil && !errors.Is(err, io.EOF) {
			slog.Warn("codex app-server read failed", "error", err)
			if errors.Is(err, bufio.ErrTooLong) {
				s.emitError(fmt.Errorf("codex app-server line exceeds max size (%d bytes): %w", maxLineSize, err))
			} else {
				s.emitError(fmt.Errorf("codex app-server connection closed: %w", err))
			}
		}
		s.alive.Store(false)
		s.rejectPending(err)
		s.rejectPendingApprovals(err)
		return
	}

	s.alive.Store(false)
	s.rejectPending(io.EOF)
	s.rejectPendingApprovals(io.EOF)
}

func (s *appServerSession) stderrLoop(r io.Reader) {
	defer s.wg.Done()
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		slog.Debug("codex app-server stderr", "line", line)
	}
	if err := scanner.Err(); err != nil && s.ctx.Err() == nil {
		slog.Debug("codex app-server stderr read failed", "error", err)
	}
}

func (s *appServerSession) waitLoop() {
	defer s.wg.Done()

	s.procMu.Lock()
	cmd := s.cmd
	s.procMu.Unlock()
	if cmd == nil {
		return
	}

	err := cmd.Wait()
	if s.ctx.Err() == nil && err != nil {
		slog.Warn("codex app-server exited unexpectedly", "error", err)
		s.emitError(fmt.Errorf("codex app-server exited: %w", err))
	}
	s.alive.Store(false)
	if err == nil {
		err = io.EOF
	}
	s.rejectPending(err)
}

func (s *appServerSession) handleResponse(resp rpcResponseEnvelope) {
	id, ok := rpcIDToInt64(resp.ID)
	if !ok {
		return
	}

	s.pendingMu.Lock()
	ch := s.pending[id]
	delete(s.pending, id)
	s.pendingMu.Unlock()

	if ch == nil {
		return
	}

	select {
	case ch <- resp:
	default:
	}
}

func (s *appServerSession) handleNotification(method string, paramsRaw json.RawMessage) {
	switch method {
	case "turn/started":
		var notif turnNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.stateMu.Lock()
			s.currentTurn = notif.Turn.ID
			s.pendingMsgs = s.pendingMsgs[:0]
			s.streamedItems = nil
			s.lastStreamedItem = ""
			s.stateMu.Unlock()
			s.storeContextUsage(nil)
		}

	case "item/started":
		var notif itemNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.handleItemStarted(notif.Item)
		}

	case "item/agentMessage/delta":
		var notif struct {
			ItemID string `json:"itemId"`
			Delta  string `json:"delta"`
		}
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.handleAgentMessageDelta(notif.ItemID, notif.Delta)
		}

	case "item/completed":
		var notif itemNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.handleItemCompleted(notif.Item)
		}

	case "turn/completed":
		var notif turnNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			if notif.ThreadID != "" && notif.ThreadID != s.CurrentSessionID() {
				return
			}
			var turnErr error
			switch {
			case notif.Turn.Error != nil && strings.TrimSpace(notif.Turn.Error.Message) != "":
				turnErr = fmt.Errorf("codex app-server turn failed: %s", notif.Turn.Error.Message)
			case notif.Turn.Status == "failed", notif.Turn.Status == "interrupted":
				turnErr = fmt.Errorf("codex app-server turn %s", notif.Turn.Status)
			}
			s.completeTurn(notif.Turn.ID, turnErr)
		}

	case "turn/plan/updated":
		var notif struct {
			Plan []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			tasks := make([]core.ProgressTask, 0, len(notif.Plan))
			for index, item := range notif.Plan {
				step := strings.Join(strings.Fields(strings.TrimSpace(item.Step)), " ")
				if step == "" {
					continue
				}
				status := core.ProgressTaskPending
				switch strings.ToLower(strings.TrimSpace(item.Status)) {
				case "inprogress", "in_progress":
					status = core.ProgressTaskInProgress
				case "completed":
					status = core.ProgressTaskCompleted
				case "failed":
					status = core.ProgressTaskFailed
				}
				tasks = append(tasks, core.ProgressTask{
					ID:     fmt.Sprintf("task-%d", index+1),
					Title:  step,
					Status: status,
				})
			}
			if len(tasks) > 0 {
				s.emit(core.Event{Type: core.EventPlanUpdate, ProgressTasks: tasks})
			}
		}

	case "thread/status/changed":
		// Idle describes thread activity, not success. Only turn/completed
		// carries the authoritative completed/failed/interrupted outcome.

	case "account/rateLimits/updated":
		var notif appServerRateLimitsResponse
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.storeUsage(mapAppServerRateLimits(notif))
		}

	case "thread/tokenUsage/updated":
		var notif appServerThreadTokenUsageNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			s.storeContextUsage(mapAppServerTokenUsage(notif))
		}

	case "error":
		var notif errorNotification
		if err := json.Unmarshal(paramsRaw, &notif); err == nil {
			if notif.ThreadID != "" && notif.ThreadID != s.CurrentSessionID() {
				return
			}
			if notif.WillRetry {
				slog.Warn("codex app-server retrying after transient error", "turn_id", notif.TurnID)
				return
			}
			message := notif.Message
			if notif.Error != nil {
				message = notif.Error.Message
			}
			if strings.TrimSpace(message) != "" {
				failure := fmt.Errorf("codex app-server: %s", message)
				if notif.TurnID == "" {
					s.emitError(failure)
				} else {
					s.completeTurn(notif.TurnID, failure)
				}
			}
		}
	}
}

func (s *appServerSession) handleItemStarted(item map[string]any) {
	itemType, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	if itemType == "" {
		return
	}

	switch itemType {
	case "agentMessage", "reasoning", "userMessage", "plan", "hookPrompt", "contextCompaction":
		return
	}

	s.flushPendingAsThinking()

	switch itemType {
	case "commandExecution":
		command, _ := item["command"].(string)
		s.emit(core.Event{Type: core.EventToolUse, TraceID: itemID, ToolName: "Bash", ToolInput: command})

	case "mcpToolCall":
		server, _ := item["server"].(string)
		tool, _ := item["tool"].(string)
		name := strings.Trim(strings.Join([]string{server, tool}, ":"), ":")
		s.emit(core.Event{Type: core.EventToolUse, TraceID: itemID, ToolName: "MCP", ToolInput: name + "\n" + appServerJSON(item["arguments"])})

	case "webSearch":
		query, _ := item["query"].(string)
		s.noteBrandWebSearchStarted(itemID)
		s.emit(core.Event{Type: core.EventToolUse, TraceID: itemID, ToolName: "WebSearch", ToolInput: query})

	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		s.emit(core.Event{Type: core.EventToolUse, TraceID: itemID, ToolName: tool, ToolInput: appServerJSON(item["arguments"])})

	case "fileChange":
		s.emit(core.Event{Type: core.EventToolUse, TraceID: itemID, ToolName: "Patch", ToolInput: appServerJSON(item["changes"])})
	}
}

func (s *appServerSession) handleItemCompleted(item map[string]any) {
	itemType, _ := item["type"].(string)
	itemID, _ := item["id"].(string)
	if itemType == "" {
		return
	}

	switch itemType {
	case "reasoning":
		text := appServerReasoningText(item)
		if text != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: text})
		}

	case "agentMessage":
		text, _ := item["text"].(string)
		if strings.TrimSpace(text) == "" {
			return
		}
		if len(s.outputSchema()) > 0 {
			// Schema-constrained progress may also look like JSON. The native
			// phase, not its shape, identifies the terminal structured answer.
			switch item["phase"] {
			case "commentary":
				s.emit(core.Event{Type: core.EventThinking, Content: text})
				return
			case "final_answer":
				s.flushPendingAsThinking()
				s.emit(core.Event{Type: core.EventText, Content: text})
				return
			}
			// Older providers omit phase; retain the tool-boundary fallback.
		}
		itemID, _ := item["id"].(string)
		s.stateMu.Lock()
		streamed, wasStreamed := "", false
		if itemID != "" && s.streamedItems != nil {
			streamed, wasStreamed = s.streamedItems[itemID]
			if wasStreamed {
				delete(s.streamedItems, itemID)
			}
		}
		if !wasStreamed {
			s.pendingMsgs = append(s.pendingMsgs, text)
		}
		s.stateMu.Unlock()
		if wasStreamed {
			// The live delta stream already delivered this message; emit only
			// a missing tail (e.g. the final flush the server may skip).
			if tail, ok := strings.CutPrefix(text, streamed); ok && tail != "" {
				s.emit(core.Event{Type: core.EventText, Content: tail})
			}
		}

	case "commandExecution":
		command, _ := item["command"].(string)
		status, _ := item["status"].(string)
		output, _ := item["aggregatedOutput"].(string)
		exitCode, hasExitCode := toInt(item["exitCode"])
		var exitCodePtr *int
		if hasExitCode {
			exitCodePtr = &exitCode
		}
		success := appServerToolSuccess(status, exitCodePtr)
		s.emit(core.Event{
			Type:         core.EventToolResult,
			TraceID:      itemID,
			ToolName:     "Bash",
			ToolInput:    command,
			ToolResult:   truncate(strings.TrimSpace(output), 500),
			ToolStatus:   strings.TrimSpace(status),
			ToolExitCode: exitCodePtr,
			ToolSuccess:  &success,
		})

	case "mcpToolCall":
		tool, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		result := appServerJSON(item["result"])
		if errText := appServerJSON(item["error"]); strings.TrimSpace(errText) != "" && result == "" {
			result = errText
		}
		success := appServerToolSuccess(status, nil)
		s.emit(core.Event{
			Type:        core.EventToolResult,
			TraceID:     itemID,
			ToolName:    tool,
			ToolResult:  truncate(strings.TrimSpace(result), 500),
			ToolStatus:  strings.TrimSpace(status),
			ToolSuccess: &success,
		})

	case "webSearch":
		query, _ := item["query"].(string)
		s.noteBrandWebSearchCompleted(itemID)
		s.emit(core.Event{
			Type:       core.EventToolResult,
			TraceID:    itemID,
			ToolName:   "WebSearch",
			ToolResult: truncate(strings.TrimSpace(query), 500),
		})

	case "dynamicToolCall":
		tool, _ := item["tool"].(string)
		status, _ := item["status"].(string)
		result := appServerDynamicToolText(item["contentItems"])
		success := appServerToolSuccess(status, nil)
		s.emit(core.Event{
			Type:        core.EventToolResult,
			TraceID:     itemID,
			ToolName:    tool,
			ToolResult:  truncate(strings.TrimSpace(result), 500),
			ToolStatus:  strings.TrimSpace(status),
			ToolSuccess: &success,
		})
	}
}

func appServerReasoningText(item map[string]any) string {
	var parts []string
	if summary, ok := item["summary"].([]any); ok {
		for _, entry := range summary {
			if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	if len(parts) == 0 {
		if content, ok := item["content"].([]any); ok {
			for _, entry := range content {
				if text, ok := entry.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func appServerDynamicToolText(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return appServerJSON(raw)
	}
	var parts []string
	for _, entry := range items {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := m["text"].(string); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return appServerJSON(raw)
	}
	return strings.Join(parts, "\n")
}

func appServerToolSuccess(status string, exitCode *int) bool {
	s := strings.ToLower(strings.TrimSpace(status))
	if exitCode != nil {
		return *exitCode == 0
	}
	return s == "completed" || s == "success" || s == "succeeded" || s == "ok"
}

func mapAppServerRateLimits(payload appServerRateLimitsResponse) *core.UsageReport {
	report := &core.UsageReport{Provider: "codex"}

	var snapshots []appServerRateLimitSnapshot
	if len(payload.RateLimitsByLimitID) > 0 {
		keys := make([]string, 0, len(payload.RateLimitsByLimitID))
		for key := range payload.RateLimitsByLimitID {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			snapshots = append(snapshots, payload.RateLimitsByLimitID[key])
		}
	} else if payload.RateLimits.LimitID != "" || payload.RateLimits.Primary != nil || payload.RateLimits.Secondary != nil || payload.RateLimits.Credits != nil {
		snapshots = append(snapshots, payload.RateLimits)
	}

	for _, snapshot := range snapshots {
		if report.Plan == "" && strings.TrimSpace(snapshot.PlanType) != "" {
			report.Plan = strings.TrimSpace(snapshot.PlanType)
		}
		if report.Credits == nil && snapshot.Credits != nil {
			report.Credits = &core.UsageCredits{
				HasCredits: snapshot.Credits.HasCredits,
				Unlimited:  snapshot.Credits.Unlimited,
			}
			if snapshot.Credits.Balance != nil {
				report.Credits.Balance = strings.TrimSpace(*snapshot.Credits.Balance)
			}
		}

		windows := appServerUsageWindows(snapshot)
		if len(windows) == 0 {
			continue
		}
		limitReached := false
		for _, window := range windows {
			if window.UsedPercent >= 100 {
				limitReached = true
				break
			}
		}

		report.Buckets = append(report.Buckets, core.UsageBucket{
			Name:         appServerBucketName(snapshot),
			Allowed:      !limitReached,
			LimitReached: limitReached,
			Windows:      windows,
		})
	}

	return report
}

func appServerBucketName(snapshot appServerRateLimitSnapshot) string {
	if name := strings.TrimSpace(snapshot.LimitName); name != "" {
		return name
	}
	if id := strings.TrimSpace(snapshot.LimitID); id != "" {
		return id
	}
	return "Rate limit"
}

func appServerUsageWindows(snapshot appServerRateLimitSnapshot) []core.UsageWindow {
	var windows []core.UsageWindow
	if snapshot.Primary != nil {
		windows = append(windows, appServerUsageWindow("Primary", snapshot.Primary))
	}
	if snapshot.Secondary != nil {
		windows = append(windows, appServerUsageWindow("Secondary", snapshot.Secondary))
	}
	return windows
}

func appServerUsageWindow(name string, window *appServerRateLimitWindow) core.UsageWindow {
	resetAfter := 0
	if window != nil && window.ResetsAt > 0 {
		resetAfter = int(time.Until(time.Unix(window.ResetsAt, 0)).Seconds())
		if resetAfter < 0 {
			resetAfter = 0
		}
	}
	return core.UsageWindow{
		Name:              name,
		UsedPercent:       window.UsedPercent,
		WindowSeconds:     window.WindowDurationMins * 60,
		ResetAfterSeconds: resetAfter,
		ResetAtUnix:       window.ResetsAt,
	}
}

func cloneUsageReport(report *core.UsageReport) *core.UsageReport {
	if report == nil {
		return nil
	}
	cloned := *report
	if len(report.Buckets) > 0 {
		cloned.Buckets = make([]core.UsageBucket, len(report.Buckets))
		for i, bucket := range report.Buckets {
			cloned.Buckets[i] = bucket
			if len(bucket.Windows) > 0 {
				cloned.Buckets[i].Windows = append([]core.UsageWindow(nil), bucket.Windows...)
			}
		}
	}
	if report.Credits != nil {
		credits := *report.Credits
		cloned.Credits = &credits
	}
	return &cloned
}

func normalizeRuntimeReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "med":
		return "medium"
	case "x-high", "very-high":
		return "xhigh"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func appServerJSON(v any) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "{}" || s == "[]" || s == `""` {
		return ""
	}
	return s
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i), true
		}
	}
	return 0, false
}

func rpcIDToInt64(v any) (int64, bool) {
	switch id := v.(type) {
	case float64:
		return int64(id), true
	case int64:
		return id, true
	case int:
		return int64(id), true
	case json.Number:
		i, err := id.Int64()
		return i, err == nil
	}
	return 0, false
}

func (s *appServerSession) completeTurn(turnID string, turnErr error) {
	if turnErr == nil && s.brandCoreAwaitingPublication() {
		turnErr = fmt.Errorf("brand analysis ended before the core profile was accepted for persistence")
	}
	s.stateMu.Lock()
	if s.currentTurn == "" || (turnID != "" && turnID != s.currentTurn) {
		s.stateMu.Unlock()
		return
	}
	s.currentTurn = ""
	if turnErr != nil {
		s.pendingMsgs = s.pendingMsgs[:0]
	}
	s.stateMu.Unlock()
	if turnErr != nil {
		s.emitError(turnErr)
		return
	}
	s.flushPendingAsText()
	s.emit(core.Event{Type: core.EventResult, SessionID: s.CurrentSessionID(), Done: true})
}

// handleAgentMessageDelta relays live assistant prose to the user as it is
// generated. Streamed items are tracked so item/completed neither duplicates
// them nor demotes them to thinking when a tool call follows.
func (s *appServerSession) handleAgentMessageDelta(itemID, delta string) {
	if delta == "" {
		return
	}
	// The dedicated onboarding task promises a saved core, not prose. Keep its
	// completion claim private until the structured delivery acknowledgement;
	// ordinary conversations continue to stream without this business gate.
	if s.brandCoreAwaitingPublication() {
		return
	}
	// Deltas do not carry the final/commentary phase. Structured tasks wait
	// for item/completed so progress cannot contaminate the terminal JSON.
	if len(s.outputSchema()) > 0 {
		return
	}
	prefix := ""
	s.stateMu.Lock()
	if s.streamedItems == nil {
		s.streamedItems = make(map[string]string)
	}
	if _, seen := s.streamedItems[itemID]; !seen && s.lastStreamedItem != "" && s.lastStreamedItem != itemID {
		prefix = "\n\n"
	}
	s.streamedItems[itemID] += delta
	s.lastStreamedItem = itemID
	s.stateMu.Unlock()
	s.emit(core.Event{Type: core.EventText, Content: prefix + delta})
}

func (s *appServerSession) flushPendingAsThinking() {
	s.stateMu.Lock()
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventThinking, Content: text})
		}
	}
}

func (s *appServerSession) flushPendingAsText() {
	s.stateMu.Lock()
	msgs := append([]string(nil), s.pendingMsgs...)
	s.pendingMsgs = s.pendingMsgs[:0]
	s.stateMu.Unlock()

	for _, text := range msgs {
		if strings.TrimSpace(text) != "" {
			s.emit(core.Event{Type: core.EventText, Content: text})
		}
	}
}

func (s *appServerSession) emit(event core.Event) {
	select {
	case s.events <- event:
	default:
		slog.Warn("codex appserver: event channel full, dropping event", "type", event.Type)
	}
}

func (s *appServerSession) emitError(err error) {
	if err == nil {
		return
	}
	s.emit(core.Event{Type: core.EventError, Error: err})
}

func (s *appServerSession) rejectPending(err error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for id, ch := range s.pending {
		delete(s.pending, id)
		select {
		case ch <- rpcResponseEnvelope{ID: id, Error: &rpcError{Message: err.Error()}}:
		default:
		}
	}
}

func (s *appServerSession) request(method string, params any, out any) error {
	return s.requestWithTimeout(method, params, out, appServerRequestTimeout)
}

func (s *appServerSession) requestWithTimeout(method string, params any, out any, timeout time.Duration) error {
	id := s.nextID.Add(1)
	ch := make(chan rpcResponseEnvelope, 1)

	s.pendingMu.Lock()
	if s.pending == nil {
		s.pending = make(map[int64]chan rpcResponseEnvelope)
	}
	s.pending[id] = ch
	s.pendingMu.Unlock()

	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}

	if err := s.writeJSON(payload); err != nil {
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return err
	}

	select {
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("%s", strings.TrimSpace(resp.Error.Message))
		}
		if out != nil {
			if err := json.Unmarshal(resp.Result, out); err != nil {
				return fmt.Errorf("decode %s response: %w", method, err)
			}
		}
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-time.After(timeout):
		s.pendingMu.Lock()
		delete(s.pending, id)
		s.pendingMu.Unlock()
		return fmt.Errorf("%s timed out", method)
	}
}

func (s *appServerSession) notify(method string, params any) error {
	payload := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		payload["params"] = params
	}
	return s.writeJSON(payload)
}

func (s *appServerSession) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("codex app-server encode: %w", err)
	}

	s.procMu.Lock()
	stdin := s.stdin
	s.procMu.Unlock()
	if stdin == nil {
		return fmt.Errorf("codex app-server connection is closed")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := stdin.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("codex app-server write: %w", err)
	}
	return nil
}
