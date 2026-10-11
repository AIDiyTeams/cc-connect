package codex

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const webInventoryToolName = "tomako_read_web_inventory"

// Inventory and discovery force direct HTTP in Skills-OL. Admit at most three
// two-worker batches process-wide; they never consume the browser-capable pool.
var webInventorySlots = make(chan struct{}, 3)

func acquireWebInventorySlot(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, webReadQueueWait)
	defer cancel()
	select {
	case webInventorySlots <- struct{}{}:
		release := func() { <-webInventorySlots }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func webInventoryDynamicTool() map[string]any {
	filePage := func(receipt bool) map[string]any {
		properties := map[string]any{
			"path":   map[string]any{"type": "string", "minLength": 1, "maxLength": 4096, "description": "Nonempty existing current-session scratch JSON file path, at most 4096 bytes. The host validates file scope and size."},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Zero-based nonnegative integer offset; default 0."},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Integer from 1 to 50 entries; default 50."},
		}
		required := []string{"path"}
		if receipt {
			properties["sha256"] = map[string]any{"type": "string", "pattern": "^[a-fA-F0-9]{64}$", "description": "Exact SHA-256 returned with the inventory receipt: 64 hexadecimal characters."}
			required = append(required, "sha256")
		}
		return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	}
	return map[string]any{
		"type": "function", "name": webInventoryToolName, "deferLoading": false, "supportsParallelToolCalls": true,
		"description": "Screen up to 50 public URLs from one origin for a shallow website inventory with two HTTP workers per batch, sharing a process-wide limit of three batches (six HTTP workers), separate from the single-page browser budget. " +
			"Returns titles, descriptions, headings and short snippets, not a full-page read or proof of content coverage. " +
			"Use discover with explicit source kinds and allowed origins to extract bounded sitemap/navigation URL files without fetching articles. Source completion is structural, not a site-coverage verdict. " +
			"Use a sourceFile JSON URL list to avoid copying URL arrays, or receipt plus its SHA-256 to page existing compact rows without network requests. " +
			"Follow page.nextOffset for more saved rows and source.nextOffset for more source URLs. Output is bounded to 24000 bytes. " +
			"Use tomako_read_web_page for pages requiring deeper evidence. The receipt retains full results; screening decisions belong to the Agent.",
		"inputSchema": map[string]any{
			"type": "object", "additionalProperties": false,
			"oneOf": []map[string]any{{"required": []string{"urls"}}, {"required": []string{"sourceFile"}}, {"required": []string{"receipt"}}, {"required": []string{"discover"}}},
			"properties": map[string]any{
				"urls": map[string]any{"type": "array", "minItems": 1, "maxItems": 50,
					"items":       map[string]any{"type": "string", "format": "uri", "maxLength": 2048, "description": "Public URL, at most 2048 bytes."},
					"description": "Supply 1 to 50 public http(s) URLs from the same origin. Input indexes are preserved in rows and remaining."},
				"sourceFile": filePage(false),
				"receipt":    filePage(true),
				"discover": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"sources", "allowedOrigins"}, "properties": map[string]any{
					"allowedOrigins": map[string]any{"type": "array", "minItems": 1, "maxItems": 4, "items": map[string]any{"type": "string", "maxLength": 512, "description": "Public origin, at most 512 bytes."}, "description": "Supply 1 to 4 explicit public HTTP(S) origins. No paths, credentials or nonstandard ports."},
					"sources": map[string]any{"type": "array", "minItems": 1, "maxItems": 8, "items": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "url"}, "properties": map[string]any{
						"kind": map[string]any{"type": "string", "enum": []string{"SITEMAP", "CONTENT_DIRECTORY", "SITE_NAVIGATION"}},
						"url":  map[string]any{"type": "string", "maxLength": 2048, "description": "Public source URL, at most 2048 bytes."},
					}}, "description": "Supply 1 to 8 explicit sources per call. Expansion may fetch at most 12 source documents, depth 3, two HTTP workers per batch within the shared six-worker inventory budget. Only sitemap indexes and rel=next are followed; other links need Agent selection."},
				}},
			},
		},
	}
}

func (s *appServerSession) webInventoryToolAvailable() bool {
	return s.webReadToolAvailable() && s.webReaderScript("web-inventory.mjs") != ""
}

func webInventoryInput(arguments map[string]any) ([]byte, int, error) {
	for key := range arguments {
		if key != "urls" && key != "sourceFile" && key != "receipt" && key != "discover" && key != "deadlineMs" && key != "maxOutputBytes" {
			return nil, 0, fmt.Errorf("unsupported web inventory argument: %s", key)
		}
	}
	modes := 0
	for _, key := range []string{"urls", "sourceFile", "receipt", "discover"} {
		if value, present := arguments[key]; present {
			modes++
			if key == "discover" {
				if err := validateInventoryDiscovery(value); err != nil {
					return nil, 0, err
				}
			} else if key != "urls" {
				if err := validateInventoryFilePage(key, value); err != nil {
					return nil, 0, err
				}
			}
		}
	}
	if modes != 1 {
		return nil, 0, fmt.Errorf("exactly one of urls, sourceFile, receipt or discover is required")
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

// This is protocol validation. URL resolution, redirects and task-file containment
// remain enforced by the reader; no source kind is inferred from prose or URLs.
func validateInventoryDiscovery(value any) error {
	discovery, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("discover must be an object")
	}
	for key := range discovery {
		if key != "sources" && key != "allowedOrigins" {
			return fmt.Errorf("unsupported discover argument: %s", key)
		}
	}
	originOf := func(value any, originOnly bool) (string, error) {
		address, ok := value.(string)
		parsed, err := url.Parse(address)
		if !ok || len(address) > 2048 || err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !isPublicHostname(parsed.Hostname()) || (parsed.Port() != "" && parsed.Port() != "80" && parsed.Port() != "443") {
			return "", fmt.Errorf("discovery requires public HTTP(S) URLs on standard ports")
		}
		if originOnly && (len(address) > 512 || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "") {
			return "", fmt.Errorf("allowedOrigins cannot contain paths, queries or fragments")
		}
		port := parsed.Port()
		if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
			port = ""
		}
		host := strings.ToLower(parsed.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port != "" {
			host += ":" + port
		}
		return parsed.Scheme + "://" + host, nil
	}
	origins, ok := discovery["allowedOrigins"].([]any)
	if !ok || len(origins) < 1 || len(origins) > 4 {
		return fmt.Errorf("discover.allowedOrigins must contain 1 to 4 origins")
	}
	allowed := map[string]bool{}
	for _, origin := range origins {
		normalized, err := originOf(origin, true)
		if err != nil {
			return err
		}
		allowed[normalized] = true
	}
	sources, ok := discovery["sources"].([]any)
	if !ok || len(sources) < 1 || len(sources) > 8 {
		return fmt.Errorf("discover.sources must contain 1 to 8 sources")
	}
	for _, value := range sources {
		source, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("discovery source must be an object")
		}
		for key := range source {
			if key != "url" && key != "kind" {
				return fmt.Errorf("unsupported discovery source argument: %s", key)
			}
		}
		kind, ok := source["kind"].(string)
		if !ok || (kind != "SITEMAP" && kind != "CONTENT_DIRECTORY" && kind != "SITE_NAVIGATION") {
			return fmt.Errorf("invalid discovery source kind")
		}
		origin, err := originOf(source["url"], false)
		if err != nil {
			return err
		}
		if !allowed[origin] {
			return fmt.Errorf("discovery source origin is not allowed")
		}
	}
	return nil
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
	if s.permissionsProfile != "" && s.sandboxScratchDir == "" {
		return "", fmt.Errorf("web inventory requires the session's authorized scratch directory")
	}
	script := s.webReaderScript("web-inventory.mjs")
	if script == "" {
		return "", fmt.Errorf("web inventory is not installed for this conversation")
	}
	if _, readingReceipt := arguments["receipt"]; !readingReceipt {
		release, err := acquireWebInventorySlot(s.ctx)
		if err != nil {
			return "", fmt.Errorf("web inventory could not acquire the shared HTTP inventory budget: %w", err)
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
	if s.sandboxScratchDir != "" {
		closeFiles, err := s.prepareInventoryFiles(cmd, arguments)
		if err != nil {
			return "", err
		}
		defer closeFiles()
	}
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

// The server owns scratchDir and its ancestors. Input descriptors pin every
// traversed component. The child starts in scratchDir, so its cwd pins the
// authorized directory for exclusive, direct-child receipt creation.
func (s *appServerSession) prepareInventoryFiles(cmd *exec.Cmd, arguments map[string]any) (func(), error) {
	cleanup := func() {
		for _, file := range cmd.ExtraFiles {
			_ = file.Close()
		}
	}
	cmd.Dir = s.sandboxScratchDir
	cmd.Env = append(cmd.Env, "TOMAKO_WEB_INVENTORY_HOST_IO=1",
		"TOMAKO_WEB_INVENTORY_SCRATCH_DIR="+s.sandboxScratchDir)
	for _, mode := range []string{"sourceFile", "receipt"} {
		page, present := arguments[mode].(map[string]any)
		if !present {
			continue
		}
		path := page["path"].(string)
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.workDir, path)
		}
		relative, err := filepath.Rel(s.sandboxScratchDir, path)
		if err != nil || !filepath.IsLocal(relative) || relative == "." {
			return nil, fmt.Errorf("web inventory input must be inside this session's scratch directory")
		}
		file, err := openImageWithoutSymlinks(s.sandboxScratchDir, relative)
		if err != nil {
			return nil, fmt.Errorf("web inventory input file is unavailable")
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, file)
		info, err := file.Stat()
		maxBytes := int64(1024 * 1024)
		if mode == "receipt" {
			maxBytes = 32 * 1024 * 1024
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
			cleanup()
			return nil, fmt.Errorf("web inventory input must be a bounded regular file")
		}
		cmd.Env = append(cmd.Env, "TOMAKO_WEB_INVENTORY_INPUT_FD="+strconv.Itoa(2+len(cmd.ExtraFiles)),
			"TOMAKO_WEB_INVENTORY_INPUT_PATH="+filepath.Join(s.sandboxScratchDir, relative))
	}
	return cleanup, nil
}
