package codex

import (
	"context"
	"encoding/json"
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
		if len(webReadSlots) != 0 {
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
	if len(webReadSlots) != 0 || len(webReadAdmission) != 0 {
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
