package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func inventoryTestSession(t *testing.T, script string) *appServerSession {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	s := webReadTestSession(t, "")
	if err := os.WriteFile(filepath.Join(s.workDir, "web-inventory.mjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWebInventoryThreadRegistrationOnlyOptsBoundedReadersIntoParallelCalls(t *testing.T) {
	s := inventoryTestSession(t, "")
	encoded, err := json.Marshal(s.threadRequestParams())
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		DynamicTools []struct {
			Name     string `json:"name"`
			Parallel bool   `json:"supportsParallelToolCalls"`
		} `json:"dynamicTools"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	opted := 0
	for _, tool := range wire.DynamicTools {
		expected := tool.Name == webReadToolName || tool.Name == webInventoryToolName
		if tool.Parallel != expected {
			t.Fatalf("unexpected parallel admission for %s: %v", tool.Name, tool.Parallel)
		}
		if tool.Parallel {
			opted++
		}
	}
	if opted != 2 {
		t.Fatalf("bounded reader registrations missing: %d", opted)
	}
}

func TestWebInventoryBoundsRemainVisibleAfterCodexSchemaProjection(t *testing.T) {
	// Codex's typed JsonSchema preserves descriptions/properties/items but drops
	// maxItems, length, numeric-range and pattern keywords. Exercise that wire
	// projection so a hard-rejected request is not advertised without its bounds.
	type visibleSchema struct {
		Description string                     `json:"description"`
		Properties  map[string]json.RawMessage `json:"properties"`
		Items       json.RawMessage            `json:"items"`
	}
	encoded, err := json.Marshal(webInventoryDynamicTool()["inputSchema"])
	if err != nil {
		t.Fatal(err)
	}
	for path, required := range map[string]string{
		"urls":                          "1 to 50",
		"urls.items":                    "2048 bytes",
		"discover.sources":              "1 to 8",
		"discover.sources.items.url":    "2048 bytes",
		"discover.allowedOrigins":       "1 to 4",
		"discover.allowedOrigins.items": "512 bytes",
		"sourceFile.path":               "4096 bytes",
		"sourceFile.offset":             "nonnegative",
		"sourceFile.limit":              "1 to 50",
		"receipt.sha256":                "64 hexadecimal",
	} {
		t.Run(path, func(t *testing.T) {
			raw := json.RawMessage(encoded)
			for _, part := range strings.Split(path, ".") {
				var node visibleSchema
				if err := json.Unmarshal(raw, &node); err != nil {
					t.Fatal(err)
				}
				if part == "items" {
					raw = node.Items
				} else {
					raw = node.Properties[part]
				}
			}
			var leaf visibleSchema
			if err := json.Unmarshal(raw, &leaf); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(leaf.Description, required) {
				t.Fatalf("model-visible description %q lost required bound %q", leaf.Description, required)
			}
		})
	}
}

func TestWebInventoryToolUsesTheConversationBoundaryAndInstalledScript(t *testing.T) {
	s := inventoryTestSession(t, "")
	offered := func() bool {
		tools, _ := s.threadRequestParams()["dynamicTools"].([]map[string]any)
		for _, tool := range tools {
			if tool["name"] == webInventoryToolName {
				return true
			}
		}
		return false
	}
	if !offered() {
		t.Fatal("inventory not advertised")
	}
	s.runtime.LogicalModel = "DEEPSEEK_V4_FLASH_SEARCH"
	s.runtime.OutputSchema = json.RawMessage(`{"type":"object"}`)
	for _, scene := range []string{"brand_analysis", "growth_opportunity_user_voice_search", "growth_opportunity_user_voice_judge"} {
		s.runtime.Scene = scene
		if offered() {
			t.Fatalf("inventory offered to dedicated workflow %s", scene)
		}
	}
	s.runtime.Scene, s.runtime.ChatSessionID = "", ""
	if offered() {
		t.Fatal("inventory offered outside conversation")
	}
	s.runtime.ChatSessionID = "csess-a"
	if err := os.Remove(filepath.Join(s.workDir, "web-inventory.mjs")); err != nil {
		t.Fatal(err)
	}
	if offered() || !advertisesWebRead(s) {
		t.Fatal("missing inventory must not remove installed single-page reader")
	}
}

func TestWebInventoryRejectsUnsafeAndUnboundedRequests(t *testing.T) {
	for name, input := range map[string]map[string]any{
		"empty":        {"urls": []any{}},
		"too many":     {"urls": make([]any, 51)},
		"private":      {"urls": []any{"http://127.0.0.1/admin"}},
		"credentials":  {"urls": []any{"https://user:secret@example.com"}},
		"file":         {"urls": []any{"file:///etc/passwd"}},
		"origin":       {"urls": []any{"https://example.com", "https://other.example.com"}},
		"deadline":     {"urls": []any{"https://example.com"}, "deadlineMs": float64(30001)},
		"fraction":     {"urls": []any{"https://example.com"}, "deadlineMs": 100.5},
		"output":       {"urls": []any{"https://example.com"}, "maxOutputBytes": float64(24001)},
		"small output": {"urls": []any{"https://example.com"}, "maxOutputBytes": float64(2047)},
		"unknown":      {"urls": []any{"https://example.com"}, "workers": float64(99)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := webInventoryInput(input); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestWebInventoryPassesJSONWithoutCredentialsAndPreservesPartialReceipt(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "provider-secret")
	s := inventoryTestSession(t, `let input='';for await (const x of process.stdin) input+=x;
if(process.env.DEEPSEEK_API_KEY || process.env.TOMAKO_IMAGE_CAPABILITY_TOKEN) process.exit(9);
process.stdout.write(JSON.stringify({status:'partial',shallow:true,remaining:[1],receipt:{path:'receipt.json',sha256:'abc'},input:JSON.parse(input)}));`)
	out, err := s.readWebInventory(map[string]any{"urls": []any{"https://example.com/a", "https://example.com/b"}, "deadlineMs": float64(100), "maxOutputBytes": float64(2048)})
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result["status"] != "partial" || result["shallow"] != true || result["receipt"] == nil || len(result["remaining"].([]any)) != 1 {
		t.Fatalf("partial evidence was altered: %s", out)
	}
	input := result["input"].(map[string]any)
	if input["deadlineMs"] != float64(100) || input["maxOutputBytes"] != float64(2048) {
		t.Fatalf("limits not propagated: %v", input)
	}
}

func TestWebInventoryFailsClosedForInvalidOrOversizedOutput(t *testing.T) {
	for _, script := range []string{`process.stdout.write('not JSON')`, `process.stdout.write(JSON.stringify({text:'a'.repeat(3000)}))`, `process.stderr.write('reader failed');process.exit(2)`} {
		s := inventoryTestSession(t, script)
		out, err := s.readWebInventory(map[string]any{"urls": []any{"https://example.com"}, "maxOutputBytes": float64(2048)})
		if err == nil || out != "" {
			t.Fatalf("invalid result accepted: %q %v", out, err)
		}
		if len(webInventorySlots) != 0 {
			t.Fatal("failed call leaked slots")
		}
	}
}

func TestWebInventoryCancellationStopsProcessAndReleasesBudget(t *testing.T) {
	s := inventoryTestSession(t, `import fs from 'node:fs';fs.writeFileSync('started','yes');setInterval(()=>{},1000);`)
	done := make(chan error, 1)
	go func() {
		_, err := s.readWebInventory(map[string]any{"urls": []any{"https://example.com"}})
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(s.workDir, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process did not start")
		}
		time.Sleep(time.Millisecond)
	}
	s.cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled inventory did not stop")
	}
	if len(webInventorySlots) != 0 {
		t.Fatal("cancelled inventory leaked budget")
	}
}

func TestWebReadWholeClaimsShareBudgetWithoutBatchDeadlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var active atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		count := 1 + i%2
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := acquireWebReadSlots(ctx, count)
			if err != nil {
				t.Errorf("whole claim failed: %v", err)
				return
			}
			if n := active.Add(int32(count)); n > 2 {
				t.Errorf("shared reader budget exceeded: %d", n)
			}
			time.Sleep(time.Millisecond)
			active.Add(-int32(count))
			release()
		}()
	}
	wg.Wait()
	if len(webReadSlots) != 0 || len(webReadAdmission) != 0 {
		t.Fatal("whole claims leaked budget")
	}
}

func TestWebReadCancelledPartialClaimReturnsItsHeldSlot(t *testing.T) {
	releaseSingle, err := acquireWebReadSlots(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSingle()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if release, err := acquireWebReadSlots(ctx, 2); err == nil {
		release()
		t.Fatal("batch acquired two slots while single-page read was active")
	}
	if len(webReadSlots) != 1 || len(webReadAdmission) != 0 {
		t.Fatalf("partial cancelled claim leaked: slots=%d gate=%d", len(webReadSlots), len(webReadAdmission))
	}
}

func TestWebInventorySupportsBoundedFileModesAndRejectsAmbiguity(t *testing.T) {
	hash := strings.Repeat("a", 64)
	for _, input := range []map[string]any{
		{"sourceFile": map[string]any{"path": ".tmp/urls.json", "offset": float64(50), "limit": float64(50)}},
		{"receipt": map[string]any{"path": ".tmp/web-inventory/batch/receipt.json", "sha256": hash, "offset": float64(0), "limit": float64(20)}},
	} {
		if _, maxBytes, err := webInventoryInput(input); err != nil || maxBytes != 24000 {
			t.Fatalf("valid file mode rejected: %v, bytes=%d", err, maxBytes)
		}
	}
	for name, input := range map[string]map[string]any{
		"no mode":         {},
		"two modes":       {"urls": []any{"https://example.com"}, "sourceFile": map[string]any{"path": ".tmp/urls.json"}},
		"null source":     {"sourceFile": nil},
		"missing path":    {"sourceFile": map[string]any{}},
		"empty path":      {"sourceFile": map[string]any{"path": " "}},
		"extra field":     {"sourceFile": map[string]any{"path": ".tmp/urls.json", "workers": float64(10)}},
		"negative offset": {"sourceFile": map[string]any{"path": ".tmp/urls.json", "offset": float64(-1)}},
		"fraction offset": {"sourceFile": map[string]any{"path": ".tmp/urls.json", "offset": 0.5}},
		"zero limit":      {"sourceFile": map[string]any{"path": ".tmp/urls.json", "limit": float64(0)}},
		"large limit":     {"receipt": map[string]any{"path": ".tmp/r.json", "sha256": hash, "limit": float64(51)}},
		"missing hash":    {"receipt": map[string]any{"path": ".tmp/r.json"}},
		"invalid hash":    {"receipt": map[string]any{"path": ".tmp/r.json", "sha256": strings.Repeat("z", 64)}},
		"short hash":      {"receipt": map[string]any{"path": ".tmp/r.json", "sha256": "abc"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := webInventoryInput(input); err == nil {
				t.Fatal("invalid mode accepted")
			}
		})
	}
}

func TestWebInventoryReceiptReadDoesNotWaitForNetworkBudget(t *testing.T) {
	release, err := acquireWebReadSlots(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for i := 0; i < 3; i++ {
		releaseHTTP, err := acquireWebInventorySlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer releaseHTTP()
	}
	s := inventoryTestSession(t, `let input='';for await(const x of process.stdin) input+=x;process.stdout.write(JSON.stringify({input:JSON.parse(input),page:{nextOffset:null,complete:true}}));`)
	ctx, cancel := context.WithTimeout(s.ctx, time.Second)
	defer cancel()
	s.ctx = ctx
	out, err := s.readWebInventory(map[string]any{"receipt": map[string]any{"path": ".tmp/web-inventory/batch/receipt.json", "sha256": strings.Repeat("a", 64)}})
	if err != nil || !strings.Contains(out, `"complete":true`) {
		t.Fatalf("local receipt blocked by network budget: %v %s", err, out)
	}
	if len(webReadSlots) != 2 || len(webInventorySlots) != 3 {
		t.Fatal("receipt changed network budget")
	}
}

func TestWebInventorySourceFileKeepsBoundedHTTPBudget(t *testing.T) {
	for i := 0; i < 3; i++ {
		release, err := acquireWebInventorySlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	s := inventoryTestSession(t, `import fs from 'node:fs';fs.writeFileSync('started','yes');process.stdout.write('{}');`)
	ctx, cancel := context.WithTimeout(s.ctx, 40*time.Millisecond)
	defer cancel()
	s.ctx = ctx
	_, err := s.readWebInventory(map[string]any{"sourceFile": map[string]any{"path": ".tmp/urls.json"}})
	if err == nil || !strings.Contains(err.Error(), "shared HTTP inventory budget") {
		t.Fatalf("scan bypassed shared budget: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.workDir, "started")); !os.IsNotExist(err) {
		t.Fatal("scan started without network budget")
	}
}

func TestWebInventorySchemaUsesExclusiveSourcesWithoutOutputKnobs(t *testing.T) {
	schema := webInventoryDynamicTool()["inputSchema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	for _, key := range []string{"urls", "sourceFile", "receipt", "discover"} {
		if properties[key] == nil {
			t.Fatalf("missing mode %s", key)
		}
	}
	for _, key := range []string{"maxOutputBytes", "deadlineMs"} {
		if _, ok := properties[key]; ok {
			t.Fatalf("model still controls %s", key)
		}
	}
	if modes, ok := schema["oneOf"].([]map[string]any); !ok || len(modes) != 4 {
		t.Fatal("schema does not require exactly one source")
	}
}

func TestWebInventoryDiscoverExplicitSourcesValidation(t *testing.T) {
	valid := map[string]any{"sources": []any{map[string]any{"kind": "SITEMAP", "url": "https://public.example/sitemap.xml"}}, "allowedOrigins": []any{"https://public.example"}}
	if _, _, err := webInventoryInput(map[string]any{"discover": valid}); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]map[string]any{
		"missing origins": {"sources": valid["sources"]},
		"unknown kind":    {"sources": []any{map[string]any{"kind": "AUTO", "url": "https://public.example/"}}, "allowedOrigins": valid["allowedOrigins"]},
		"outside origin":  {"sources": valid["sources"], "allowedOrigins": []any{"https://other.example"}},
		"private origin":  {"sources": []any{map[string]any{"kind": "SITE_NAVIGATION", "url": "http://127.0.0.1/"}}, "allowedOrigins": []any{"http://127.0.0.1"}},
		"origin path":     {"sources": valid["sources"], "allowedOrigins": []any{"https://public.example/path"}},
		"extra field":     {"sources": valid["sources"], "allowedOrigins": valid["allowedOrigins"], "recursive": true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := webInventoryInput(map[string]any{"discover": value}); err == nil {
				t.Fatal("accepted invalid discovery")
			}
		})
	}
	if _, _, err := webInventoryInput(map[string]any{"discover": valid, "urls": []any{"https://public.example/"}}); err == nil {
		t.Fatal("accepted two modes")
	}
}

func TestWebInventoryDiscoveryKeepsBoundedHTTPBudget(t *testing.T) {
	for i := 0; i < 3; i++ {
		release, err := acquireWebInventorySlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	s := inventoryTestSession(t, `process.stdout.write('{}');`)
	ctx, cancel := context.WithTimeout(s.ctx, 40*time.Millisecond)
	defer cancel()
	s.ctx = ctx
	_, err := s.readWebInventory(map[string]any{"discover": map[string]any{"sources": []any{map[string]any{"kind": "SITEMAP", "url": "https://public.example/sitemap.xml"}}, "allowedOrigins": []any{"https://public.example"}}})
	if err == nil || !strings.Contains(err.Error(), "shared HTTP inventory budget") {
		t.Fatalf("discovery bypassed shared budget: %v", err)
	}
}

func TestWebInventoryFencedMissingScratchFailsClosed(t *testing.T) {
	s := inventoryTestSession(t, `import fs from 'node:fs';fs.writeFileSync('started','yes');process.stdout.write('{}');`)
	s.permissionsProfile = "tomako-brand-fence"
	if _, err := s.readWebInventory(map[string]any{"urls": []any{"https://example.com"}}); err == nil {
		t.Fatal("fenced inventory started without an authorized scratch directory")
	}
	if _, err := os.Stat(filepath.Join(s.workDir, "started")); !os.IsNotExist(err) {
		t.Fatal("reader process started without an authorized scratch directory")
	}
}

func TestWebInventoryFencedInstalledReaderIntegration(t *testing.T) {
	script := os.Getenv("CC_WEB_INVENTORY_SCRIPT")
	if script == "" {
		t.Skip("set CC_WEB_INVENTORY_SCRIPT to test the installed Skills reader")
	}
	s := inventoryTestSession(t, "")
	s.extraEnv = []string{"SKILLS_OL_DIR=" + filepath.Dir(script)}
	s.permissionsProfile = "tomako-brand-fence"
	s.sandboxScratchDir = filepath.Join(s.workDir, ".tmp", "tasks", "own")
	sibling := filepath.Join(s.workDir, ".tmp", "tasks", "sibling")
	content := []byte(`{"version":1,"shallow":true,"origin":"https://example.com","urls":["https://example.com/synthetic"],"rows":[{"index":0,"url":"https://example.com/synthetic","status":"ok","title":"Synthetic receipt"}],"remaining":[]}`)
	for _, dir := range []string{s.sandboxScratchDir, sibling} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "receipt.json"), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	input := func(path string) map[string]any {
		return map[string]any{"receipt": map[string]any{"path": path, "sha256": fmt.Sprintf("%x", sha256.Sum256(content))}}
	}
	if _, err := s.readWebInventory(input(filepath.Join(s.sandboxScratchDir, "receipt.json"))); err != nil {
		t.Fatalf("same-session receipt was rejected: %v", err)
	}
	if _, err := s.readWebInventory(input(filepath.Join(sibling, "receipt.json"))); err == nil {
		t.Fatal("trusted reader returned another session's receipt")
	}
	alias := filepath.Join(s.sandboxScratchDir, "sibling-alias.json")
	if err := os.Symlink(filepath.Join(sibling, "receipt.json"), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := s.readWebInventory(input(alias)); err == nil {
		t.Fatal("trusted reader followed a sibling receipt alias")
	}
}

func TestWebInventoryFencedReaderUsesAuthorizedWorkingDirectoryAndInputDescriptor(t *testing.T) {
	t.Setenv("TOMAKO_WEB_INVENTORY_HOST_IO", "untrusted-parent")
	s := inventoryTestSession(t, `import fs from 'node:fs';
 const body=fs.readFileSync(Number(process.env.TOMAKO_WEB_INVENTORY_INPUT_FD),'utf8');
 fs.writeFileSync('receipt.json',body,{flag:'wx',mode:0o600});
 process.stdout.write(JSON.stringify({host:process.env.TOMAKO_WEB_INVENTORY_HOST_IO,source:JSON.parse(body),cwd:fs.realpathSync(process.cwd())}));`)
	s.permissionsProfile = "tomako-brand-fence"
	s.sandboxScratchDir = filepath.Join(s.workDir, ".tmp", "tasks", "own")
	if err := os.MkdirAll(s.sandboxScratchDir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(s.sandboxScratchDir, "urls.json")
	body := `["https://example.com/one"]`
	if err := os.WriteFile(source, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := s.readWebInventory(map[string]any{"sourceFile": map[string]any{"path": source}})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Host, Cwd string
		Source    []string
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	actual, _ := filepath.EvalSymlinks(s.sandboxScratchDir)
	if result.Host != "1" || result.Cwd != actual || len(result.Source) != 1 {
		t.Fatalf("incorrect scoped reader context: %s", out)
	}
	data, err := os.ReadFile(filepath.Join(s.sandboxScratchDir, "receipt.json"))
	if err != nil || string(data) != body {
		t.Fatalf("receipt roundtrip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.workDir, "receipt.json")); !os.IsNotExist(err) {
		t.Fatal("receipt written outside session directory")
	}
}

// Stub child processes hold their acquired budgets; no public HTTP is requested.
func TestWebInventoryThreeBatchesDoNotStarveSinglePageReader(t *testing.T) {
	releaseReaders, err := acquireWebReadSlots(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	readersHeld := true
	defer func() {
		if readersHeld {
			releaseReaders()
		}
	}()
	sessions := make([]*appServerSession, 4)
	done := make([]chan error, 4)
	for i := range sessions {
		sessions[i] = inventoryTestSession(t, `import fs from 'node:fs';fs.writeFileSync('started','yes');setInterval(()=>{},1000);`)
		done[i] = make(chan error, 1)
	}
	start := func(i int) {
		go func() {
			_, err := sessions[i].readWebInventory(map[string]any{"urls": []any{"https://example.com/"}})
			done[i] <- err
		}()
	}
	started := func(i int) bool { _, err := os.Stat(filepath.Join(sessions[i].workDir, "started")); return err == nil }
	waitStart := func(i int) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for !started(i) {
			if time.Now().After(until) {
				t.Fatalf("batch %d did not start concurrently", i)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for i := 0; i < 3; i++ {
		start(i)
	}
	for i := 0; i < 3; i++ {
		waitStart(i)
	}
	releaseReaders()
	readersHeld = false
	start(3)
	time.Sleep(80 * time.Millisecond)
	if started(3) {
		t.Fatal("more than three two-worker inventory batches admitted")
	}
	reader := webReadTestSession(t, `process.stdout.write('reader-ok')`)
	readerCtx, cancel := context.WithTimeout(reader.ctx, time.Second)
	defer cancel()
	reader.ctx = readerCtx
	if out, err := reader.readWebPage(map[string]any{"url": "https://example.com/"}); err != nil || out != "reader-ok" {
		t.Fatalf("inventory starved single-page reader: %v %q", err, out)
	}
	sessions[0].cancel()
	waitStart(3)
	for _, s := range sessions {
		s.cancel()
	}
	for i := range done {
		select {
		case err := <-done[i]:
			if err == nil {
				t.Fatal("cancelled stub unexpectedly succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled stub did not release")
		}
	}
	if len(webInventorySlots) != 0 || len(webReadSlots) != 0 {
		t.Fatal("cancelled calls leaked a budget")
	}
}
