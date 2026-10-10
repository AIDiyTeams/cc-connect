package codex

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

const webInventoryToolName = "tomako_read_web_inventory"

func webInventoryDynamicTool() map[string]any {
	filePage := func(receipt bool) map[string]any {
		properties := map[string]any{
			"path":   map[string]any{"type": "string", "minLength": 1, "maxLength": 4096, "description": "Existing task-local .tmp JSON file. Real-path containment and file size are validated by the reader."},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based offset; default 0."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum entries; default 50."},
		}
		required := []string{"path"}
		if receipt {
			properties["sha256"] = map[string]any{"type": "string", "pattern": "^[a-fA-F0-9]{64}$", "description": "Exact SHA-256 returned with the inventory receipt."}
			required = append(required, "sha256")
		}
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	return map[string]any{
		"type": "function", "name": webInventoryToolName, "deferLoading": false,
		"description": "Screen up to 50 public URLs from one origin for a shallow website inventory with two shared workers. " +
			"Returns titles, descriptions, headings and short snippets, not a full-page read or proof of content coverage. " +
			"Use a sourceFile JSON URL list to avoid copying URL arrays, or receipt plus its SHA-256 to page existing compact rows without network requests. " +
			"Follow page.nextOffset for more saved rows and source.nextOffset for more source URLs. Output is bounded to 24000 bytes. " +
			"Use tomako_read_web_page for pages requiring deeper evidence. The receipt retains full results; screening decisions belong to the Agent.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"oneOf": []map[string]any{{"required": []string{"urls"}}, {"required": []string{"sourceFile"}}, {"required": []string{"receipt"}}},
			"properties": map[string]any{
				"urls": map[string]any{"type": "array", "minItems": 1, "maxItems": 50,
					"items":       map[string]any{"type": "string", "format": "uri", "maxLength": 2048},
					"description": "Public http(s) URLs from the same origin. Input indexes are preserved in rows and remaining."},
				"sourceFile": filePage(false),
				"receipt":    filePage(true),
			},
		},
	}
}

func (s *appServerSession) webInventoryToolAvailable() bool {
	return s.webReadToolAvailable() && s.webReaderScript("web-inventory.mjs") != ""
}

func webInventoryInput(arguments map[string]any) ([]byte, int, error) {
	for key := range arguments {
		if key != "urls" && key != "sourceFile" && key != "receipt" && key != "deadlineMs" && key != "maxOutputBytes" {
			return nil, 0, fmt.Errorf("unsupported web inventory argument: %s", key)
		}
	}
	modes := 0
	for _, key := range []string{"urls", "sourceFile", "receipt"} {
		if value, present := arguments[key]; present {
			modes++
			if key != "urls" {
				if err := validateInventoryFilePage(key, value); err != nil {
					return nil, 0, err
				}
			}
		}
	}
	if modes != 1 {
		return nil, 0, fmt.Errorf("exactly one of urls, sourceFile or receipt is required")
	}
	if value, present := arguments["urls"]; present {
		if err := validateInventoryURLs(value); err != nil {
			return nil, 0, err
		}
	}
	// Keep old in-flight callers compatible; these controls are no longer advertised.
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

func validateInventoryFilePage(mode string, value any) error {
	page, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", mode)
	}
	for key := range page {
		if key != "path" && key != "offset" && key != "limit" && !(mode == "receipt" && key == "sha256") {
			return fmt.Errorf("unsupported %s argument: %s", mode, key)
		}
	}
	path, ok := page["path"].(string)
	if !ok || strings.TrimSpace(path) == "" || len(path) > 4096 || strings.ContainsRune(path, '\x00') {
		return fmt.Errorf("%s.path must be a nonempty file path up to 4096 bytes", mode)
	}
	for _, key := range []string{"offset", "limit"} {
		if value, present := page[key]; present {
			n, ok := nonNegativeInt(value)
			if !ok || (key == "limit" && (n < 1 || n > 50)) {
				return fmt.Errorf("%s.%s must be a nonnegative integer; limit must be from 1 to 50", mode, key)
			}
		}
	}
	if mode == "receipt" {
		hash, ok := page["sha256"].(string)
		if !ok || len(hash) != 64 {
			return fmt.Errorf("receipt.sha256 must contain 64 hexadecimal characters")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("receipt.sha256 must contain 64 hexadecimal characters")
		}
	}
	return nil
}

func validateInventoryURLs(value any) error {
	urls, ok := value.([]any)
	if !ok || len(urls) < 1 || len(urls) > 50 {
		return fmt.Errorf("urls must contain 1 to 50 public URLs from the same origin")
	}
	origin := ""
	for _, value := range urls {
		address, ok := value.(string)
		parsed, err := url.Parse(address)
		if !ok || len(address) > 2048 || err != nil || parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || !isPublicHostname(parsed.Hostname()) {
			return fmt.Errorf("each inventory URL must be an absolute public http/https URL without credentials")
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
			return fmt.Errorf("inventory URLs must share one origin")
		}
		origin = current
	}
	return nil
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
	if _, readingReceipt := arguments["receipt"]; !readingReceipt {
		release, err := acquireWebReadSlots(s.ctx, 2)
		if err != nil {
			return "", fmt.Errorf("web inventory could not acquire the shared reader budget: %w", err)
		}
		defer release()
	}
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
