package core

import (
	"context"
	"encoding/json"
	"io"
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
	return &AgentMemoryWriteResult{File: "/tmp/fact.md", Name: "fact.md"}, nil
}

func (a *memoryWriterAgent) GetWorkDir() string { return "/project/work" }

type memoryManagerAgent struct {
	memoryWriterAgent
	listed   AgentMemoryListRequest
	got      AgentMemoryGetRequest
	updated  AgentMemoryUpdateRequest
	deleted  AgentMemoryDeleteRequest
	listResp *AgentMemoryListResult
	getResp  *AgentMemoryFactFile
}

func (a *memoryManagerAgent) ListMemoryFacts(_ context.Context, req AgentMemoryListRequest) (*AgentMemoryListResult, error) {
	a.listed = req
	if a.listResp != nil {
		return a.listResp, nil
	}
	return &AgentMemoryListResult{
		SessionKey: req.SessionKey,
		WorkDir:    req.WorkDir,
		FactsDir:   filepath.Join(req.WorkDir, ".codex/memories/extensions/tomako/facts"),
		Facts:      []AgentMemoryFactMeta{{Name: "a.md", Size: 12, ModTime: "2026-07-12T00:00:00Z"}},
	}, nil
}

func (a *memoryManagerAgent) GetMemoryFact(_ context.Context, req AgentMemoryGetRequest) (*AgentMemoryFactFile, error) {
	a.got = req
	if a.getResp != nil {
		return a.getResp, nil
	}
	return &AgentMemoryFactFile{Name: req.Name, Content: "# hello\n", Size: 8}, nil
}

func (a *memoryManagerAgent) UpdateMemoryFact(_ context.Context, req AgentMemoryUpdateRequest) (*AgentMemoryFactFile, error) {
	a.updated = req
	return &AgentMemoryFactFile{Name: req.Name, Content: req.Content, Size: int64(len(req.Content))}, nil
}

func (a *memoryManagerAgent) DeleteMemoryFact(_ context.Context, req AgentMemoryDeleteRequest) error {
	a.deleted = req
	return nil
}

func decodeMgmtData(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OK    bool           `json:"ok"`
		Data  map[string]any `json:"data"`
		Error string         `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode response: %v\nbody=%s", err, body)
	}
	if !envelope.OK {
		t.Fatalf("ok=false error=%s body=%s", envelope.Error, body)
	}
	return envelope.Data
}

// samePath compares filesystem paths after symlink resolution (macOS /var).
func samePath(t *testing.T, got, want string) bool {
	t.Helper()
	gotN := normalizeWorkspacePath(got)
	wantN := normalizeWorkspacePath(want)
	return gotN == wantN
}

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
	if !samePath(t, agent.last.WorkDir, wantDir) {
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

func TestMgmt_ProjectMemoryFactsListGetUpdateDelete(t *testing.T) {
	base := t.TempDir()
	agent := &memoryManagerAgent{}
	e := NewEngine("proj", agent, nil, "", LangEnglish)
	e.SetMultiWorkspace(base, filepath.Join(t.TempDir(), "bindings.json"))
	e.SetWorkspaceIdleTimeout(0)

	mgmt := NewManagementServer(0, "tok", nil)
	mgmt.RegisterEngine("proj", e)
	ts := httptest.NewServer(mgmt.buildHandler(http.NewServeMux()))
	defer ts.Close()

	sessionKey := "java-backend:cibos:88"
	wantDir := filepath.Join(base, "user-88")
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
	}

	listReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/projects/proj/memory/facts?session_key="+sessionKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	auth(listReq)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	listData := decodeMgmtData(t, listResp)
	if agent.listed.WorkDir != wantDir {
		t.Fatalf("list WorkDir = %q, want %q", agent.listed.WorkDir, wantDir)
	}
	if listData["session_key"] != sessionKey {
		t.Fatalf("list session_key = %#v", listData["session_key"])
	}

	getReq, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/projects/proj/memory/facts/brand.md?session_key="+sessionKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	auth(getReq)
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d", getResp.StatusCode)
	}
	_ = decodeMgmtData(t, getResp)
	if agent.got.Name != "brand.md" || agent.got.WorkDir != wantDir {
		t.Fatalf("get req = %+v", agent.got)
	}

	updateBody := `{"session_key":"java-backend:cibos:88","content":"# Edited\n"}`
	putReq, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/projects/proj/memory/facts/brand.md", strings.NewReader(updateBody))
	if err != nil {
		t.Fatal(err)
	}
	auth(putReq)
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	defer putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("put status = %d", putResp.StatusCode)
	}
	_ = decodeMgmtData(t, putResp)
	if agent.updated.Name != "brand.md" || agent.updated.WorkDir != wantDir || !strings.Contains(agent.updated.Content, "Edited") {
		t.Fatalf("update req = %+v", agent.updated)
	}

	delReq, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/projects/proj/memory/facts/brand.md?session_key="+sessionKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	auth(delReq)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	defer delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d", delResp.StatusCode)
	}
	delData := decodeMgmtData(t, delResp)
	if delData["deleted"] != true || delData["name"] != "brand.md" {
		t.Fatalf("delete data = %#v", delData)
	}
	if agent.deleted.Name != "brand.md" || agent.deleted.WorkDir != wantDir {
		t.Fatalf("delete req = %+v", agent.deleted)
	}
}

func TestMgmt_ProjectMemoryFactsRequiresSessionKey(t *testing.T) {
	agent := &memoryManagerAgent{}
	e := NewEngine("proj", agent, nil, "", LangEnglish)
	mgmt := NewManagementServer(0, "tok", nil)
	mgmt.RegisterEngine("proj", e)
	ts := httptest.NewServer(mgmt.buildHandler(http.NewServeMux()))
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/projects/proj/memory/facts", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
