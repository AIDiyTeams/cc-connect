package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type webhookReconstructPlatform struct {
	name            string
	acceptedPrefix  string
	lastSessionKey  string
	reconstructCall int
}

func (p *webhookReconstructPlatform) Name() string { return p.name }
func (p *webhookReconstructPlatform) Start(MessageHandler) error {
	return nil
}
func (p *webhookReconstructPlatform) Reply(context.Context, any, string) error {
	return nil
}
func (p *webhookReconstructPlatform) Send(context.Context, any, string) error {
	return nil
}
func (p *webhookReconstructPlatform) Stop() error { return nil }
func (p *webhookReconstructPlatform) ReconstructReplyCtx(sessionKey string) (any, error) {
	p.lastSessionKey = sessionKey
	p.reconstructCall++
	if !strings.HasPrefix(sessionKey, p.acceptedPrefix) {
		return nil, errors.New("session key is owned by another adapter")
	}
	return "reply:" + sessionKey, nil
}

func TestWebhookServer_AuthBearer(t *testing.T) {
	ws := NewWebhookServer(0, "my-secret", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook", nil)
	r.Header.Set("Authorization", "Bearer my-secret")
	if !ws.authenticate(r) {
		t.Error("expected auth to succeed with correct Bearer token")
	}
	r.Header.Set("Authorization", "Bearer wrong")
	if ws.authenticate(r) {
		t.Error("expected auth to fail with wrong Bearer token")
	}
}

func TestWebhookServer_AuthHeader(t *testing.T) {
	ws := NewWebhookServer(0, "tok123", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook", nil)
	r.Header.Set("X-Webhook-Token", "tok123")
	if !ws.authenticate(r) {
		t.Error("expected auth to succeed with X-Webhook-Token")
	}
}

func TestWebhookServer_AuthQuery(t *testing.T) {
	ws := NewWebhookServer(0, "qsecret", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook?token=qsecret", nil)
	if !ws.authenticate(r) {
		t.Error("expected auth to succeed with query token")
	}
}

func TestWebhookServer_NoTokenRequired(t *testing.T) {
	ws := NewWebhookServer(0, "", "/hook")
	r := httptest.NewRequest(http.MethodPost, "/hook", nil)
	if !ws.authenticate(r) {
		t.Error("expected auth to pass when no token configured")
	}
}

func TestWebhookServer_HandleHook_MethodNotAllowed(t *testing.T) {
	ws := NewWebhookServer(0, "", "/hook")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/hook", nil)
	ws.handleHook(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestWebhookServer_HandleHook_Unauthorized(t *testing.T) {
	ws := NewWebhookServer(0, "secret", "/hook")
	w := httptest.NewRecorder()
	body, _ := json.Marshal(WebhookRequest{SessionKey: "tg:1:1", Prompt: "hi"})
	r := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	ws.handleHook(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestWebhookServer_HandleHook_Validation(t *testing.T) {
	ws := NewWebhookServer(0, "", "/hook")

	tests := []struct {
		name string
		body WebhookRequest
		code int
	}{
		{"missing session_key", WebhookRequest{Prompt: "hi"}, http.StatusBadRequest},
		{"missing prompt and exec", WebhookRequest{SessionKey: "tg:1:1"}, http.StatusBadRequest},
		{"both prompt and exec", WebhookRequest{SessionKey: "tg:1:1", Prompt: "hi", Exec: "ls"}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			body, _ := json.Marshal(tt.body)
			r := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
			ws.handleHook(w, r)
			if w.Code != tt.code {
				t.Errorf("expected %d, got %d: %s", tt.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestWebhookServer_DefaultValues(t *testing.T) {
	ws := NewWebhookServer(0, "", "")
	if ws.port != 9111 {
		t.Errorf("expected default port 9111, got %d", ws.port)
	}
	if ws.path != "/hook" {
		t.Errorf("expected default path /hook, got %s", ws.path)
	}
}

func TestResolveWebhookReplyTarget_PrefersDirectStaticPlatform(t *testing.T) {
	direct := &webhookReconstructPlatform{
		name:           "telegram",
		acceptedPrefix: "telegram:",
	}
	bridge := &webhookReconstructPlatform{
		name:           "bridge",
		acceptedPrefix: "java-backend:",
	}

	target, replyCtx, err := resolveWebhookReplyTarget(
		[]Platform{bridge, direct},
		"telegram:42:7",
	)
	if err != nil {
		t.Fatalf("resolveWebhookReplyTarget() error = %v", err)
	}
	if target != direct {
		t.Fatalf("target = %q, want direct telegram platform", target.Name())
	}
	if replyCtx != "reply:telegram:42:7" {
		t.Fatalf("replyCtx = %v", replyCtx)
	}
	if bridge.reconstructCall != 0 {
		t.Fatalf("bridge reconstruct calls = %d, want 0", bridge.reconstructCall)
	}
}

func TestResolveWebhookReplyTarget_UsesBridgeForDynamicAdapterPrefix(t *testing.T) {
	telegram := &webhookReconstructPlatform{
		name:           "telegram",
		acceptedPrefix: "telegram:",
	}
	bridge := &webhookReconstructPlatform{
		name:           "bridge",
		acceptedPrefix: "java-backend:",
	}

	target, replyCtx, err := resolveWebhookReplyTarget(
		[]Platform{telegram, bridge},
		"java-backend:tomako:4:workspace:task:llm-1234",
	)
	if err != nil {
		t.Fatalf("resolveWebhookReplyTarget() error = %v", err)
	}
	if target != bridge {
		t.Fatalf("target = %q, want bridge platform", target.Name())
	}
	if replyCtx != "reply:java-backend:tomako:4:workspace:task:llm-1234" {
		t.Fatalf("replyCtx = %v", replyCtx)
	}
	if bridge.lastSessionKey != "java-backend:tomako:4:workspace:task:llm-1234" {
		t.Fatalf("bridge session key = %q", bridge.lastSessionKey)
	}
}
