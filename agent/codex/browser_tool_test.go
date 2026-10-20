package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestBrowserCallsCannotOutliveOrMoveBetweenTurns(t *testing.T) {
	s := &appServerSession{currentTurn: "turn-one"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := &browserToolCall{cancel: cancel}
	if s.trackBrowserCall(call, "old-turn") || s.trackBrowserCall(call, "") || !s.trackBrowserCall(call, "turn-one") {
		t.Fatal("tool call was not bound to its originating turn")
	}
	s.stateMu.Lock()
	s.interruptedTurn = "turn-one"
	s.cancelBrowserCallsLocked("turn-one")
	s.stateMu.Unlock()
	if ctx.Err() == nil || len(s.browserCalls) != 0 || s.trackBrowserCall(call, "turn-one") {
		t.Fatal("an interrupted turn retained browser execution")
	}
	s.currentTurn = "turn-two"
	if s.trackBrowserCall(call, "turn-one") {
		t.Fatal("a delayed old call entered the next turn")
	}
}

func TestBrowserCurrentPageBindingReachesTheScopedHelper(t *testing.T) {
	for _, current := range []bool{true, false} {
		operation, input, err := browserToolRequest(map[string]any{"operation": "open", "currentPage": current})
		if err != nil || operation != "open" || input["currentPage"] != current || input["tab"] != nil {
			t.Fatalf("message-page binding changed: %s %#v %v", operation, input, err)
		}
	}
}

func browserSession(t *testing.T, helper string) *appServerSession {
	t.Helper()
	s := teamToolSession(t, "")
	dir := strings.TrimPrefix(s.extraEnv[0], "SKILLS_OL_DIR=")
	if err := os.WriteFile(filepath.Join(dir, "browser-tool.mjs"), []byte(helper), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBrowserToolUsesFrozenTurnAuthorityAndNoShellInterpolation(t *testing.T) {
	s := browserSession(t, `import {readFileSync} from 'node:fs';
let input='';for await(const part of process.stdin)input+=part;
const authority=readFileSync(process.env.TOMAKO_TASK_ENV_FILE,'utf8');
console.log(JSON.stringify({argv:process.argv.slice(2),input:JSON.parse(input),
  original:authority.includes("TOMAKO_TASK_ID='cmsg-cmo'"),
  privateAuthority:authority.includes("`+testCmoToken+`"),
  unsafeEnvironment:!!process.env.NODE_OPTIONS, inheritedSecret:!!process.env.PROVIDER_SECRET}));`)
	t.Setenv("NODE_OPTIONS", "--invalid-inherited-option")
	t.Setenv("PROVIDER_SECRET", "must-not-be-inherited")
	text := "draft ' $(touch /tmp/never-run-browser-test) `not a command`\n第二行"
	args := map[string]any{"operation": "act", "action": "fill", "text": text}
	call, err := s.prepareBrowserCall(args)
	if err != nil {
		t.Fatal(err)
	}
	s.runtime = core.SessionRuntime{TaskID: "cmsg-next", WorkspaceID: "other", BrandID: "other", ImageCapabilityToken: "next-token"}
	items, success := call.run()
	if !success {
		t.Fatalf("call failed: %#v", items)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(items[0]["text"].(string)), &got); err != nil {
		t.Fatal(err)
	}
	if got["original"] != true || got["privateAuthority"] != true || got["unsafeEnvironment"] != false || got["inheritedSecret"] != false {
		t.Fatalf("authority/environment changed: %#v", got)
	}
	input := got["input"].(map[string]any)
	if input["text"] != text || input["requestId"] == "" || input["operation"] != nil {
		t.Fatalf("input changed: %#v", input)
	}
	if _, err := os.Stat(call.directory); !os.IsNotExist(err) {
		t.Fatalf("private call directory not removed: %v", err)
	}
	for _, key := range []string{"workspaceId", "token", "env", "script", "urlOverride"} {
		if _, _, err := browserToolRequest(map[string]any{"operation": "read", key: "injected"}); err == nil {
			t.Fatalf("%s accepted", key)
		}
	}
}

func TestBrowserScreenshotIsAnImageInTheSameToolResultAndTemporaryFilesAreRemoved(t *testing.T) {
	s := browserSession(t, `import {writeFileSync} from 'node:fs';import {join} from 'node:path';
const path=join(process.env.TMPDIR,'screenshot.png');
writeFileSync(path,Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/l9sAAAAASUVORK5CYII=','base64'));
console.log(JSON.stringify({screenshot:{imagePath:path,mimeType:'image/png',stable:true,screenshotId:'observed',viewport:{width:1,height:1}}}));`)
	call, err := s.prepareBrowserCall(map[string]any{"operation": "screenshot", "sessionId": "existing"})
	if err != nil {
		t.Fatal(err)
	}
	items, success := call.run()
	if !success || len(items) != 2 || items[1]["type"] != "inputImage" {
		t.Fatalf("pixels absent: %#v", items)
	}
	if !strings.HasPrefix(items[1]["imageUrl"].(string), "data:image/png;base64,") {
		t.Fatal("wrong image protocol")
	}
	if strings.Contains(items[0]["text"].(string), "imagePath") || strings.Contains(items[0]["text"].(string), "view_image") {
		t.Fatal("bridge path exposed")
	}
	if _, err := os.Stat(call.directory); !os.IsNotExist(err) {
		t.Fatal("screenshot not removed")
	}
}

func TestBrowserFailuresKeepTheExecutionReceiptAndNeverExposeAuthority(t *testing.T) {
	s := browserSession(t, `console.log(JSON.stringify({ok:false,error:'`+testCmoToken+` rejected',code:'TAB_CHANGED',outcomeUnknown:false}));process.exitCode=1;`)
	call, err := s.prepareBrowserCall(map[string]any{"operation": "open", "tab": map[string]any{"tabId": 1, "url": "https://example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	items, success := call.run()
	text := items[0]["text"].(string)
	if success || strings.Contains(text, testCmoToken) || !strings.Contains(text, "TAB_CHANGED") || !strings.Contains(text, `"outcomeUnknown":false`) {
		t.Fatalf("failure lost/redaction failed: %s", text)
	}
}

func TestBrowserToolRegistrationAndResumedConversationFallback(t *testing.T) {
	s := browserSession(t, "// installed")
	if !dynamicToolsInclude(s.threadRequestParams(), browserToolName) {
		t.Fatal("browser tool missing")
	}
	s.developerInstructionsManaged = true
	s.runtime.DeveloperInstructions = "Current policy"
	before := appServerJSON(s.turnDeveloperInstructions())
	if !strings.Contains(before, "This older conversation may not have") {
		t.Fatal("older conversation cannot reach current capability")
	}
	s.threadBrowserTool.Store(true)
	after := appServerJSON(s.turnDeveloperInstructions())
	if strings.Contains(after, "This older conversation may not have") || !strings.Contains(after, "operation=tabs") {
		t.Fatal("native tool guidance incorrect")
	}
	s.runtime.ImageCapabilityToken = ""
	if s.browserToolAvailable() {
		t.Fatal("tool offered without authority")
	}
	if _, err := s.prepareBrowserCall(map[string]any{"operation": "tabs"}); err == nil {
		t.Fatal("old tool could execute after authority revoked")
	}
	s.runtime = cmoRuntime()
	s.runtime.Scene = "brand_analysis"
	if s.browserToolAvailable() {
		t.Fatal("dedicated workflow changed")
	}
}

func TestBrowserToolScopeAndInputShape(t *testing.T) {
	s := browserSession(t, "// installed")
	s.runtime.ChatSessionID = ""
	if s.browserToolAvailable() {
		t.Fatal("non-conversation runtime gained a new tool")
	}
	for _, args := range []map[string]any{
		{"operation": "evaluate"},
		{"operation": "act", "script": "alert(1)"},
		{"operation": "act", "action": "fill", "text": strings.Repeat("x", 9000)},
		{"operation": "read", "find": 12},
	} {
		if _, _, err := browserToolRequest(args); err == nil {
			t.Fatalf("invalid input accepted: %#v", args)
		}
	}
}

func TestBrowserCompactPageRemainsReadableAndReceiptPreservesWarnings(t *testing.T) {
	s := browserSession(t, `console.log(JSON.stringify({ok:true,sessionId:'current',
result:{valueUnverified:'element replaced; read to verify'},pageText:'[full-ref] button "Continue"\n正文：ready'}));`)
	call, err := s.prepareBrowserCall(map[string]any{"operation": "read", "find": "Continue"})
	if err != nil {
		t.Fatal(err)
	}
	items, success := call.run()
	if !success || len(items) != 2 || !strings.Contains(items[0]["text"].(string), "valueUnverified") ||
		items[1]["text"] != "[full-ref] button \"Continue\"\n正文：ready" {
		t.Fatalf("compact page or execution warning lost: %#v", items)
	}
}

func TestBrowserControlAndTabOperationsKeepStructuredIntent(t *testing.T) {
	for _, args := range []map[string]any{
		{"operation": "control", "sessionId": "lease", "human": false},
		{"operation": "tab", "sessionId": "lease", "action": "select", "targetId": "oauth"},
	} {
		op, input, err := browserToolRequest(args)
		if err != nil || op != args["operation"] || input["requestId"] == nil || input["sessionId"] != "lease" {
			t.Fatalf("handoff contract lost: %s %#v %v", op, input, err)
		}
	}
}
