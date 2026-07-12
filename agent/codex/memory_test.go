package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestResolveSessionWorkDirUsesSessionWorkspaceBase(t *testing.T) {
	got := resolveSessionWorkDir("/fallback", "/tmp/user-workspaces", "java-backend:cibos:42")
	want := filepath.Join("/tmp/user-workspaces", "java-backend_cibos_42")
	if got != want {
		t.Fatalf("resolveSessionWorkDir() = %q, want %q", got, want)
	}
}

func TestWriteMemoryFactsPrefersEngineWorkDir(t *testing.T) {
	base := t.TempDir()
	userDir := filepath.Join(base, "user-407382056")
	a := &Agent{workDir: "/fallback", sessionWorkspaceBase: filepath.Join(base, "wrong-base")}

	result, err := a.WriteMemoryFacts(context.Background(), core.AgentMemoryWriteRequest{
		SessionKey:   "java-backend:cibos:407382056",
		WorkDir:      userDir,
		SourceTaskID: "llm-abc",
		Title:        "brand_analysis",
		Facts: []core.AgentMemoryFact{
			{Type: "brand_name", Value: "Tomako"},
		},
	})
	if err != nil {
		t.Fatalf("WriteMemoryFacts() error = %v", err)
	}
	wantPrefix := filepath.Join(userDir, ".codex", "memories", "extensions", "tomako", "facts")
	if !strings.HasPrefix(result.File, wantPrefix+string(os.PathSeparator)) {
		t.Fatalf("fact file = %q, want prefix %q", result.File, wantPrefix)
	}
}

func TestWriteMemoryFactsWritesTomakoExtensionFile(t *testing.T) {
	base := t.TempDir()
	a := &Agent{workDir: "/fallback", sessionWorkspaceBase: base}

	result, err := a.WriteMemoryFacts(context.Background(), core.AgentMemoryWriteRequest{
		SessionKey:   "java-backend:cibos:42",
		SourceTaskID: "llm-abc",
		Title:        "brand_analysis",
		Facts: []core.AgentMemoryFact{
			{Type: "brand_name", Value: "Tomako"},
			{Type: "target_audience", Value: "builders"},
		},
	})
	if err != nil {
		t.Fatalf("WriteMemoryFacts() error = %v", err)
	}
	if result == nil || result.File == "" {
		t.Fatalf("WriteMemoryFacts() result file is empty")
	}

	wantPrefix := filepath.Join(base, "java-backend_cibos_42", ".codex", "memories", "extensions", "tomako", "facts")
	if !strings.HasPrefix(result.File, wantPrefix+string(os.PathSeparator)) {
		t.Fatalf("fact file = %q, want prefix %q", result.File, wantPrefix)
	}
	content, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatalf("read fact file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "- brand_name: Tomako") || !strings.Contains(text, "- target_audience: builders") {
		t.Fatalf("fact file content missing facts:\n%s", text)
	}
	if _, err := os.Stat(filepath.Join(base, "java-backend_cibos_42", ".codex", "memories", "extensions", "tomako", "instructions.md")); err != nil {
		t.Fatalf("instructions.md not written: %v", err)
	}
}

func TestWriteMemoryFactsCustomExtension(t *testing.T) {
	base := t.TempDir()
	userDir := filepath.Join(base, "operator-7")
	a := &Agent{
		workDir:            "/fallback",
		memoryExtension:    "factory-sched",
		memoryFactTitle:    "Factory Fact",
		memoryInstructions: "# Factory Schedule Facts\n",
	}
	result, err := a.WriteMemoryFacts(context.Background(), core.AgentMemoryWriteRequest{
		SessionKey:   "bridge:mes:7",
		WorkDir:      userDir,
		SourceTaskID: "job-1",
		Title:        "line_status",
		Facts: []core.AgentMemoryFact{
			{Type: "line", Value: "A1"},
		},
	})
	if err != nil {
		t.Fatalf("WriteMemoryFacts() error = %v", err)
	}
	wantPrefix := filepath.Join(userDir, ".codex", "memories", "extensions", "factory-sched", "facts")
	if !strings.HasPrefix(result.File, wantPrefix+string(os.PathSeparator)) {
		t.Fatalf("fact file = %q, want prefix %q", result.File, wantPrefix)
	}
	body, err := os.ReadFile(filepath.Join(userDir, ".codex", "memories", "extensions", "factory-sched", "instructions.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Factory Schedule Facts") {
		t.Fatalf("instructions = %q", body)
	}
}

func TestMemoryFactCRUD(t *testing.T) {
	userDir := t.TempDir()
	a := &Agent{workDir: "/fallback"}
	sessionKey := "java-backend:cibos:99"

	written, err := a.WriteMemoryFacts(context.Background(), core.AgentMemoryWriteRequest{
		SessionKey:   sessionKey,
		WorkDir:      userDir,
		SourceTaskID: "llm-1",
		Title:        "brand",
		Facts:        []core.AgentMemoryFact{{Type: "brand_name", Value: "Tomako"}},
	})
	if err != nil {
		t.Fatalf("WriteMemoryFacts() error = %v", err)
	}
	if written.Name == "" {
		t.Fatal("WriteMemoryFacts() name is empty")
	}

	listed, err := a.ListMemoryFacts(context.Background(), core.AgentMemoryListRequest{
		SessionKey: sessionKey,
		WorkDir:    userDir,
	})
	if err != nil {
		t.Fatalf("ListMemoryFacts() error = %v", err)
	}
	if len(listed.Facts) != 1 || listed.Facts[0].Name != written.Name {
		t.Fatalf("ListMemoryFacts() = %+v, want [%s]", listed.Facts, written.Name)
	}

	got, err := a.GetMemoryFact(context.Background(), core.AgentMemoryGetRequest{
		SessionKey: sessionKey,
		WorkDir:    userDir,
		Name:       written.Name,
	})
	if err != nil {
		t.Fatalf("GetMemoryFact() error = %v", err)
	}
	if !strings.Contains(got.Content, "brand_name: Tomako") {
		t.Fatalf("GetMemoryFact() content = %q", got.Content)
	}

	updated, err := a.UpdateMemoryFact(context.Background(), core.AgentMemoryUpdateRequest{
		SessionKey: sessionKey,
		WorkDir:    userDir,
		Name:       written.Name,
		Content:    "# Edited\n\n- brand_name: Foldos\n",
	})
	if err != nil {
		t.Fatalf("UpdateMemoryFact() error = %v", err)
	}
	if !strings.Contains(updated.Content, "brand_name: Foldos") {
		t.Fatalf("UpdateMemoryFact() content = %q", updated.Content)
	}

	if err := a.DeleteMemoryFact(context.Background(), core.AgentMemoryDeleteRequest{
		SessionKey: sessionKey,
		WorkDir:    userDir,
		Name:       written.Name,
	}); err != nil {
		t.Fatalf("DeleteMemoryFact() error = %v", err)
	}
	if _, err := a.GetMemoryFact(context.Background(), core.AgentMemoryGetRequest{
		SessionKey: sessionKey,
		WorkDir:    userDir,
		Name:       written.Name,
	}); err == nil {
		t.Fatal("GetMemoryFact() after delete should fail")
	}
}

func TestSafeFactPathRejectsTraversal(t *testing.T) {
	factsDir := t.TempDir()
	if _, err := safeFactPath(factsDir, "../escape.md"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := safeFactPath(factsDir, "ok.md"); err != nil {
		t.Fatalf("safeFactPath(ok.md) error = %v", err)
	}
}
