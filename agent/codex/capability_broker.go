package codex

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// A handle selects a credential kind, never authenticates a request. Only the
// session's sandbox can reach its socket; Portal still owns all business ACLs.
const brokerHandlePrefix = "tomako-broker:"
const brokerMaxBody = 32 << 20

type capabilityBroker struct {
	session  *appServerSession
	origin   string
	socket   string
	server   *http.Server
	client   *http.Client
	revision string // protected by session.runtimeMu
}

type brokerRequest struct {
	Revision       string `json:"revision"`
	URL            string `json:"url"`
	Method         string `json:"method"`
	CapabilityKind string `json:"capabilityKind"`
	ContentType    string `json:"contentType"`
	BodyBase64     string `json:"bodyBase64"`
}

func newCapabilityBroker(s *appServerSession) (*capabilityBroker, error) {
	origin := strings.TrimRight(envValue(core.MergeEnv(os.Environ(), s.extraEnv), "SKILL_RESULT_API_URL"), "/")
	if origin != "https://test.tomako.ai" && origin != "https://tomako.ai" {
		return nil, fmt.Errorf("capability broker requires a trusted Portal origin")
	}
	socket := filepath.Join(filepath.Dir(s.taskRuntimeEnvFile), "cap.sock")
	// Linux sockaddr_un has a small fixed path limit. A long brand root must
	// fail before starting an unprotected session, never fall back to bearer.
	if len(socket) >= 104 {
		return nil, fmt.Errorf("capability broker socket path exceeds the platform limit")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, fmt.Errorf("create capability broker: %w", err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	b := &capabilityBroker{session: s, origin: origin, socket: socket,
		client: &http.Client{Timeout: 180 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	b.server = &http.Server{Handler: http.HandlerFunc(b.serve), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 190 * time.Second, MaxHeaderBytes: 8192}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

func (b *capabilityBroker) close() {
	if b != nil {
		_ = b.server.Close()
		_ = os.Remove(b.socket)
	}
}

// Called under runtimeMu. The public file has the same scope schema as legacy
// tools, but no credential bytes. Rotate the generation on each new task; a
// refreshed token for the same task does not invalidate in-flight tool work.
func (b *capabilityBroker) writeRuntime(runtime core.SessionRuntime) (string, error) {
	public := runtime
	public.MachineCapabilityToken = brokerHandle("machine", runtime.MachineCapabilityToken)
	public.ImageCapabilityToken = brokerHandle("image", runtime.ImageCapabilityToken)
	if public.ImageCapabilityToken == "" && runtime.MachineCapabilityToken != "" {
		public.ImageCapabilityToken = brokerHandlePrefix + "machine"
	}
	public.DocumentCapabilityToken = brokerHandle("document", runtime.DocumentCapabilityToken)
	public.EmployeeCommandCapabilityToken = brokerHandle("employee", runtime.EmployeeCommandCapabilityToken)
	public.ProductUpdateCapabilityToken = brokerHandle("product", runtime.ProductUpdateCapabilityToken)
	content, err := taskRuntimeEnvContent(public)
	if err != nil {
		return b.session.taskRuntimeEnvFile, err
	}
	revision := b.revision
	if revision == "" || runtime.TaskID != b.session.runtime.TaskID {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return b.session.taskRuntimeEnvFile, err
		}
		revision = hex.EncodeToString(random[:])
	}
	content += "\nexport TOMAKO_CAPABILITY_SOCKET=" + shellSingleQuote(b.socket)
	content += "\nexport TOMAKO_CAPABILITY_REVISION=" + shellSingleQuote(revision) + "\n"
	content += "export TOMAKO_CAPABILITY_PROXY='1'\n"
	path, err := writeTaskRuntimeEnv(b.session.taskRuntimeEnvFile, content)
	if err == nil {
		b.revision = revision
	}
	return path, err
}

func brokerHandle(kind, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return brokerHandlePrefix + kind
}

func brokerToken(runtime core.SessionRuntime, kind string) string {
	switch kind {
	case "machine":
		return runtime.MachineCapabilityToken
	case "image":
		return runtime.ImageCapabilityToken
	case "document":
		return runtime.DocumentCapabilityToken
	case "employee":
		return runtime.EmployeeCommandCapabilityToken
	case "product":
		return runtime.ProductUpdateCapabilityToken
	default:
		return ""
	}
}

func brokerReject(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `{"code":"CAPABILITY_UNAVAILABLE","message":"This operation is unavailable in the current task; no successful receipt was received."}`)
}

func (b *capabilityBroker) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.RequestURI() == "/v1/disclaimer" {
		b.serveDisclaimer(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.RequestURI() == "/v1/x-search" {
		b.serveXSearch(w, r)
		return
	}
	if r.Method != http.MethodPost || r.URL.RequestURI() != "/v1/portal" {
		brokerReject(w, http.StatusNotFound)
		return
	}
	var input brokerRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, (brokerMaxBody*4/3)+8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	b.session.runtimeMu.RLock()
	runtime, revision, alive := b.session.runtime, b.revision, b.session.alive.Load()
	b.session.runtimeMu.RUnlock()
	token := brokerToken(runtime, input.CapabilityKind)
	if !alive || revision == "" || input.Revision != revision || strings.TrimSpace(token) == "" {
		brokerReject(w, http.StatusForbidden)
		return
	}
	body, err := base64.StdEncoding.DecodeString(input.BodyBase64)
	if err != nil || len(body) > brokerMaxBody {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	endpoint, err := validateBrokerRequest(input, body, runtime, b.origin)
	if err != nil {
		brokerReject(w, http.StatusForbidden)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), input.Method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	if input.ContentType != "" {
		request.Header.Set("Content-Type", input.ContentType)
	}
	request.Header.Set("X-Tomako-Machine-Capability", token)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		brokerReject(w, http.StatusServiceUnavailable)
		return
	}
	request.Header.Set("X-Tomako-Machine-Nonce", hex.EncodeToString(nonce[:]))
	response, err := b.client.Do(request)
	if err != nil {
		brokerReject(w, http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	// No redirect, cookie, location or diagnostic headers leave this boundary.
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		brokerReject(w, http.StatusBadGateway)
		return
	}
	// Portal marks only explicitly constructed public operation errors. Keep
	// their recovery contract; arbitrary diagnostics still stay inside Portal.
	publicError := response.StatusCode >= 400 && response.StatusCode < 500 &&
		response.Header.Get("X-Tomako-Public-Error") == "v1"
	if response.StatusCode >= 400 && !publicError {
		brokerReject(w, response.StatusCode)
		return
	}
	limit := brokerMaxBody
	if publicError {
		limit = 16 << 10
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil || len(result) > limit || publicError && !validBrokerPublicError(result, response.StatusCode) {
		brokerReject(w, http.StatusBadGateway)
		return
	}
	for _, secret := range []string{runtime.MachineCapabilityToken, runtime.ImageCapabilityToken,
		runtime.DocumentCapabilityToken, runtime.EmployeeCommandCapabilityToken, runtime.ProductUpdateCapabilityToken} {
		if secret != "" && bytes.Contains(result, []byte(secret)) {
			brokerReject(w, http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(result)
}

var brokerEmployeeRoute = regexp.MustCompile(`^/employees/[A-Za-z0-9_:-]+/(commands|capabilities/[A-Za-z0-9._:-]+(?:/versions(?:/[A-Za-z0-9_:-]+(?:/restore)?)?|/restore|/publish-preview)?)$`)
var brokerImageRoute = regexp.MustCompile(`^/images/(generate|reference-upload|batch-reservation(?:/settle)?|batches/[A-Za-z0-9._:-]+/finalize|[A-Za-z0-9._:-]+/(?:status|attach))$`)
var brokerBacklinkRoute = regexp.MustCompile(`^/backlinks(?:/template|/[A-Za-z0-9_:-]+|/tasks(?:/[A-Za-z0-9_:-]+(?:/events|/operations)?)?)?$`)
var brokerExternalResultRoute = regexp.MustCompile(`^/external-data/results/[a-f0-9]{64}$`)

// The registry admits existing, scoped product capabilities only. It does not
// classify natural language or replace Portal's identity/resource authorization.
func validateBrokerRequest(input brokerRequest, body []byte, runtime core.SessionRuntime, origin string) (*url.URL, error) {
	deny := func() (*url.URL, error) { return nil, fmt.Errorf("operation outside current capability") }
	u, err := url.Parse(input.URL)
	if err != nil || u.Scheme+"://"+u.Host != origin || u.User != nil || u.Fragment != "" ||
		u.Opaque != "" || u.RawPath != "" || strings.Contains(u.Path, "\\") || strings.Contains(u.Path, "//") ||
		strings.ContainsAny(input.ContentType, "\r\n") {
		return deny()
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return deny()
		}
	}
	if input.Method != "GET" && input.Method != "POST" && input.Method != "PATCH" && input.Method != "PUT" {
		return deny()
	}
	if input.Method == "GET" && len(body) != 0 {
		return deny()
	}
	for _, id := range []string{runtime.TaskID, runtime.WorkspaceID, runtime.BrandID} {
		if !taskRuntimeIDPattern.MatchString(id) {
			return deny()
		}
	}
	machine := input.CapabilityKind == "machine" || input.CapabilityKind == "image"
	if input.Method == "POST" && (u.Path == "/api/skill-results" || u.Path == "/api/llm-task-progress") {
		var data map[string]any
		if !machine || json.Unmarshal(body, &data) != nil || data["taskId"] != runtime.TaskID || u.RawQuery != "" {
			return deny()
		}
		return u, nil
	}
	if u.Path == "/api/metadata/invoke" && input.Method == "POST" && machine && u.RawQuery == "" {
		var data struct {
			Capability  string         `json:"capability"`
			SubmittedBy string         `json:"submittedBy"`
			Params      map[string]any `json:"params"`
		}
		if json.Unmarshal(body, &data) != nil || data.SubmittedBy != runtime.TaskID ||
			data.Params["workspaceId"] != runtime.WorkspaceID || data.Params["productId"] != runtime.BrandID {
			return deny()
		}
		switch data.Capability {
		case "tomako.generate-video", "tomako.read-fal-video", "tomako.list-backlink-tasks",
			"tomako.read-backlink-task", "tomako.read-backlink-mailbox", "tomako.list-backlink-approvals",
			"tomako.create-backlink-task", "tomako.update-backlink-task", "tomako.send-backlink-email",
			"tomako.verify-backlink-task", "tomako.reconcile-backlink-email", "tomako.sync-backlink-replies":
			return u, nil
		default:
			return deny()
		}
	}
	prefix := "/api/workspaces/" + runtime.WorkspaceID + "/products/" + runtime.BrandID
	if !strings.HasPrefix(u.Path, prefix+"/") {
		return deny()
	}
	path := strings.TrimPrefix(u.Path, prefix)
	if input.CapabilityKind == "document" {
		if path == "/content-documents/agent-operations" && input.Method == "POST" && u.RawQuery == "" {
			return u, nil
		}
		if path == "/chat/agent-reply-checklists" && (input.Method == "GET" || input.Method == "PUT") && u.RawQuery == "" {
			return u, nil
		}
	}
	if input.CapabilityKind == "employee" && brokerEmployeeRoute.MatchString(path) && (input.Method == "GET" || input.Method == "POST") {
		return u, nil
	}
	if input.CapabilityKind == "product" && path == "/agent-update" && input.Method == "PATCH" && u.RawQuery == "" {
		return u, nil
	}
	if machine {
		if path == "/agent-context" && input.Method == "GET" {
			return u, nil
		}
		if path == "/agent-update" && input.Method == "PATCH" {
			return u, nil
		}
		if (path == "/cmo/reception" || path == "/blog-seo-skill-reads") && input.Method == "POST" {
			return u, nil
		}
		if path == "/visual-assets/video-upload" && input.Method == "POST" {
			return u, nil
		}
		if brokerImageRoute.MatchString(path) && (input.Method == "GET" && strings.HasSuffix(path, "/status") || input.Method == "POST" && !strings.HasSuffix(path, "/status")) {
			return u, nil
		}
		if brokerBacklinkRoute.MatchString(path) && (input.Method == "GET" || input.Method == "POST") {
			return u, nil
		}
		if input.Method == "GET" && (path == "/external-data/discover" || path == "/external-data/describe" || brokerExternalResultRoute.MatchString(path)) {
			return u, nil
		}
		if path == "/external-data/execute" && input.Method == "POST" {
			return u, nil
		}
	}
	return deny()
}

type brokerXSearchRequest struct {
	Revision        string   `json:"revision"`
	Query           string   `json:"query"`
	Maximum         int      `json:"maximum"`
	AllowedHandles  []string `json:"allowedHandles"`
	ExcludedHandles []string `json:"excludedHandles"`
	FromDate        *string  `json:"fromDate"`
	ToDate          *string  `json:"toDate"`
}

var xHandle = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

// The existing disclaimer implementation remains the only owner of its prompt,
// model parsing and grounding checks. The sandbox supplies a brief, never a
// provider URL, key, model or executable path.
func (b *capabilityBroker) serveDisclaimer(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision string          `json:"revision"`
		Brief    json.RawMessage `json:"brief"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.Brief) == 0 || input.Brief[0] != '{' {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	b.session.runtimeMu.RLock()
	runtime, revision, alive := b.session.runtime, b.revision, b.session.alive.Load()
	b.session.runtimeMu.RUnlock()
	if !alive || revision == "" || input.Revision != revision || !imageRuntimeAuthorized(runtime) {
		brokerReject(w, http.StatusForbidden)
		return
	}
	// Routing remains owned by Portal. A specialized helper must follow this
	// task's resolved model and effort, never introduce a second bridge default.
	model, effort := strings.TrimSpace(runtime.GatewayModel), strings.TrimSpace(runtime.ReasoningEffort)
	if model == "" || effort == "" || strings.ContainsAny(model+effort, "\r\n\x00") {
		brokerReject(w, http.StatusServiceUnavailable)
		return
	}
	env := core.MergeEnv(os.Environ(), b.session.extraEnv)
	key := envValue(env, "OPENAI_API_KEY")
	script := filepath.Join(envValue(env, "SKILLS_OL_DIR"), "disclaimer-generator.mjs")
	if !filepath.IsAbs(script) {
		brokerReject(w, http.StatusServiceUnavailable)
		return
	}
	endpoint := "http://127.0.0.1:11446/v1/responses"
	if b.origin == "https://tomako.ai" {
		endpoint = "http://127.0.0.1:11436/v1/responses"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", script, "--broker-execute")
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if name == "PATH" || name == "HOME" || name == "LANG" || name == "TZ" || name == "OPENAI_API_KEY" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "TOMAKO_DISCLAIMER_MODEL_ENDPOINT="+endpoint,
		"TOMAKO_DISCLAIMER_MODEL="+model, "TOMAKO_DISCLAIMER_REASONING_EFFORT="+effort)
	encoded, _ := json.Marshal(map[string]json.RawMessage{"brief": input.Brief})
	cmd.Stdin = bytes.NewReader(encoded)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil || stdout.Len() > 256*1024 || key != "" && bytes.Contains(stdout.Bytes(), []byte(key)) || !json.Valid(stdout.Bytes()) {
		brokerReject(w, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(stdout.Bytes())
}

func (b *capabilityBroker) serveXSearch(w http.ResponseWriter, r *http.Request) {
	var input brokerXSearchRequest
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.Query) == 0 || len(input.Query) > 8000 ||
		input.Maximum < 1 || input.Maximum > 10 || len(input.AllowedHandles) > 20 || len(input.ExcludedHandles) > 20 ||
		(len(input.AllowedHandles) > 0 && len(input.ExcludedHandles) > 0) {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	for _, handle := range append(append([]string{}, input.AllowedHandles...), input.ExcludedHandles...) {
		if !xHandle.MatchString(handle) {
			brokerReject(w, http.StatusBadRequest)
			return
		}
	}
	for _, date := range []*string{input.FromDate, input.ToDate} {
		if date != nil {
			if _, err := time.Parse("2006-01-02", *date); err != nil {
				brokerReject(w, http.StatusBadRequest)
				return
			}
		}
	}
	if input.FromDate != nil && input.ToDate != nil && *input.FromDate > *input.ToDate {
		brokerReject(w, http.StatusBadRequest)
		return
	}
	b.session.runtimeMu.RLock()
	runtime, revision, alive := b.session.runtime, b.revision, b.session.alive.Load()
	b.session.runtimeMu.RUnlock()
	if !alive || revision == "" || input.Revision != revision || !imageRuntimeAuthorized(runtime) {
		brokerReject(w, http.StatusForbidden)
		return
	}
	env := core.MergeEnv(os.Environ(), b.session.extraEnv)
	key := envValue(env, "XAI_API_KEY")
	script := filepath.Join(envValue(env, "SKILLS_OL_DIR"), "scripts", "signals-x-search-worker.py")
	if key == "" || !filepath.IsAbs(script) {
		brokerReject(w, http.StatusServiceUnavailable)
		return
	}
	// Reuse the existing citation-gated implementation and its exact prompt.
	// The request cannot choose code, a file, a model, an endpoint or headers.
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", script, "--broker-execute")
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if name == "PATH" || name == "HOME" || name == "LANG" || name == "TZ" ||
			name == "XAI_API_KEY" || name == "XAI_X_SEARCH_ENDPOINT" || name == "XAI_X_SEARCH_MODEL" {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	if envValue(cmd.Env, "XAI_X_SEARCH_MODEL") == "" {
		cmd.Env = append(cmd.Env, "XAI_X_SEARCH_MODEL=grok-4.5")
	}
	if envValue(cmd.Env, "XAI_X_SEARCH_ENDPOINT") == "" {
		cmd.Env = append(cmd.Env, "XAI_X_SEARCH_ENDPOINT=https://api.x.ai/v1/responses")
	}
	// The revision authorizes the broker call; it is not a worker parameter.
	encoded, _ := json.Marshal(map[string]any{
		"query": input.Query, "maximum": input.Maximum,
		"allowedHandles":  append([]string{}, input.AllowedHandles...),
		"excludedHandles": append([]string{}, input.ExcludedHandles...),
		"fromDate":        input.FromDate, "toDate": input.ToDate,
	})
	cmd.Stdin = bytes.NewReader(encoded)
	// This fixed script caps provider data at 4 MB and validated posts at ten.
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil || stdout.Len() > 4_000_000 || bytes.Contains(stdout.Bytes(), []byte(key)) {
		brokerReject(w, http.StatusBadGateway)
		return
	}
	var receipt map[string]any
	if json.Unmarshal(stdout.Bytes(), &receipt) != nil {
		brokerReject(w, http.StatusBadGateway)
		return
	}
	receipt["model"] = "x-search"
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(receipt)
}
