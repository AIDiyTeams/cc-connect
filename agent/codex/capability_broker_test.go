package codex

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

type brokerRoundTrip func(*http.Request) (*http.Response, error)

func (f brokerRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func brokerFixture(t *testing.T) (*capabilityBroker, core.SessionRuntime) {
	t.Helper()
	dir := t.TempDir()
	s := &appServerSession{taskRuntimeEnvFile: filepath.Join(dir, "machine.env")}
	s.alive.Store(true)
	runtime := core.SessionRuntime{TaskID: "cmsg-task-a", WorkspaceID: "ws-a", BrandID: "brand-a",
		GatewayModel: "test-routing/selected-model", ReasoningEffort: "medium",
		ImageCapabilityToken: "synthetic-image-secret-4631", DocumentCapabilityToken: "synthetic-doc-secret-7913",
		EmployeeCommandCapabilityToken: "synthetic-employee-secret-1589", ProductUpdateCapabilityToken: "synthetic-product-secret-6023"}
	b := &capabilityBroker{session: s, origin: "https://test.tomako.ai", socket: filepath.Join(dir, "cap.sock")}
	s.capabilityBroker = b
	if _, err := b.writeRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	s.runtime = runtime
	return b, runtime
}

func brokerInput(b *capabilityBroker) brokerRequest {
	return brokerRequest{Revision: b.revision, URL: b.origin + "/api/workspaces/ws-a/products/brand-a/content-documents/agent-operations",
		Method: "POST", CapabilityKind: "document", ContentType: "application/json", BodyBase64: base64.StdEncoding.EncodeToString([]byte(`{"action":"read","documentId":"doc-own"}`))}
}

func invokeBroker(t *testing.T, b *capabilityBroker, input brokerRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(input)
	w := httptest.NewRecorder()
	b.serve(w, httptest.NewRequest(http.MethodPost, "/v1/portal", bytes.NewReader(data)))
	return w
}

func TestCapabilityBrokerKeepsCredentialsOutOfRuntimeFiles(t *testing.T) {
	b, runtime := brokerFixture(t)
	data, err := os.ReadFile(b.session.taskRuntimeEnvFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{runtime.ImageCapabilityToken, runtime.DocumentCapabilityToken, runtime.EmployeeCommandCapabilityToken, runtime.ProductUpdateCapabilityToken} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("runtime file contains a bearer")
		}
	}
	for _, value := range []string{brokerHandlePrefix + "image", brokerHandlePrefix + "document", brokerHandlePrefix + "employee", brokerHandlePrefix + "product", "TOMAKO_CAPABILITY_SOCKET", b.revision} {
		if !bytes.Contains(data, []byte(value)) {
			t.Fatalf("missing non-secret runtime field %s", value)
		}
	}
}

func TestCapabilityBrokerPreservesReceiptAndInjectsCurrentAuthority(t *testing.T) {
	b, runtime := brokerFixture(t)
	receipt := `{"code":0,"data":{"documentId":"doc-own","content":{"text":"model/Skill/收入是用户自己的正常内容"}}}`
	b.client = &http.Client{Transport: brokerRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Tomako-Machine-Capability") != runtime.DocumentCapabilityToken || len(r.Header.Get("X-Tomako-Machine-Nonce")) != 32 {
			t.Fatal("missing trusted authority")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"action":"read","documentId":"doc-own"}` {
			t.Fatal("business payload changed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(receipt)), Header: http.Header{"Set-Cookie": []string{"private=secret"}}}, nil
	})}
	w := invokeBroker(t, b, brokerInput(b))
	if w.Code != 200 || w.Body.String() != receipt || w.Header().Get("Set-Cookie") != "" {
		t.Fatalf("public receipt changed: %d", w.Code)
	}
}

func TestCapabilityBrokerRejectsCrossScopeAndUnregisteredTransport(t *testing.T) {
	b, _ := brokerFixture(t)
	b.client = &http.Client{Transport: brokerRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("forbidden request reached upstream")
		return nil, nil
	})}
	tests := map[string]func(*brokerRequest){
		"sibling-workspace": func(i *brokerRequest) { i.URL = strings.Replace(i.URL, "/ws-a/", "/ws-b/", 1) },
		"sibling-brand":     func(i *brokerRequest) { i.URL = strings.Replace(i.URL, "/brand-a/", "/brand-b/", 1) },
		"arbitrary-origin":  func(i *brokerRequest) { i.URL = strings.Replace(i.URL, b.origin, "https://attacker.invalid", 1) },
		"production-origin": func(i *brokerRequest) { i.URL = strings.Replace(i.URL, b.origin, "https://tomako.ai", 1) },
		"userinfo":          func(i *brokerRequest) { i.URL = strings.Replace(i.URL, "https://", "https://user@", 1) },
		"fragment":          func(i *brokerRequest) { i.URL += "#secret" },
		"dot-traversal": func(i *brokerRequest) {
			i.URL = b.origin + "/api/workspaces/ws-a/products/brand-a/../brand-b/content-documents/agent-operations"
		},
		"encoded-slash":    func(i *brokerRequest) { i.URL = strings.Replace(i.URL, "/ws-a/", "/ws-a%2f../", 1) },
		"double-slash":     func(i *brokerRequest) { i.URL = strings.Replace(i.URL, "/products/", "//products/", 1) },
		"header-injection": func(i *brokerRequest) { i.ContentType = "application/json\r\nAuthorization: evil" },
		"admin-route": func(i *brokerRequest) {
			i.URL = b.origin + "/api/metadata/domain-graph"
			i.Method = "GET"
			i.BodyBase64 = ""
		},
		"unknown-scoped-route": func(i *brokerRequest) { i.URL = b.origin + "/api/workspaces/ws-a/products/brand-a/admin/export" },
		"wrong-kind":           func(i *brokerRequest) { i.CapabilityKind = "image" },
		"invented-kind":        func(i *brokerRequest) { i.CapabilityKind = "admin" },
		"missing-kind":         func(i *brokerRequest) { i.CapabilityKind = "" },
		"missing-generation":   func(i *brokerRequest) { i.Revision = "" },
		"sibling-generation":   func(i *brokerRequest) { i.Revision = "another-session" },
		"delete":               func(i *brokerRequest) { i.Method = "DELETE" },
		"get-with-body":        func(i *brokerRequest) { i.Method = "GET" },
		"broken-body":          func(i *brokerRequest) { i.BodyBase64 = "not base64!" },
		"read-attach": func(i *brokerRequest) {
			i.URL = b.origin + "/api/workspaces/ws-a/products/brand-a/images/img-own/attach"
			i.CapabilityKind = "image"
			i.Method = "GET"
			i.BodyBase64 = ""
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			input := brokerInput(b)
			change(&input)
			w := invokeBroker(t, b, input)
			if w.Code < 400 || strings.Contains(w.Body.String(), "synthetic-") {
				t.Fatalf("unsafe rejection: %d", w.Code)
			}
		})
	}
}

func TestCapabilityBrokerRevokesOldTaskAndClosedSession(t *testing.T) {
	b, runtime := brokerFixture(t)
	old := brokerInput(b)
	runtime.TaskID = "cmsg-task-b"
	b.session.runtimeMu.Lock()
	_, err := b.writeRuntime(runtime)
	b.session.runtime = runtime
	b.session.runtimeMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if w := invokeBroker(t, b, old); w.Code != 403 {
		t.Fatal("previous task authority survived rotation")
	}
	b.session.alive.Store(false)
	if w := invokeBroker(t, b, brokerInput(b)); w.Code != 403 {
		t.Fatal("closed session retained authority")
	}
}

func TestCapabilityBrokerDoesNotReflectCredentialOrRedirect(t *testing.T) {
	for _, status := range []int{200, 302} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			b, runtime := brokerFixture(t)
			b.client = &http.Client{Transport: brokerRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Location": []string{"https://attacker.invalid"}}, Body: io.NopCloser(strings.NewReader(`{"debug":"` + runtime.DocumentCapabilityToken + `"}`))}, nil
			})}
			w := invokeBroker(t, b, brokerInput(b))
			if w.Code != 502 || strings.Contains(w.Body.String(), runtime.DocumentCapabilityToken) || w.Header().Get("Location") != "" {
				t.Fatal("credential or redirect escaped")
			}
		})
	}
}

func TestCapabilityBrokerPreservesExistingScopedRouteFamilies(t *testing.T) {
	b, runtime := brokerFixture(t)
	prefix := b.origin + "/api/workspaces/ws-a/products/brand-a"
	cases := []struct{ kind, method, path string }{
		{"image", "POST", "/images/generate"}, {"image", "GET", "/images/img-one/status"}, {"image", "POST", "/images/reference-upload"},
		{"image", "POST", "/images/img-one/attach"},
		{"image", "POST", "/visual-assets/video-upload"}, {"image", "GET", "/agent-context?section=brand"},
		{"product", "PATCH", "/agent-update"}, {"image", "POST", "/cmo/reception"},
		{"employee", "GET", "/employees/moki/capabilities/content.operations"}, {"employee", "POST", "/employees/moki/commands"},
		{"employee", "GET", "/employees/moki/capabilities/content.operations/versions"},
		{"employee", "POST", "/employees/moki/capabilities/content.operations/versions/v2/restore"},
		{"image", "POST", "/images/batch-reservation"}, {"image", "POST", "/images/batch-reservation/settle"},
		{"document", "PUT", "/chat/agent-reply-checklists"}, {"document", "GET", "/chat/agent-reply-checklists"},
		{"image", "GET", "/external-data/discover?query=public"}, {"image", "POST", "/external-data/execute"},
		{"image", "GET", "/backlinks?page=1"}, {"image", "POST", "/blog-seo-skill-reads"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			input := brokerRequest{URL: prefix + c.path, Method: c.method, CapabilityKind: c.kind}
			if _, err := validateBrokerRequest(input, nil, runtime, b.origin); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCapabilityBrokerHidesUpstreamFailureDiagnostics(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 429, 500, 502} {
		b, _ := brokerFixture(t)
		b.client = &http.Client{Transport: brokerRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"message":"provider route and stack trace synthetic-private"}`))}, nil
		})}
		w := invokeBroker(t, b, brokerInput(b))
		if w.Code != status || strings.Contains(w.Body.String(), "synthetic-private") || !strings.Contains(w.Body.String(), "CAPABILITY_UNAVAILABLE") {
			t.Fatalf("unsafe status %d", status)
		}
	}
}

func TestCapabilityBrokerPreservesOnlyPortalPublicErrorContract(t *testing.T) {
	const conflict = `{"code":409,"message":"Read the current document before continuing","data":{"currentRevision":8,"currentOffset":12,"pendingProposalRevision":9}}`
	cases := []struct {
		name   string
		status int
		marker string
		body   string
		allow  bool
	}{
		{"document-recovery", 409, "v1", conflict, true},
		{"public-validation", 400, "v1", `{"code":400,"message":"A required public field is missing"}`, true},
		{"unmarked", 409, "", conflict, false},
		{"unknown-version", 409, "v2", conflict, false},
		{"server-diagnostic", 500, "v1", `{"code":500,"message":"internal private stack"}`, false},
		{"unknown-field", 409, "v1", `{"code":409,"message":"conflict","stack":"private"}`, false},
		{"unknown-recovery", 409, "v1", `{"code":409,"message":"conflict","data":{"provider":"private"}}`, false},
		{"mismatched-status", 409, "v1", `{"code":400,"message":"conflict"}`, false},
		{"negative-offset", 409, "v1", `{"code":409,"message":"conflict","data":{"currentOffset":-1}}`, false},
		{"blank-message", 409, "v1", `{"code":409,"message":" "}`, false},
		{"oversized-message", 409, "v1", `{"code":409,"message":"` + strings.Repeat("x", 1025) + `"}`, false},
		{"trailing-json", 409, "v1", conflict + `{}`, false},
		{"reflected-secret", 409, "v1", `{"code":409,"message":"synthetic-doc-secret-7913"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := brokerFixture(t)
			b.client = &http.Client{Transport: brokerRoundTrip(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(c.body)),
					Header: http.Header{"X-Tomako-Public-Error": []string{c.marker}, "Set-Cookie": []string{"private=secret"}}}, nil
			})}
			w := invokeBroker(t, b, brokerInput(b))
			if c.allow {
				if w.Code != c.status || w.Body.String() != c.body {
					t.Fatalf("public recovery receipt changed: %d %s", w.Code, w.Body.String())
				}
			} else if !strings.Contains(w.Body.String(), "CAPABILITY_UNAVAILABLE") || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "synthetic-doc-secret") {
				t.Fatal("untrusted error escaped")
			}
			if w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Tomako-Public-Error") != "" {
				t.Fatal("upstream headers escaped")
			}
		})
	}
}

func TestCapabilityBrokerMetadataUsesDeclaredCurrentScope(t *testing.T) {
	b, runtime := brokerFixture(t)
	for _, capability := range []string{"tomako.generate-video", "tomako.read-fal-video", "tomako.list-backlink-tasks", "tomako.read-backlink-task", "tomako.read-backlink-mailbox", "tomako.list-backlink-approvals", "tomako.create-backlink-task", "tomako.update-backlink-task", "tomako.send-backlink-email", "tomako.verify-backlink-task", "tomako.reconcile-backlink-email", "tomako.sync-backlink-replies"} {
		body, _ := json.Marshal(map[string]any{"capability": capability, "submittedBy": runtime.TaskID, "params": map[string]any{"workspaceId": runtime.WorkspaceID, "productId": runtime.BrandID}})
		input := brokerRequest{URL: b.origin + "/api/metadata/invoke", Method: "POST", CapabilityKind: "image"}
		if _, err := validateBrokerRequest(input, body, runtime, b.origin); err != nil {
			t.Fatalf("lost %s", capability)
		}
		for _, wrong := range []string{strings.Replace(string(body), runtime.TaskID, "cmsg-other", 1), strings.Replace(string(body), runtime.BrandID, "brand-other", 1), strings.Replace(string(body), capability, "tomako.admin-export", 1)} {
			if _, err := validateBrokerRequest(input, []byte(wrong), runtime, b.origin); err == nil {
				t.Fatal("accepted unscoped metadata request")
			}
		}
	}
}

func TestCapabilityBrokerDisclaimerPreservesGeneratedResultWithoutExposingProvider(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required")
	}
	b, _ := brokerFixture(t)
	dir := t.TempDir()
	b.session.extraEnv = []string{"SKILLS_OL_DIR=" + dir, "OPENAI_API_KEY=synthetic-trusted-key"}
	script := `let input=""; for await(const part of process.stdin) input+=part; const {brief}=JSON.parse(input);
if(process.argv[2]!=="--broker-execute" || process.env.TOMAKO_DISCLAIMER_MODEL_ENDPOINT!=="http://127.0.0.1:11446/v1/responses" || process.env.OPENAI_API_KEY!=="synthetic-trusted-key" || process.env.TOMAKO_CAPABILITY_SOCKET || process.env.TOMAKO_DISCLAIMER_MODEL!=="test-routing/selected-model" || process.env.TOMAKO_DISCLAIMER_REASONING_EFFORT!=="medium") process.exit(2);
process.stdout.write(JSON.stringify({generationMode:"model",sections:[{body:brief.description}]}));`
	if err := os.WriteFile(filepath.Join(dir, "disclaimer-generator.mjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"revision": b.revision, "brief": map[string]any{"description": "用户自己的model、Skill与收入信息"}}
	call := func() *httptest.ResponseRecorder {
		data, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		b.serve(w, httptest.NewRequest("POST", "/v1/disclaimer", bytes.NewReader(data)))
		return w
	}
	if w := call(); w.Code != 200 || !strings.Contains(w.Body.String(), "用户自己的model、Skill与收入信息") || strings.Contains(w.Body.String(), "synthetic-trusted-key") {
		t.Fatalf("receipt changed: %d %s", w.Code, w.Body.String())
	}
	input["model"] = "caller-route"
	if w := call(); w.Code != 400 {
		t.Fatal("caller can select provider parameters")
	}
	delete(input, "model")
	selected := b.session.runtime.GatewayModel
	b.session.runtime.GatewayModel = ""
	if w := call(); w.Code != 503 {
		t.Fatal("missing trusted route must not select a bridge default")
	}
	b.session.runtime.GatewayModel = selected
	input["revision"] = "revoked"
	if w := call(); w.Code != 403 {
		t.Fatal("revoked task can generate")
	}
}

func TestBrokerShellEnvironmentAllowsRuntimeButNeverUnknownSupervisorSecrets(t *testing.T) {
	t.Setenv("FUTURE_PROVIDER_SECRET", "never-copy-this")
	t.Setenv("XAI_API_KEY", "never-copy-xai")
	t.Setenv("OPENAI_API_KEY", "never-copy-provider")
	names := envNames(codexProcessEnv([]string{"TOMAKO_CAPABILITY_BROKER_REQUIRED=1", "CODEX_HOME=/brand/.codex", "TOMAKO_TASK_ENV_FILE=/tmp/broker/machine.env", "SKILLS_OL_DIR=/skills"}))
	for _, key := range []string{"FUTURE_PROVIDER_SECRET", "XAI_API_KEY", "OPENAI_API_KEY"} {
		if names[key] {
			t.Fatalf("supervisor secret inherited: %s", key)
		}
	}
	for _, key := range []string{"PATH", "CODEX_HOME", "TOMAKO_TASK_ENV_FILE", "SKILLS_OL_DIR"} {
		if !names[key] {
			t.Fatalf("runtime variable lost: %s", key)
		}
	}
}

func TestCapabilityBrokerXSearchRunsOnlyPrivateWorker(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	b, _ := brokerFixture(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	b.session.extraEnv = []string{"SKILLS_OL_DIR=" + dir, "XAI_API_KEY=synthetic-trusted-key",
		"XAI_X_SEARCH_MODEL=private-retrieval-model", "XAI_X_SEARCH_ENDPOINT=https://provider.invalid/responses"}
	// The public entry deliberately fails: trusted execution must use the
	// private worker that the Agent's filesystem profile hides.
	if err := os.WriteFile(filepath.Join(dir, "scripts", "signals-x-search.py"), []byte("raise RuntimeError('public wrapper executed')"), 0600); err != nil {
		t.Fatal(err)
	}
	script := `import json, os, sys
assert sys.argv[1:] == ['--broker-execute'] and sys.dont_write_bytecode
assert os.environ['XAI_API_KEY'] == 'synthetic-trusted-key'
assert os.environ['XAI_X_SEARCH_MODEL'] == 'private-retrieval-model'
assert os.environ['XAI_X_SEARCH_ENDPOINT'] == 'https://provider.invalid/responses'
assert not os.environ.get('TOMAKO_CAPABILITY_SOCKET')
request = json.load(sys.stdin)
assert set(request) == {'query', 'maximum', 'allowedHandles', 'excludedHandles', 'fromDate', 'toDate'}
assert request['allowedHandles'] == [] and request['excludedHandles'] == []
print(json.dumps({'model': 'x-search', 'query': request['query'],
 'posts': [{'exactOriginalText': 'A public post can discuss Grok or xAI.', 'evidenceMethod': 'X_SEARCH_CITATION_GATED'}],
 'rejected': [{'reason': 'STATUS_URL_NOT_IN_CITATIONS'}], 'citations': ['https://x.com/author/status/123'],
 'usage': {'xToolCalls': 1, 'costUsd': 0.007, 'costBasis': 'PROVIDER_REPORTED_TICKS'}}))`
	if err := os.WriteFile(filepath.Join(dir, "scripts", "signals-x-search-worker.py"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"revision": b.revision, "query": "normal business research", "maximum": 2}
	call := func() *httptest.ResponseRecorder {
		data, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		b.serve(w, httptest.NewRequest("POST", "/v1/x-search", bytes.NewReader(data)))
		return w
	}
	if w := call(); w.Code != 200 || !strings.Contains(w.Body.String(), "A public post can discuss Grok or xAI.") ||
		!strings.Contains(w.Body.String(), `"costUsd":0.007`) || strings.Contains(w.Body.String(), "synthetic-trusted-key") ||
		strings.Contains(w.Body.String(), "private-retrieval-model") || strings.Contains(w.Body.String(), "provider.invalid") {
		t.Fatalf("public capability receipt changed: %d %s", w.Code, w.Body.String())
	}
	for _, field := range []string{"model", "endpoint", "script", "headers"} {
		input[field] = "caller-controlled"
		if w := call(); w.Code != 400 {
			t.Fatalf("caller can choose private worker %s: %d", field, w.Code)
		}
		delete(input, field)
	}
	input["revision"] = "revoked"
	if w := call(); w.Code != 403 {
		t.Fatal("revoked task can invoke private worker")
	}
}

// Run with CC_X_SEARCH_WORKER_SCRIPT pointing to the matching Skills checkout.
// The optional cross-repository test calls only an in-process synthetic server.
func TestCapabilityBrokerXSearchWithPrivateWorkerIntegration(t *testing.T) {
	worker := os.Getenv("CC_X_SEARCH_WORKER_SCRIPT")
	if worker == "" {
		t.Skip("set CC_X_SEARCH_WORKER_SCRIPT for the real Python worker contract test")
	}
	data, err := os.ReadFile(worker)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := brokerFixture(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "signals-x-search-worker.py"), data, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request map[string]any
		if r.Header.Get("Authorization") != "Bearer synthetic-private-key" || json.NewDecoder(r.Body).Decode(&request) != nil || request["model"] != "synthetic-private-model" {
			t.Error("trusted provider request changed")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"synthetic-private-model","citations":["https://x.com/author/status/123"],"output":[{"type":"message","content":[{"type":"output_text","text":"{\"posts\":[{\"url\":\"https://x.com/author/status/123\",\"exactOriginalText\":\"A public post can discuss Grok.\"},{\"url\":\"https://x.com/uncited/status/456\",\"exactOriginalText\":\"Unverified\"}]}"}]}],"usage":{"input_tokens":1000,"output_tokens":500,"server_side_tool_usage":{"x_search":1},"cost_in_usd_ticks":70000000}}`)
	}))
	defer server.Close()
	b.session.extraEnv = []string{"SKILLS_OL_DIR=" + dir, "XAI_API_KEY=synthetic-private-key",
		"XAI_X_SEARCH_MODEL=synthetic-private-model", "XAI_X_SEARCH_ENDPOINT=" + server.URL}
	input, _ := json.Marshal(map[string]any{"revision": b.revision, "query": "normal business research", "maximum": 2})
	w := httptest.NewRecorder()
	b.serve(w, httptest.NewRequest("POST", "/v1/x-search", bytes.NewReader(input)))
	if w.Code != 200 || calls != 1 {
		t.Fatalf("real broker/worker protocol failed: status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
	var receipt struct {
		Model    string
		Posts    []map[string]any
		Rejected []map[string]any
		Usage    map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Model != "x-search" || len(receipt.Posts) != 1 || receipt.Posts[0]["exactOriginalText"] != "A public post can discuss Grok." ||
		receipt.Posts[0]["evidenceMethod"] != "X_SEARCH_CITATION_GATED" || len(receipt.Rejected) != 1 ||
		receipt.Rejected[0]["reason"] != "STATUS_URL_NOT_IN_CITATIONS" || receipt.Usage["costBasis"] != "PROVIDER_REPORTED_TICKS" ||
		receipt.Usage["costUsd"] != 0.007 || receipt.Usage["xToolCalls"] != float64(1) {
		t.Fatalf("real worker public receipt changed: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "synthetic-private") || strings.Contains(w.Body.String(), server.URL) {
		t.Fatal("trusted provider identity or credentials escaped")
	}
}
