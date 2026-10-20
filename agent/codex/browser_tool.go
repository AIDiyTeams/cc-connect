package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// The browser tool is the conversation's way to use a browser, one typed call per
// step. Before it the Agent drove the browser through a shell command: it read a
// long usage note first, called status and close separately, invented request ids,
// and piped page JSON that the shell truncated through jq or node to find a button,
// so a two-click task took 17 model rounds. The Skills-OL helper keeps the same
// Portal capabilities and adds what a conversation should not have to do itself;
// the turn's authority reaches it through the task env file, never through model
// arguments. The command line stays for scripts and unattended runs.
const browserToolName = "tomako_browser"

var browserToolTimeout = 120 * time.Second

const (
	browserToolOutputLimit = 64 * 1024
	browserToolImageLimit  = 6 * 1024 * 1024
)

var browserToolActions = []string{"tabs", "open", "read", "click", "fill", "select", "check", "press", "scroll", "media",
	"screenshot", "click-coordinate", "drag", "navigate", "close"}

// Fields the helper accepts; anything else is refused here before Node starts.
var browserToolFields = map[string]bool{
	"action": true, "path": true, "tab": true, "url": true, "ref": true, "text": true, "option": true, "checked": true,
	"key": true, "direction": true, "time": true, "x": true, "y": true, "toX": true, "toY": true, "screenshotId": true,
	"currentPage": true, "find": true, "newTab": true, "closeTab": true, "taskName": true, "includeOffscreenFrames": true,
}

func browserDynamicTool() map[string]any {
	str := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	num := func(description string) map[string]any {
		return map[string]any{"type": "number", "description": description}
	}
	return map[string]any{"type": "function", "name": browserToolName, "deferLoading": false,
		"description": "When this message has browserMessagePage, current page is its frozen sending tab: open with currentPage=true, omitting tab/url/newTab. Never infer it from recent tabs. If closed, changed or unavailable, stop and explain; never choose another page or browser. Set currentPage=false only when the user explicitly requests another page or a new tab. Use a browser for the user, one step per call. path local-chrome is the user's own Chrome (their logins and the pages they " +
			"have open; they see every step), cloud is the product's cloud browser. tabs lists the pages open in the user's Chrome (title, address, " +
			"which one they are looking at, when each was last used). open with tab takes over a page the user already has open exactly as it is: " +
			"nothing reloads, so their login, scroll position and anything they already typed stay. open with url opens a new tab. Pages come back " +
			"as numbered controls (`[42] button 「Delete」`) followed by the page text; act with the number as ref. click, fill, select, check, press " +
			"and scroll answer with what changed on the page and the page after it, so a separate read is only needed for a part you have not seen; " +
			"use find to narrow a long page to the controls and lines containing a word. screenshot returns the picture. Control ends by itself when " +
			"this turn ends: pages you took over stay open as the user left them; tabs you opened stay unless you close them with closeTab. " +
			"Paths, costs and site rules are in platform-browser.md when you need them.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"action"},
			"properties": map[string]any{
				"action":                 map[string]any{"type": "string", "enum": browserToolActions},
				"currentPage":            map[string]any{"type": "boolean", "description": "open: use the sending tab saved with this message. False only when the user explicitly requests another page."},
				"path":                   map[string]any{"type": "string", "enum": []string{"local-chrome", "cloud"}, "description": "open only. Omit to continue on the conversation's current path."},
				"tab":                    str("open only: a tab id from tabs to take over."),
				"url":                    str("open (new tab) or navigate: an https address."),
				"ref":                    str("The number of a control from the latest page, e.g. \"42\" or \"3@1\"."),
				"text":                   str("fill: the text to enter."),
				"option":                 str("select: the option text or value. media: pause, seek or transcript."),
				"checked":                map[string]any{"type": "boolean", "description": "check: true to tick, false to clear."},
				"key":                    map[string]any{"type": "string", "enum": []string{"Enter", "Tab", "Escape", "Backspace", "ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight"}},
				"direction":              map[string]any{"type": "string", "enum": []string{"up", "down"}},
				"time":                   num("media seek: seconds."),
				"x":                      num("click-coordinate / drag start, in screenshot viewport pixels."),
				"y":                      num("click-coordinate / drag start."),
				"toX":                    num("drag end."),
				"toY":                    num("drag end."),
				"screenshotId":           str("click-coordinate / drag: the screenshot the coordinates come from."),
				"find":                   str("Show only controls and text lines containing this word."),
				"newTab":                 map[string]any{"type": "boolean", "description": "open: a fresh tab even if the conversation has one."},
				"closeTab":               map[string]any{"type": "boolean", "description": "close: also close the tab Tomako opened."},
				"taskName":               str("open: a short name for the tab group of tabs Tomako opens."),
				"includeOffscreenFrames": map[string]any{"type": "boolean", "description": "read: also read frames outside the viewport."},
			}},
	}
}

func (s *appServerSession) browserToolScript() string {
	return s.webReaderScript("browser-tool.mjs")
}

// Conversations a person is in get the tool; dedicated workflows and unattended
// runs keep the tools and command line they were designed with.
func (s *appServerSession) browserToolAvailable() bool {
	if s.isBrandAnalysisRuntime() || s.isUserVoiceArchiveRuntime() || s.isUserVoiceJudgmentRuntime() || s.unattended() {
		return false
	}
	s.runtimeMu.RLock()
	runtime := s.runtime
	s.runtimeMu.RUnlock()
	return browserToolAuthorized(runtime) && s.browserToolScript() != ""
}

func browserToolAuthorized(runtime core.SessionRuntime) bool {
	token := strings.TrimSpace(runtime.MachineCapabilityToken)
	if token == "" {
		token = strings.TrimSpace(runtime.ImageCapabilityToken)
	}
	return strings.TrimSpace(runtime.ChatSessionID) != "" && runtime.TaskID != "" && token != ""
}

// browserToolInput checks the shape the schema promises; the helper validates the rest.
func browserToolInput(arguments map[string]any) ([]byte, error) {
	action, _ := arguments["action"].(string)
	known := false
	for _, candidate := range browserToolActions {
		known = known || candidate == action
	}
	if !known {
		return nil, fmt.Errorf("action must be one of %s", strings.Join(browserToolActions, ", "))
	}
	clean := map[string]any{}
	for key, value := range arguments {
		if !browserToolFields[key] {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		if value == nil {
			continue
		}
		if text, ok := value.(string); ok && len(text) > 8000 {
			return nil, fmt.Errorf("%s is too long", key)
		}
		clean[key] = value
	}
	return json.Marshal(clean)
}

type browserToolCall struct {
	cmd     *exec.Cmd
	ctx     context.Context
	cancel  context.CancelFunc
	cleanup func()
	secrets []string
}

func (s *appServerSession) prepareBrowserTool(arguments map[string]any) (*browserToolCall, error) {
	unavailable := errors.New("the browser tool is not available for this turn; say so instead of retrying")
	if !s.browserToolAvailable() {
		return nil, unavailable
	}
	stdin, err := browserToolInput(arguments)
	if err != nil {
		return nil, err
	}
	script := s.browserToolScript()
	s.runtimeMu.RLock()
	runtime := s.runtime
	s.runtimeMu.RUnlock()
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil || path == "" {
		return nil, errors.New("the browser authority for this turn is unavailable")
	}
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, browserToolTimeout)
	cmd := exec.CommandContext(ctx, "node", script)
	// The helper's own trusted directory, never a workspace the Agent can write.
	cmd.Dir = filepath.Dir(script)
	cmd.Env = core.MergeEnv(os.Environ(), append(append([]string(nil), s.extraEnv...), "TOMAKO_TASK_ENV_FILE="+path))
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.WaitDelay = 2 * time.Second
	var secrets []string
	for _, value := range []string{runtime.MachineCapabilityToken, runtime.ImageCapabilityToken, runtime.DocumentCapabilityToken,
		runtime.EmployeeCommandCapabilityToken, runtime.ProductUpdateCapabilityToken, runtime.TaskAuthorityEnvelopeB64} {
		if value = strings.TrimSpace(value); len(value) >= 8 {
			secrets = append(secrets, value)
		}
	}
	return &browserToolCall{cmd: cmd, ctx: ctx, cancel: cancel, cleanup: func() { removeTaskRuntimeEnv(path) }, secrets: secrets}, nil
}

type browserToolResult struct {
	Text      string
	ImagePath string
	ImageMime string
}

// run returns the helper's text (and a screenshot to attach), or its error in words the Agent can act on.
func (c *browserToolCall) run() (browserToolResult, error) {
	defer c.cancel()
	defer c.cleanup()
	stdout := &limitedBuffer{limit: browserToolOutputLimit}
	stderr := &limitedBuffer{limit: 16 * 1024}
	c.cmd.Stdout = stdout
	c.cmd.Stderr = stderr
	runErr := c.cmd.Run()
	if errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
		return browserToolResult{}, errors.New("the browser step did not answer in time; its effect is unknown: read the page before repeating it")
	}
	if c.ctx.Err() != nil {
		return browserToolResult{}, errors.New("the browser step was interrupted before it answered; read the page before repeating it")
	}
	var answer struct {
		OK    bool   `json:"ok"`
		Text  string `json:"text"`
		Error string `json:"error"`
		Image *struct {
			Path string `json:"path"`
			Mime string `json:"mime"`
		} `json:"image"`
	}
	line := lastJSONLine(stdout.Bytes())
	if line == nil || json.Unmarshal(line, &answer) != nil {
		if runErr != nil {
			return browserToolResult{}, errors.New("the browser step failed without an answer; read the page before repeating it")
		}
		return browserToolResult{}, errors.New("the browser step returned no answer; read the page before repeating it")
	}
	if !answer.OK {
		return browserToolResult{}, errors.New(c.redact(limitRunes(strings.TrimSpace(answer.Error), 1200)))
	}
	result := browserToolResult{Text: c.redact(answer.Text)}
	if answer.Image != nil && answer.Image.Path != "" {
		result.ImagePath, result.ImageMime = answer.Image.Path, answer.Image.Mime
	}
	return result, nil
}

func (c *browserToolCall) redact(text string) string {
	for _, secret := range c.secrets {
		text = strings.ReplaceAll(text, secret, "[redacted]")
	}
	return text
}

// browserToolContent is the text, plus the screenshot inline so the model sees it in the same call.
func browserToolContent(result browserToolResult) []map[string]any {
	items := []map[string]any{{"type": "inputText", "text": result.Text}}
	if result.ImagePath == "" || (result.ImageMime != "image/png" && result.ImageMime != "image/jpeg") {
		return items
	}
	info, err := os.Stat(result.ImagePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > browserToolImageLimit {
		return items
	}
	data, err := os.ReadFile(result.ImagePath)
	if err != nil {
		return items
	}
	return append(items, map[string]any{"type": "inputImage", "imageUrl": "data:" + result.ImageMime + ";base64," + base64.StdEncoding.EncodeToString(data)})
}

func (s *appServerSession) handleBrowserToolCall(rawID json.RawMessage, arguments map[string]any) {
	call, err := s.prepareBrowserTool(arguments)
	if err != nil {
		s.writeDynamicToolResponse(rawID, false, err.Error())
		return
	}
	go func() {
		result, err := call.run()
		if err != nil {
			s.writeDynamicToolResponse(rawID, false, err.Error())
			return
		}
		_ = s.writeJSON(map[string]any{"jsonrpc": "2.0", "id": rawID,
			"result": map[string]any{"success": true, "contentItems": browserToolContent(result)}})
	}()
}
