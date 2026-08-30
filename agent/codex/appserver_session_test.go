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

func TestThreadParamsIncludeTaskScopedNativeWebSearch(t *testing.T) {
	s := &appServerSession{
		workDir:   "/srv/tomako/workspaces/brand-42",
		model:     "tomako/deepseek-v4-flash-search",
		webSearch: "live",
	}

	params := s.threadRequestParams()
	config, ok := params["config"].(map[string]any)
	if !ok || config["web_search"] != "live" {
		t.Fatalf("thread params missing task-scoped native web search: %#v", params)
	}
	if config["features.default_mode_request_user_input"] != true {
		t.Fatalf("thread params missing Default mode request_user_input feature: %#v", params)
	}
}

func TestThreadParamsEnableDefaultModeRequestUserInputWithoutWebSearch(t *testing.T) {
	s := &appServerSession{workDir: "/srv/tomako"}

	params := s.threadRequestParams()
	config, ok := params["config"].(map[string]any)
	if !ok || config["features.default_mode_request_user_input"] != true {
		t.Fatalf("thread params missing Default mode request_user_input feature: %#v", params)
	}
	if _, ok := config["web_search"]; ok {
		t.Fatalf("thread params unexpectedly enabled web search: %#v", params)
	}
}

func TestBrandAnalysisRuntimeRegistersOnlyDedicatedDynamicTools(t *testing.T) {
	s := &appServerSession{workDir: "/srv/tomako"}
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}

	params := s.threadRequestParams()
	tools, ok := params["dynamicTools"].([]map[string]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("dynamic tools = %#v", params["dynamicTools"])
	}
	if tools[0]["name"] != "collect_brand_evidence" || tools[1]["name"] != "publish_brand_analysis_stage" {
		t.Fatalf("unexpected brand tools: %#v", tools)
	}
}

func TestBrandAnalysisRuntimeIsAppliedBeforeDeferredThreadCreation(t *testing.T) {
	s := &appServerSession{workDir: "/srv/tomako"}
	s.alive.Store(true)
	if err := s.SetSessionRuntime(core.SessionRuntime{
		Scene:           "brand_analysis",
		GatewayModel:    "tomako/deepseek-v4-flash",
		WebSearch:       "live",
		ReasoningEffort: "low",
	}); err != nil {
		t.Fatalf("SetSessionRuntime: %v", err)
	}

	params := s.threadRequestParams()
	if params["model"] != "tomako/deepseek-v4-flash" {
		t.Fatalf("thread model = %#v", params["model"])
	}
	config, _ := params["config"].(map[string]any)
	if config["web_search"] != "live" {
		t.Fatalf("thread config = %#v", params["config"])
	}
	tools, _ := params["dynamicTools"].([]map[string]any)
	if len(tools) != 2 {
		t.Fatalf("dynamic tools = %#v", params["dynamicTools"])
	}
}

func TestSessionRuntimeReplacesThreadAcrossBrandToolBoundary(t *testing.T) {
	s := &appServerSession{}
	s.alive.Store(true)
	s.threadID.Store("thread-without-brand-tools")

	if err := s.SetSessionRuntime(core.SessionRuntime{Scene: "brand_analysis"}); err != nil {
		t.Fatalf("enter brand runtime: %v", err)
	}
	if got := s.CurrentSessionID(); got != "" {
		t.Fatalf("thread without brand tools was reused: %q", got)
	}

	s.threadID.Store("thread-with-brand-tools")
	s.threadBrandTools = true
	if err := s.SetSessionRuntime(core.SessionRuntime{Scene: "studio_chat"}); err != nil {
		t.Fatalf("leave brand runtime: %v", err)
	}
	if got := s.CurrentSessionID(); got != "" {
		t.Fatalf("brand-tool thread leaked into a non-brand turn: %q", got)
	}
}

func TestBrandAnalysisRuntimeResetsPerTaskFlowOnReusedBrandThread(t *testing.T) {
	s := &appServerSession{}
	s.alive.Store(true)
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}
	s.threadID.Store("warm-brand-thread")
	s.threadBrandTools = true
	s.brandFlow = brandAnalysisFlow{
		evidenceReady:        true,
		corePublished:        true,
		searchAttempts:       1,
		searchCompleted:      true,
		competitorsPublished: true,
	}

	if err := s.SetSessionRuntime(core.SessionRuntime{Scene: "brand_analysis"}); err != nil {
		t.Fatalf("reuse brand runtime: %v", err)
	}
	if got := s.CurrentSessionID(); got != "warm-brand-thread" {
		t.Fatalf("warm brand thread should be reused, got %q", got)
	}
	if s.brandFlow != (brandAnalysisFlow{}) {
		t.Fatalf("brand flow leaked from previous task: %#v", s.brandFlow)
	}
}

func TestBrandAnalysisFlowEnforcesEvidenceCoreSearchCompetitorOrder(t *testing.T) {
	s := &appServerSession{}
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}

	if err := s.advanceBrandAnalysisStage("core", map[string]any{}); err == nil {
		t.Fatal("core published before evidence")
	}
	if err := s.beginBrandEvidenceCollection(); err != nil {
		t.Fatalf("begin evidence: %v", err)
	}
	if err := s.beginBrandEvidenceCollection(); err == nil {
		t.Fatal("duplicate evidence collection accepted")
	}
	s.finishBrandEvidenceCollection(true)
	if err := s.beginBrandEvidenceCollection(); err == nil {
		t.Fatal("second successful evidence collection accepted")
	}
	if err := s.advanceBrandAnalysisStage("core", map[string]any{}); err != nil {
		t.Fatalf("publish core: %v", err)
	}
	if err := s.advanceBrandAnalysisStage("core", map[string]any{}); err == nil {
		t.Fatal("duplicate core publication accepted")
	}
	readyCompetitors := map[string]any{"status": "complete", "competitors": []any{map[string]any{"name": "Canva"}}}
	if err := s.advanceBrandAnalysisStage("competitors", readyCompetitors); err == nil {
		t.Fatal("ready competitors published before native web search")
	}
	s.noteBrandWebSearchStarted("search-1")
	if err := s.advanceBrandAnalysisStage("competitors", readyCompetitors); err == nil {
		t.Fatal("ready competitors published while native web search was running")
	}
	s.noteBrandWebSearchCompleted("search-1")
	if err := s.advanceBrandAnalysisStage("competitors", readyCompetitors); err != nil {
		t.Fatalf("publish competitors: %v", err)
	}
	if err := s.advanceBrandAnalysisStage("competitors", readyCompetitors); err == nil {
		t.Fatal("duplicate competitor publication accepted")
	}
}

func TestBrandAnalysisFlowCanCloseUnavailableSearchAfterCore(t *testing.T) {
	s := &appServerSession{}
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}
	if err := s.beginBrandEvidenceCollection(); err != nil {
		t.Fatalf("begin evidence: %v", err)
	}
	s.finishBrandEvidenceCollection(true)
	if err := s.advanceBrandAnalysisStage("core", map[string]any{}); err != nil {
		t.Fatalf("publish core: %v", err)
	}
	if err := s.advanceBrandAnalysisStage("competitors", map[string]any{
		"status": "unavailable", "competitors": []any{},
	}); err != nil {
		t.Fatalf("close unavailable competitors: %v", err)
	}
}

func TestBrandAnalysisFlowRejectsMultipleNativeSearchAttempts(t *testing.T) {
	s := &appServerSession{}
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}
	if err := s.beginBrandEvidenceCollection(); err != nil {
		t.Fatalf("begin evidence: %v", err)
	}
	s.finishBrandEvidenceCollection(true)
	if err := s.advanceBrandAnalysisStage("core", map[string]any{}); err != nil {
		t.Fatalf("publish core: %v", err)
	}
	s.noteBrandWebSearchStarted("search-1")
	s.noteBrandWebSearchCompleted("search-1")
	s.noteBrandWebSearchStarted("search-2")
	if err := s.advanceBrandAnalysisStage("competitors", map[string]any{
		"status": "complete", "competitors": []any{map[string]any{"name": "Canva"}},
	}); err == nil {
		t.Fatal("competitors published after multiple search attempts")
	}
}

func TestBrandAnalysisFlowRejectsCoreAfterEarlyNativeSearch(t *testing.T) {
	s := &appServerSession{}
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}
	if err := s.beginBrandEvidenceCollection(); err != nil {
		t.Fatalf("begin evidence: %v", err)
	}
	s.finishBrandEvidenceCollection(true)
	s.noteBrandWebSearchStarted("search-too-early")
	if err := s.advanceBrandAnalysisStage("core", map[string]any{}); err == nil {
		t.Fatal("core published after an early native web search")
	}
}

func TestBrandStructuredResultWaitsForQueueCapacityAndDeliveryAck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, events: make(chan core.Event, 1)}
	s.events <- core.Event{Type: core.EventThinking}

	done := make(chan error, 1)
	go func() {
		done <- s.publishStructuredResult("core", map[string]any{"productType": "SaaS"})
	}()
	select {
	case err := <-done:
		t.Fatalf("publish returned before queue capacity was available: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	<-s.events
	var event core.Event
	select {
	case event = <-s.events:
	case <-time.After(time.Second):
		t.Fatal("structured result was not enqueued after capacity became available")
	}
	if event.Type != core.EventStructuredResult || event.DeliveryAck == nil {
		t.Fatalf("structured event = %#v", event)
	}
	event.DeliveryAck <- nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publish structured result: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("publish did not finish after delivery acknowledgement")
	}
}

func TestBrandStructuredResultReturnsDeliveryFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &appServerSession{ctx: ctx, events: make(chan core.Event, 1)}
	done := make(chan error, 1)
	go func() {
		done <- s.publishStructuredResult("core", map[string]any{"productType": "SaaS"})
	}()
	event := <-s.events
	event.DeliveryAck <- io.ErrClosedPipe
	if err := <-done; err == nil || !strings.Contains(err.Error(), "delivery failed") {
		t.Fatalf("publish error = %v, want delivery failure", err)
	}
}

func TestBrandStageCanRetryAfterDeliveryFailure(t *testing.T) {
	s := &appServerSession{}
	s.runtime = core.SessionRuntime{Scene: "brand_analysis"}
	if err := s.beginBrandEvidenceCollection(); err != nil {
		t.Fatalf("begin evidence: %v", err)
	}
	s.finishBrandEvidenceCollection(true)
	if err := s.beginBrandAnalysisStagePublication("core", map[string]any{}); err != nil {
		t.Fatalf("begin core publication: %v", err)
	}
	s.finishBrandAnalysisStagePublication("core", false)
	if err := s.beginBrandAnalysisStagePublication("core", map[string]any{}); err != nil {
		t.Fatalf("retry core publication: %v", err)
	}
	s.finishBrandAnalysisStagePublication("core", true)
	if err := s.beginBrandAnalysisStagePublication("core", map[string]any{}); err == nil {
		t.Fatal("published core accepted another retry")
	}
}

func TestBrandEvidenceForModelIsBoundedAndExcludesVisualAssets(t *testing.T) {
	long := strings.Repeat("product evidence ", 200)
	pages := make([]any, 0, 12)
	for index := 0; index < 12; index++ {
		pages = append(pages, map[string]any{
			"url":      "https://example.com/product",
			"title":    long,
			"headings": []any{long, long, long, long, long, long, long, long, long},
			"snippets": []any{long, long, long, long, long, long, long, long, long},
		})
	}
	compact := brandEvidenceForModel(map[string]any{
		"brandName":     "Example",
		"evidencePages": pages,
		"logoUrl":       "https://example.com/logo.svg",
		"brandColors":   []any{"#ff0000"},
		"jsonLd": []any{map[string]any{
			"@type": "SoftwareApplication", "name": "Example", "unused": long,
		}},
	})
	encoded, err := json.Marshal(compact)
	if err != nil {
		t.Fatalf("marshal compact evidence: %v", err)
	}
	if len(encoded) > 30_000 {
		t.Fatalf("compact evidence too large: %d bytes", len(encoded))
	}
	if _, exists := compact["logoUrl"]; exists {
		t.Fatalf("visual asset leaked into model evidence: %#v", compact)
	}
	compactPages, _ := compact["evidencePages"].([]any)
	if len(compactPages) != 6 {
		t.Fatalf("evidence pages = %d, want 6", len(compactPages))
	}
}

func TestValidateBrandAnalysisStageKeepsOnlyCoreSemanticFields(t *testing.T) {
	stage, result, err := validateBrandAnalysisStage(map[string]any{
		"stage": "core",
		"result": map[string]any{
			"productType": "SaaS",
			"platforms":   []any{"Web"},
			"audience":    "Marketing teams",
			"keyFeatures": []any{"Generate", "Edit", "Review"},
			"logoUrl":     "https://attacker.invalid/logo.svg",
		},
	})
	if err != nil || stage != "core" {
		t.Fatalf("validate stage: stage=%q err=%v", stage, err)
	}
	if _, ok := result["logoUrl"]; ok {
		t.Fatalf("model must not replace deterministic logo: %#v", result)
	}
}

func TestNormalizeCompetitorCandidatesChecksURLsWithoutVisitingThem(t *testing.T) {
	items := normalizeCompetitorCandidates([]any{
		map[string]any{"name": "Canva", "websiteUrl": "http://www.canva.com/design?utm_source=x#hero", "confidence": "high"},
		map[string]any{"name": "Local", "websiteUrl": "http://127.0.0.1:8080/"},
		map[string]any{"name": "Canva duplicate", "websiteUrl": "https://www.canva.com/other"},
	})
	if len(items) != 1 {
		t.Fatalf("normalized candidates = %#v", items)
	}
	got := items[0].(map[string]any)
	if got["websiteUrl"] != "https://www.canva.com/" || got["confidence"] != "low" {
		t.Fatalf("normalized candidate = %#v", got)
	}
}

func TestBrandAnalysisRejectsPrivateHostsAndInvalidCoreEnums(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "10.0.0.8", "service.internal"} {
		if isPublicHostname(host) {
			t.Fatalf("private host accepted: %s", host)
		}
	}
	if !isPublicHostname("tomako.ai") {
		t.Fatal("public hostname rejected")
	}
	_, _, err := validateBrandAnalysisStage(map[string]any{
		"stage": "core",
		"result": map[string]any{
			"productType": "website",
			"platforms":   []any{"Web"},
			"audience":    "Teams",
			"keyFeatures": []any{"One", "Two", "Three"},
		},
	})
	if err == nil {
		t.Fatal("invalid product type accepted")
	}
}

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

func TestAppServerSession_SessionRuntimeSelectsTurnModelAndMetadata(t *testing.T) {
	s := &appServerSession{}
	s.alive.Store(true)
	err := s.SetSessionRuntime(core.SessionRuntime{
		Scene:              "brand_analysis",
		LogicalModel:       "VISION_BALANCED_V1",
		GatewayModel:       "tomako/vision-balanced-v1",
		WebSearch:          "live",
		RoutePolicyID:      "rp-vision-balanced",
		RoutePolicyVersion: 7,
		InferenceRequestID: "infer-123",
		RequiredModalities: []string{"TEXT", "IMAGE"},
		WorkspaceID:        "ws-1",
		BrandID:            "brand-1",
		UserID:             "42",
		ChatSessionID:      "csess-1",
		TaskID:             "cmsg-1",
		ReasoningEffort:    "low",
		TurnNo:             2,
	})
	if err != nil {
		t.Fatalf("SetSessionRuntime: %v", err)
	}
	if got := s.GetModel(); got != "tomako/vision-balanced-v1" {
		t.Fatalf("model = %q", got)
	}
	if got := s.getWebSearch(); got != "live" {
		t.Fatalf("web search = %q", got)
	}
	if got := s.GetReasoningEffort(); got != "low" {
		t.Fatalf("reasoning effort = %q", got)
	}
	metadata := s.responsesAPIClientMetadata()
	if metadata["tomako.inference_request_id"] != "infer-123" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata["tomako.required_modalities"] != "TEXT,IMAGE" {
		t.Fatalf("required modalities = %q", metadata["tomako.required_modalities"])
	}
	if metadata["tomako.user_id"] != "42" {
		t.Fatalf("user metadata = %#v", metadata)
	}
	if _, ok := metadata["tomako.gateway_model"]; ok {
		t.Fatalf("gateway model must travel as turn/start.model, not metadata: %#v", metadata)
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
		"config": map[string]any{
			"features.default_mode_request_user_input": true,
		},
		"permissions":    "tomako-brand-fence",
		"approvalPolicy": "never",
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

func TestAppServerSession_HandleRequestUserInputPreservesIDKeyedMultiSelectAnswers(t *testing.T) {
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
				"database": "Postgres, SQLite",
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
	if len(got) != 1 || got[0] != "Postgres, SQLite" {
		t.Fatalf("answers[database] = %#v, want [Postgres, SQLite]", got)
	}
}

func TestAppServerRequestUserInputResponseRetainsLegacyQuestionTextAnswers(t *testing.T) {
	response := appServerRequestUserInputResponseFromResult(
		[]appServerRequestUserInputQuestion{{
			ID:       "database",
			Question: "Which database should we use?",
		}},
		core.PermissionResult{
			Behavior: "allow",
			UpdatedInput: map[string]any{
				"answers": map[string]any{
					"Which database should we use?": "Postgres",
				},
			},
		},
	)

	got := response.Answers["database"].Answers
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
