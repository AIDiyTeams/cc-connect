package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

const webReadToolName = "tomako_read_web_page"

const (
	webReadTimeout     = 45 * time.Second
	webReadQueueWait   = 30 * time.Second
	webReadOutputLimit = 128 * 1024
)

// A read may start a headless browser, and the bridge host is small and shared
// by several conversations, so reads queue process-wide.
var webReadSlots = make(chan struct{}, 2)

// Serialize whole browser-capable reader claims and release partial claims on
// cancellation. Direct-HTTP inventories use their own bounded batch budget.
var webReadAdmission = make(chan struct{}, 1)

func acquireWebReadSlots(ctx context.Context, count int) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, webReadQueueWait)
	defer cancel()
	select {
	case webReadAdmission <- struct{}{}:
		defer func() { <-webReadAdmission }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	held := 0
	release := func() {
		for held > 0 {
			<-webReadSlots
			held--
		}
	}
	for held < count {
		select {
		case webReadSlots <- struct{}{}:
			held++
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// The reader handles untrusted pages, so it receives only what Node and the
// browser need, never the bridge's provider keys or a session's capability tokens.
var webReadEnvKeys = map[string]bool{
	"PATH": true, "HOME": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true,
	"TMPDIR": true, "SKILLS_OL_DIR": true, "PLAYWRIGHT_BROWSERS_PATH": true, "NODE_PATH": true,
}

func webReadDynamicTool() map[string]any {
	return map[string]any{
		"type":         "function",
		"name":         webReadToolName,
		"deferLoading": false, "supportsParallelToolCalls": true,
		"description": "Read one public web page and get its readable text, final URL, title and links, with a status that says whether the page was actually read. " +
			"Use it to check facts on the source itself, such as official pricing, documentation and announcements, instead of fetching pages with shell commands. " +
			"Long pages come in parts: use the returned nextStart, never estimate fixed offsets. Use mode=outline for a bounded heading directory (not a body read), then choose exact [start,endExclusive) UTF-16 ranges of 1 to 24000 characters. Results include source metadata and a task-local hashed receipt. Any status other than ok means the page was not read, so facts from it remain unverified. " +
			"Pages are fetched directly and rendered in a browser when they look script-built; set render to true when the text still looks incomplete, for example missing prices or table values.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":           map[string]any{"type": "string", "format": "uri", "description": "Absolute http(s) URL of a public page."},
				"start":         map[string]any{"type": "integer", "minimum": 0, "description": "Character offset given by a previous result to continue a long page; omit to read from the beginning."},
				"render":        map[string]any{"type": "boolean", "description": "Load the page in a browser even when the fetched HTML looks complete."},
				"mode":          map[string]any{"type": "string", "enum": []string{"body", "outline"}, "description": "body (default) reads text; outline only lists actual body heading locations and is not body evidence."},
				"endExclusive":  map[string]any{"type": "integer", "description": "Body only: exclusive end offset, greater than start by 1 to 24000 UTF-16 code units. Reads the exact range, including tiny missing spans, without line-boundary adjustment."},
				"outlineOffset": map[string]any{"type": "integer", "description": "Outline only: nonnegative heading offset from outlinePage.nextOffset, default 0."},
				"outlineLimit":  map[string]any{"type": "integer", "description": "Outline only: 1 to 50 headings, default 40. Follow outlinePage.nextOffset until null when needed."},
			},
			"required":             []string{"url"},
			"additionalProperties": false,
		},
	}
}

// webReadScript returns Skills-OL's reader when it is installed; until then the tool is not offered.
func (s *appServerSession) webReadScript() string {
	return s.webReaderScript("web-read.mjs")
}

func (s *appServerSession) webReaderScript(name string) string {
	for _, entry := range core.MergeEnv(os.Environ(), s.extraEnv) {
		if root, ok := strings.CutPrefix(entry, "SKILLS_OL_DIR="); ok && root != "" {
			path := filepath.Join(root, name)
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path
			}
		}
	}
	return ""
}

// webReadToolAvailable: conversations can read public pages; dedicated analysis
// workflows keep exactly the tools and budgets they were designed with.
func (s *appServerSession) webReadToolAvailable() bool {
	if s.isBrandAnalysisRuntime() || s.isUserVoiceArchiveRuntime() || s.isUserVoiceJudgmentRuntime() {
		return false
	}
	s.runtimeMu.RLock()
	conversation := strings.TrimSpace(s.runtime.ChatSessionID) != ""
	s.runtimeMu.RUnlock()
	return conversation && s.webReadScript() != ""
}

func webReadEnv() []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok && webReadEnvKeys[key] {
			env = append(env, entry)
		}
	}
	return env
}

func nonNegativeInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number > math.MaxInt32 || number != math.Trunc(number) {
		return 0, false
	}
	return int(number), true
}

// limitedBuffer keeps the first limit bytes and records that more arrived.
type limitedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func (s *appServerSession) readWebPage(arguments map[string]any) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(stringArg(arguments["url"])))
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("url must be an absolute public http/https URL")
	}
	if !isPublicHostname(parsed.Hostname()) {
		return "", fmt.Errorf("url host must be public")
	}
	script := s.webReadScript()
	if script == "" {
		return "", fmt.Errorf("web page reading is not installed for this conversation")
	}
	args := []string{script, parsed.String(), "--format=agent"}
	mode := "body"
	if value, exists := arguments["mode"]; exists {
		mode, _ = value.(string)
		if mode != "body" && mode != "outline" {
			return "", fmt.Errorf("mode must be body or outline")
		}
		args = append(args, "--mode="+mode)
	}
	if endValue, exists := arguments["endExclusive"]; exists {
		end, valid := nonNegativeInt(endValue)
		start := 0
		if value, exists := arguments["start"]; exists {
			var ok bool
			start, ok = nonNegativeInt(value)
			if !ok {
				return "", fmt.Errorf("exact range start must be a nonnegative integer")
			}
		}
		if mode != "body" || !valid || end <= start || end-start > 24000 {
			return "", fmt.Errorf("body range must contain 1 to 24000 UTF-16 code units")
		}
		args = append(args, fmt.Sprintf("--end-exclusive=%d", end))
	}
	if mode == "outline" {
		if _, exists := arguments["start"]; exists {
			return "", fmt.Errorf("outline cannot request a body start")
		}
	}
	for _, field := range []struct {
		key, flag string
		max       int
	}{{"outlineOffset", "outline-offset", math.MaxInt32}, {"outlineLimit", "outline-limit", 50}} {
		if value, exists := arguments[field.key]; exists {
			n, ok := nonNegativeInt(value)
			if mode != "outline" || !ok || n > field.max || (field.key == "outlineLimit" && n == 0) {
				return "", fmt.Errorf("invalid %s: outline offset must be nonnegative, limit 1 to 50", field.key)
			}
			args = append(args, fmt.Sprintf("--%s=%d", field.flag, n))
		}
	}
	if start, ok := nonNegativeInt(arguments["start"]); ok && start > 0 {
		args = append(args, fmt.Sprintf("--start=%d", start))
	}
	if render, _ := arguments["render"].(bool); render {
		args = append(args, "--render=always")
	}

	release, err := acquireWebReadSlots(s.ctx, 1)
	if err != nil {
		if s.ctx.Err() != nil {
			return "", s.ctx.Err()
		}
		return "", fmt.Errorf("the web page reader is busy; try again shortly")
	}
	defer release()
	ctx, cancel := context.WithTimeout(s.ctx, webReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", args...)
	cmd.Dir = s.workDir
	cmd.Env = webReadEnv()
	if s.permissionsProfile != "" && s.sandboxScratchDir == "" {
		return "", fmt.Errorf("web reader session scratch is unavailable")
	}
	if s.sandboxScratchDir != "" {
		// The trusted session setup owns this directory; model arguments cannot select it.
		cmd.Dir = s.sandboxScratchDir
		cmd.Env = append(cmd.Env, "TOMAKO_WEB_READ_HOST_IO=1", "TOMAKO_WEB_READ_SCRATCH_DIR="+s.sandboxScratchDir)
	}
	stdout := &limitedBuffer{limit: webReadOutputLimit}
	stderr := &limitedBuffer{limit: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("reading the page timed out; facts from it remain unverified")
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("the web page reader failed: %s", limitRunes(message, 300))
	}
	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return "", fmt.Errorf("the web page reader returned nothing")
	}
	if stdout.truncated {
		output += "\n[Output cut at the tool limit; continue with start to read further.]"
	}
	return output, nil
}

// webReadPublicActivity shows a page read in the conversation the way native web
// receipts appear: the page address, and no claim that the read succeeded.
func webReadPublicActivity(arguments any, status string) *core.PublicActivity {
	args, _ := arguments.(map[string]any)
	if text, ok := arguments.(string); ok {
		_ = json.Unmarshal([]byte(text), &args)
	}
	address, _ := args["url"].(string)
	return webPublicActivity(map[string]any{"action": map[string]any{"type": "openPage", "url": address}}, status)
}
