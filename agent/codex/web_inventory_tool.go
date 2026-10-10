package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

const webInventoryToolName = "tomako_read_web_inventory"

func webInventoryDynamicTool() map[string]any {
	return map[string]any{
		"type": "function", "name": webInventoryToolName, "deferLoading": false,
		"description": "Screen up to 50 public URLs from one origin for a shallow website inventory with two shared workers. " +
			"Returns titles, descriptions, headings and short snippets, not a full-page read or proof of content coverage. " +
			"Use tomako_read_web_page for pages requiring deeper evidence. Partial results identify remaining input indexes; the receipt retains full results.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"urls"},
			"properties": map[string]any{
				"urls": map[string]any{"type": "array", "minItems": 1, "maxItems": 50,
					"items":       map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
					"description": "Public http(s) URLs from the same origin. Input indexes are preserved in rows and remaining."},
				"deadlineMs":     map[string]any{"type": "integer", "minimum": 100, "maximum": 30000, "description": "Optional lower execution deadline; default 30000 ms."},
				"maxOutputBytes": map[string]any{"type": "integer", "minimum": 2048, "maximum": 24000, "description": "Optional lower JSON output budget; default 24000 bytes."},
			},
		},
	}
}

func (s *appServerSession) webInventoryToolAvailable() bool {
	return s.webReadToolAvailable() && s.webReaderScript("web-inventory.mjs") != ""
}

func webInventoryInput(arguments map[string]any) ([]byte, int, error) {
	for key := range arguments {
		if key != "urls" && key != "deadlineMs" && key != "maxOutputBytes" {
			return nil, 0, fmt.Errorf("unsupported web inventory argument: %s", key)
		}
	}
	urls, ok := arguments["urls"].([]any)
	if !ok || len(urls) < 1 || len(urls) > 50 {
		return nil, 0, fmt.Errorf("urls must contain 1 to 50 public URLs from the same origin")
	}
	origin := ""
	for _, value := range urls {
		address, ok := value.(string)
		parsed, err := url.Parse(address)
		if !ok || len(address) > 2048 || err != nil || parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || !isPublicHostname(parsed.Hostname()) {
			return nil, 0, fmt.Errorf("each inventory URL must be an absolute public http/https URL without credentials")
		}
		port := parsed.Port()
		if port == "" {
			port = "80"
			if parsed.Scheme == "https" {
				port = "443"
			}
		}
		current := parsed.Scheme + "://" + strings.ToLower(parsed.Hostname()) + ":" + port
		if origin != "" && current != origin {
			return nil, 0, fmt.Errorf("inventory URLs must share one origin")
		}
		origin = current
	}
	limits := map[string][2]int{"deadlineMs": {100, 30000}, "maxOutputBytes": {2048, 24000}}
	maxBytes := 24000
	for key, bounds := range limits {
		if value, present := arguments[key]; present {
			integer, ok := nonNegativeInt(value)
			if !ok || integer < bounds[0] || integer > bounds[1] {
				return nil, 0, fmt.Errorf("%s must be an integer from %d to %d", key, bounds[0], bounds[1])
			}
			if key == "maxOutputBytes" {
				maxBytes = integer
			}
		}
	}
	input, err := json.Marshal(arguments)
	return input, maxBytes, err
}

func (s *appServerSession) readWebInventory(arguments map[string]any) (string, error) {
	input, maxBytes, err := webInventoryInput(arguments)
	if err != nil {
		return "", err
	}
	script := s.webReaderScript("web-inventory.mjs")
	if script == "" {
		return "", fmt.Errorf("web inventory is not installed for this conversation")
	}
	release, err := acquireWebReadSlots(s.ctx, 2)
	if err != nil {
		return "", fmt.Errorf("web inventory could not acquire the shared reader budget: %w", err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(s.ctx, 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script)
	prepareCmdForKill(cmd)
	cmd.Cancel = func() error { return forceKillCmd(cmd) }
	cmd.WaitDelay = time.Second
	cmd.Dir, cmd.Env, cmd.Stdin = s.workDir, webReadEnv(), bytes.NewReader(input)
	stdout, stderr := &limitedBuffer{limit: maxBytes + 1}, &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("web inventory cancelled or timed out: %w", ctx.Err())
		}
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("web inventory failed: %s", limitRunes(message, 300))
	}
	output := bytes.TrimSpace(stdout.Bytes())
	if stdout.truncated || len(output) > maxBytes || !json.Valid(output) {
		return "", fmt.Errorf("web inventory returned invalid or oversized JSON; do not treat it as evidence")
	}
	return string(output), nil
}
