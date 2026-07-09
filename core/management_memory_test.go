package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type memoryWriterAgent struct {
	stubAgent
	last AgentMemoryWriteRequest
}

func (a *memoryWriterAgent) WriteMemoryFacts(_ context.Context, req AgentMemoryWriteRequest) (*AgentMemoryWriteResult, error) {
	a.last = req
	return &AgentMemoryWriteResult{File: "/tmp/fact.md"}, nil
}

func (a *memoryWriterAgent) GetWorkDir() string { return "/project/work" }

func TestMgmt_ProjectMemoryFactsWritesThroughAgent(t *testing.T) {
	agent := &memoryWriterAgent{}
	e := NewEngine("proj", agent, nil, "", LangEnglish)
	mgmt := NewManagementServer(0, "tok", nil)
	mgmt.RegisterEngine("proj", e)
	ts := httptest.NewServer(mgmt.buildHandler(http.NewServeMux()))
	defer ts.Close()

	body := `{"session_key":"java-backend:cibos:42","source_task_id":"llm-1","title":"brand","facts":[{"type":"brand_name","value":"Tomako"}]}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/proj/memory/facts", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if agent.last.SessionKey != "java-backend:cibos:42" || len(agent.last.Facts) != 1 || agent.last.Facts[0].Value != "Tomako" {
		t.Fatalf("agent request = %+v", agent.last)
	}
	if agent.last.WorkDir != "/project/work" {
		t.Fatalf("WorkDir = %q, want project work_dir fallback", agent.last.WorkDir)
	}
}

func TestMgmt_ProjectMemoryFactsUsesMultiWorkspaceUserDir(t *testing.T) {
	base := t.TempDir()
	agent := &memoryWriterAgent{}
	e := NewEngine("proj", agent, nil, "", LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetWorkspaceIdleTimeout(0)

	mgmt := NewManagementServer(0, "tok", nil)
	mgmt.RegisterEngine("proj", e)
	ts := httptest.NewServer(mgmt.buildHandler(http.NewServeMux()))
	defer ts.Close()

	body := `{"session_key":"java-backend:cibos:407382056","source_task_id":"llm-1","title":"brand","facts":[{"type":"brand_name","value":"Tomako"}]}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/projects/proj/memory/facts", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	wantDir := filepath.Join(base, "user-407382056")
	if agent.last.WorkDir != wantDir {
		t.Fatalf("WorkDir = %q, want %q", agent.last.WorkDir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, ".codex")); err != nil {
		t.Fatalf("expected user workspace .codex dir: %v", err)
	}
}

func TestResolveMemoryWorkDir_MultiWorkspace(t *testing.T) {
	base := t.TempDir()
	agent := &memoryWriterAgent{}
	e := NewEngine("proj", agent, nil, "", LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetWorkspaceIdleTimeout(0)

	got, err := e.ResolveMemoryWorkDir("java-backend:cibos:42")
	if err != nil {
		t.Fatalf("ResolveMemoryWorkDir() error = %v", err)
	}
	want := filepath.Join(base, "user-42")
	if got != want {
		t.Fatalf("ResolveMemoryWorkDir() = %q, want %q", got, want)
	}
}

func TestResolveMemoryWorkDir_RejectsMissingUser(t *testing.T) {
	base := t.TempDir()
	agent := &memoryWriterAgent{}
	e := NewEngine("proj", agent, nil, "", LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetWorkspaceIdleTimeout(0)

	if _, err := e.ResolveMemoryWorkDir("java-backend"); err == nil {
		t.Fatal("expected error for session_key without user id")
	}
}
