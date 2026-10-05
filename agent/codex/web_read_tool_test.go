package codex

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func webReadTestSession(t *testing.T, reader string) *appServerSession {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "web-read.mjs"), []byte(reader), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &appServerSession{ctx: ctx, cancel: cancel, workDir: dir, events: make(chan core.Event, 4),
		extraEnv: []string{"SKILLS_OL_DIR=" + dir, "TOMAKO_IMAGE_CAPABILITY_TOKEN=session-capability"},
		runtime:  core.SessionRuntime{TaskID: "cmsg-a", WorkspaceID: "ws-a", BrandID: "b-a", ChatSessionID: "csess-a"}}
	s.alive.Store(true)
	return s
}

func advertisesWebRead(s *appServerSession) bool {
	tools, _ := s.threadRequestParams()["dynamicTools"].([]map[string]any)
	for _, tool := range tools {
		if tool["name"] == webReadToolName {
			return true
		}
	}
	return false
}

func TestWebReadToolOfferedToConversationsOnlyWhenTheReaderIsInstalled(t *testing.T) {
	s := webReadTestSession(t, "")
	if !advertisesWebRead(s) {
		t.Fatal("a conversation with the reader installed did not get the tool")
	}
	s.runtime.ChatSessionID = ""
	if advertisesWebRead(s) {
		t.Fatal("a background task without a conversation received the tool")
	}
	s.runtime.ChatSessionID = "csess-a"
	s.runtime.Scene = "brand_analysis"
	if advertisesWebRead(s) {
		t.Fatal("the dedicated brand-analysis tool set changed")
	}
	s.runtime.Scene = ""
	if err := os.Remove(filepath.Join(s.workDir, "web-read.mjs")); err != nil {
		t.Fatal(err)
	}
	if advertisesWebRead(s) {
		t.Fatal("the tool was offered before Skills-OL installed the reader")
	}
}

func TestWebReadToolRefusesAddressesThatAreNotPublicPages(t *testing.T) {
	s := webReadTestSession(t, "")
	for _, address := range []string{"http://localhost:3000/admin", "https://10.0.0.8/", "file:///etc/passwd",
		"https://user:secret@example.com/", "ftp://example.com/file", "https://intranet/"} {
		if _, err := s.readWebPage(map[string]any{"url": address}); err == nil {
			t.Fatalf("%s was accepted", address)
		}
	}
}

func TestWebReadToolPassesBoundedArgumentsAndKeepsSecretsFromTheReader(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	t.Setenv("DEEPSEEK_API_KEY", "provider-secret")
	s := webReadTestSession(t, `process.stdout.write(JSON.stringify({args: process.argv.slice(2),
  provider: process.env.DEEPSEEK_API_KEY || "", capability: process.env.TOMAKO_IMAGE_CAPABILITY_TOKEN || ""}));`)
	output, err := s.readWebPage(map[string]any{"url": "https://example.com/pricing", "start": float64(1200), "render": true})
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Args       []string `json:"args"`
		Provider   string   `json:"provider"`
		Capability string   `json:"capability"`
	}
	if err := json.Unmarshal([]byte(output), &seen); err != nil {
		t.Fatalf("output=%q: %v", output, err)
	}
	if strings.Join(seen.Args, " ") != "https://example.com/pricing --format=agent --start=1200 --render=always" {
		t.Fatalf("args=%v", seen.Args)
	}
	if seen.Provider != "" || seen.Capability != "" {
		t.Fatalf("the reader received credentials: %+v", seen)
	}
	if _, err := s.readWebPage(map[string]any{"url": "https://example.com/", "start": float64(-5)}); err != nil {
		t.Fatal(err)
	}
}

func TestWebReadToolReportsAReaderFailureInsteadOfAnEmptyPage(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	s := webReadTestSession(t, `process.stderr.write("browser could not start"); process.exit(3);`)
	_, err := s.readWebPage(map[string]any{"url": "https://example.com/pricing"})
	if err == nil || !strings.Contains(err.Error(), "browser could not start") {
		t.Fatalf("err=%v", err)
	}
}

func TestWebReadToolShowsThePageAddressInTheConversation(t *testing.T) {
	s := webReadTestSession(t, "")
	s.handleItemStarted(map[string]any{"type": "dynamicToolCall", "id": "item-1", "tool": webReadToolName,
		"arguments": map[string]any{"url": "https://example.com/pricing?session=secret#plans"}})
	event := <-s.events
	if event.PublicActivity == nil || event.PublicActivity.Kind != "open_page" || event.PublicActivity.Status != "running" {
		t.Fatalf("activity=%+v", event.PublicActivity)
	}
	if event.PublicActivity.URL != "" {
		t.Fatalf("a query-bearing address was shown: %q", event.PublicActivity.URL)
	}
	s.handleItemCompleted(map[string]any{"type": "dynamicToolCall", "id": "item-1", "tool": webReadToolName, "status": "completed",
		"arguments": `{"url":"https://example.com/pricing"}`, "contentItems": []any{}})
	event = <-s.events
	if event.PublicActivity == nil || event.PublicActivity.URL != "https://example.com/pricing" || event.PublicActivity.Status != "returned" {
		t.Fatalf("activity=%+v", event.PublicActivity)
	}
}
