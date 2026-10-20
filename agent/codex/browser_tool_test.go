package codex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

const browserToolSecret = "browser-image-capability-secret"

func browserToolSession(t *testing.T, helper string, runtime core.SessionRuntime) *appServerSession {
	t.Helper()
	dir := t.TempDir()
	if helper != "" {
		if err := os.WriteFile(filepath.Join(dir, "browser-tool.mjs"), []byte(helper), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &appServerSession{ctx: ctx, cancel: cancel, workDir: t.TempDir(), extraEnv: []string{"SKILLS_OL_DIR=" + dir},
		events: make(chan core.Event, 8), runtime: runtime}
	s.alive.Store(true)
	return s
}

func browserRuntime() core.SessionRuntime {
	return core.SessionRuntime{TaskID: "cmsg-browser", WorkspaceID: "ws-a", BrandID: "prod-a", ChatSessionID: "csess-browser",
		ImageCapabilityToken: browserToolSecret}
}

func TestBrowserToolOfferedInConversationsWithBrowserAuthority(t *testing.T) {
	if !browserToolSession(t, "// helper", browserRuntime()).browserToolAvailable() {
		t.Fatal("a conversation turn with authority and the helper installed gets the tool")
	}
	noConversation := browserRuntime()
	noConversation.ChatSessionID = ""
	noToken := browserRuntime()
	noToken.ImageCapabilityToken = ""
	for name, s := range map[string]*appServerSession{
		"no conversation": browserToolSession(t, "// helper", noConversation),
		"no authority":    browserToolSession(t, "// helper", noToken),
		"no helper":       browserToolSession(t, "", browserRuntime()),
	} {
		if s.browserToolAvailable() {
			t.Fatalf("%s: the tool must not be offered", name)
		}
	}
}

func TestBrowserToolInputKeepsTheSchemaShape(t *testing.T) {
	if _, err := browserToolInput(map[string]any{"action": "evaluate"}); err == nil {
		t.Fatal("an unknown action is refused")
	}
	if _, err := browserToolInput(map[string]any{"action": "click", "ref": "4", "script": "alert(1)"}); err == nil {
		t.Fatal("an unknown field is refused before Node starts")
	}
	if _, err := browserToolInput(map[string]any{"action": "fill", "ref": "4", "text": strings.Repeat("x", 9000)}); err == nil {
		t.Fatal("an oversized value is refused")
	}
	body, err := browserToolInput(map[string]any{"action": "open", "tab": "1853163903", "url": nil})
	if err != nil || string(body) != `{"action":"open","tab":"1853163903"}` {
		t.Fatalf("got %s, %v", body, err)
	}
}

func TestBrowserToolRunsTheHelperWithTheTurnsAuthorityAndAttachesScreenshots(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	image := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(image, []byte("\x89PNG\r\n\x1a\nfake"), 0o600); err != nil {
		t.Fatal(err)
	}
	helper := `import {readFileSync} from 'node:fs';
let stdin = '';
for await (const chunk of process.stdin) stdin += chunk;
const file = readFileSync(process.env.TOMAKO_TASK_ENV_FILE, 'utf8');
console.log(JSON.stringify({ok: true, text: 'got ' + stdin + ' authority=' + file.includes('` + browserToolSecret + `') + ' token=` + browserToolSecret + `',
  image: {path: ` + "`" + image + "`" + `, mime: 'image/png'}}));`
	s := browserToolSession(t, helper, browserRuntime())
	call, err := s.prepareBrowserTool(map[string]any{"action": "screenshot"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := call.run()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, `got {"action":"screenshot"} authority=true`) || strings.Contains(result.Text, browserToolSecret) {
		t.Fatalf("helper text: %q", result.Text)
	}
	items := browserToolContent(result)
	if len(items) != 2 || items[1]["type"] != "inputImage" || !strings.HasPrefix(items[1]["imageUrl"].(string), "data:image/png;base64,") {
		t.Fatalf("screenshot is attached inline: %#v", items)
	}
}

func TestBrowserToolReportsTheHelpersReasonWithoutCredentials(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	helper := `console.log(JSON.stringify({ok: false, error: 'this conversation has no open page; open first ` + browserToolSecret + `'})); process.exitCode = 1;`
	call, err := browserToolSession(t, helper, browserRuntime()).prepareBrowserTool(map[string]any{"action": "read"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = call.run()
	if err == nil || !strings.Contains(err.Error(), "open first") || strings.Contains(err.Error(), browserToolSecret) {
		t.Fatalf("got %v", err)
	}
}

func TestBrowserCurrentPageBindingReachesTheScopedHelper(t *testing.T) {
	for _, current := range []bool{true, false} {
		body, err := browserToolInput(map[string]any{"action": "open", "currentPage": current})
		if err != nil || !strings.Contains(string(body), `"currentPage":`) {
			t.Fatalf("binding was dropped: %s %v", body, err)
		}
	}
}
