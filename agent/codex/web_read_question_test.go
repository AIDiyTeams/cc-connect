package codex

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// A two-window page: the second window repeats the content hash unless the
// reader is told the page changed. "🙂" is two UTF-16 units, "排" is one.
const webReadQuestionFakeReader = `
import { existsSync } from "node:fs";
const args = process.argv.slice(2);
const start = Number((args.find(a => a.startsWith("--start=")) || "--start=0").slice(8));
const changed = existsSync("changed"), blocked = existsSync("blocked");
const first = "Intro 🙂 排期\nScheduling after approval works in three steps.\n";
const second = "Teams can   assign reviewers\nbefore a post is queued.\n";
const base = {url: args[0], finalUrl: args[0], sourceUrl: args[0], title: "Approval guide", fetchedAt: "2026-10-12T00:00:00.000Z",
  totalChars: 120, mode: "body", bodyRead: true, contentIncomplete: false, rangeComplete: true, args};
if (blocked) {
  process.stdout.write(JSON.stringify({url: args[0], status: "blocked", note: "The page was not readable from Tomako's server."}));
} else if (start === 0) {
  process.stdout.write(JSON.stringify({...base, status: "ok", contentHash: "a".repeat(64), start: 0, endExclusive: 60, nextStart: 61,
    text: first, receipt: {path: "/scratch/web-read-1.json", sha256: "1".repeat(64)}}));
} else {
  process.stdout.write(JSON.stringify({...base, status: "ok", contentHash: (changed ? "b" : "a").repeat(64), start, endExclusive: 120,
    nextStart: null, text: second, receipt: {path: "/scratch/web-read-2.json", sha256: "2".repeat(64)}}));
}
`

type fakeWebReaderGateway struct {
	mu       sync.Mutex
	requests []map[string]any
	auth     []string
	answers  []string
	status   int
}

func (g *fakeWebReaderGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	g.mu.Lock()
	g.requests = append(g.requests, body)
	g.auth = append(g.auth, r.Header.Get("Authorization")+" "+r.URL.Path)
	answer := g.answers[0]
	if len(g.answers) > 1 {
		g.answers = g.answers[1:]
	}
	status := g.status
	g.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"output": []any{
		map[string]any{"type": "reasoning", "content": []any{}},
		map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": answer}}},
	}})
}

func webReadQuestionSession(t *testing.T, answers ...string) (*appServerSession, *fakeWebReaderGateway) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	s := webReadTestSession(t, webReadQuestionFakeReader)
	gateway := &fakeWebReaderGateway{answers: answers}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	home := t.TempDir()
	config := "model_provider = \"custom\"\n\n[model_providers.custom]\nname = \"custom\"\nbase_url = \"" + server.URL + "/v1\"\nwire_api = \"responses\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"OPENAI_API_KEY":"gateway-key"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "")
	s.codexHome = home
	s.runtime.LogicalModel = "GPT_6_1_SOL"
	s.runtime.RoutePolicyID = "rp-gpt-6-1-sol"
	s.runtime.InferenceRequestID = "infer-1"
	s.runtime.WebReader = &core.HelperModelRoute{LogicalModel: "GPT_6_LUNA", GatewayModel: "tomako/gpt-6-luna",
		RoutePolicyID: "rp-gpt-6-luna", RoutePolicyVersion: 3, ReasoningEffort: "low"}
	return s, gateway
}

func questionProperty(tool map[string]any) any {
	return tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["question"]
}

func TestWebReadQuestionIsOfferedOnlyWithAPortalReaderRoute(t *testing.T) {
	s := webReadTestSession(t, "")
	if questionProperty(s.webReadThreadTool()) != nil {
		t.Fatal("question mode was offered without a Portal reader route")
	}
	s.runtime.WebReader = &core.HelperModelRoute{GatewayModel: "tomako/gpt-6-luna"}
	tool := s.webReadThreadTool()
	if questionProperty(tool) == nil || !strings.Contains(tool["description"].(string), "question") {
		t.Fatalf("question mode missing: %v", tool)
	}
	if questionProperty(webReadDynamicTool()) != nil {
		t.Fatal("the shared base schema was changed")
	}
}

func TestWebReadQuestionAnswersWithLocatedQuotesAndTheReadReceipts(t *testing.T) {
	s, gateway := webReadQuestionSession(t,
		"```json\n{\"coverage\":\"PARTIAL\",\"finding\":\"讲了审批后排期，没讲多平台。\",\"evidence\":[\"Scheduling after approval works in three steps.\",\"Teams can assign reviewers before a post is queued.\",\"This sentence is not on the page at all.\"],\"limitations\":\"无\"}\n```")
	output, err := s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "  Does it explain scheduling after approval?  "})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Coverage: PARTIAL", "Finding: 讲了审批后排期，没讲多平台。",
		// "Intro 🙂 排期\n" is 12 UTF-16 units: the emoji counts twice.
		`- [12,59) "Scheduling after approval works in three steps."`,
		// Whitespace differs from the page, so the located page text is quoted.
		`- [61,114) "Teams can   assign reviewers\nbefore a post is queued."`,
		`{"path":"/scratch/web-read-1.json","sha256":"` + strings.Repeat("1", 64) + `"} covers [0,60)`,
		`{"path":"/scratch/web-read-2.json","sha256":"` + strings.Repeat("2", 64) + `"} covers [61,120)`,
		"Read: the whole page", "Question: Does it explain scheduling after approval?",
		"Reader: GPT_6_LUNA, 1 attempt(s)", "2 quote(s) located",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in:\n%s", want, output)
		}
	}
	if strings.Contains(output, "not on the page") || strings.Contains(output, "Intro") {
		t.Fatalf("unlocated quote or page text leaked into the answer:\n%s", output)
	}
	if len(gateway.requests) != 1 || gateway.auth[0] != "Bearer gateway-key /v1/responses" {
		t.Fatalf("requests=%d auth=%v", len(gateway.requests), gateway.auth)
	}
	request := gateway.requests[0]
	metadata := request["client_metadata"].(map[string]any)
	if request["model"] != "tomako/gpt-6-luna" || request["reasoning"].(map[string]any)["effort"] != "low" ||
		request["store"] != false || metadata["tomako.logical_model"] != "GPT_6_LUNA" ||
		metadata["tomako.route_policy_id"] != "rp-gpt-6-luna" || metadata["tomako.route_policy_version"] != "3" ||
		metadata["tomako.purpose"] != "web_reader" || metadata["tomako.chat_session_id"] != "csess-a" ||
		metadata["tomako.inference_request_id"] != "infer-1" {
		t.Fatalf("request=%v", request)
	}
	prompt, _ := json.Marshal(request["input"])
	if !strings.Contains(string(prompt), "Scheduling after approval") || !strings.Contains(string(prompt), "assign reviewers") {
		t.Fatalf("the reader did not get both windows: %s", prompt)
	}
}

func TestWebReadQuestionRetriesOnceAndThenSaysNothingWasVerified(t *testing.T) {
	invented := `{"coverage":"FULL","finding":"Everything is covered.","evidence":["A sentence the page never had."],"limitations":""}`
	s, gateway := webReadQuestionSession(t, invented, invented)
	output, err := s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "Is approval covered?"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gateway.requests) != 2 || !strings.Contains(output, "[Page reader gave no checkable answer]") ||
		strings.Contains(output, "Everything is covered") || strings.Contains(output, "receipt") {
		t.Fatalf("requests=%d output:\n%s", len(gateway.requests), output)
	}
	retry, _ := json.Marshal(gateway.requests[1]["input"])
	if !strings.Contains(string(retry), "could not be used") {
		t.Fatalf("the retry did not explain the problem: %s", retry)
	}

	s, gateway = webReadQuestionSession(t, "not json",
		`{"coverage":"NONE","finding":"It is about approvals only.","evidence":["Scheduling after approval works in three steps."],"limitations":""}`)
	output, err = s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "Does it compare pricing?"})
	if err != nil || len(gateway.requests) != 2 || !strings.Contains(output, "Coverage: NONE") || !strings.Contains(output, "2 attempt(s)") {
		t.Fatalf("err=%v requests=%d output:\n%s", err, len(gateway.requests), output)
	}
}

func TestWebReadQuestionNeverStitchesTwoPageVersions(t *testing.T) {
	s, gateway := webReadQuestionSession(t,
		`{"coverage":"PARTIAL","finding":"Approval steps only.","evidence":["Scheduling after approval works in three steps."],"limitations":""}`)
	if err := os.WriteFile(filepath.Join(s.workDir, "changed"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "Is approval covered?"})
	if err != nil {
		t.Fatal(err)
	}
	prompt, _ := json.Marshal(gateway.requests[0]["input"])
	if strings.Contains(string(prompt), "assign reviewers") || strings.Contains(output, "web-read-2") ||
		!strings.Contains(output, "Read: characters 0-60 of 120; the rest was not read (ask again with start=61") {
		t.Fatalf("a changed page version was stitched in:\n%s\n%s", prompt, output)
	}
}

func TestWebReadQuestionDoesNotAskAboutAPageThatWasNotRead(t *testing.T) {
	s, gateway := webReadQuestionSession(t, `{}`)
	if err := os.WriteFile(filepath.Join(s.workDir, "blocked"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "Is approval covered?"})
	if err != nil || len(gateway.requests) != 0 || !strings.Contains(output, "[Web page not read]") || !strings.Contains(output, "unverified") {
		t.Fatalf("err=%v requests=%d output:\n%s", err, len(gateway.requests), output)
	}
}

func TestWebReadQuestionRefusesUnusableRequests(t *testing.T) {
	s, gateway := webReadQuestionSession(t, `{}`)
	for _, args := range []map[string]any{
		{"question": ""}, {"question": strings.Repeat("问", webReadQuestionMaxRunes+1)}, {"question": 3},
		{"question": "q", "mode": "outline"}, {"question": "q", "endExclusive": float64(100)},
		{"question": "q", "start": float64(-1)},
	} {
		args["url"] = "https://example.com/guide"
		if _, err := s.readWebPage(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	s.runtime.WebReader = nil
	if _, err := s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "q"}); err == nil ||
		!strings.Contains(err.Error(), "without question") {
		t.Fatalf("err=%v", err)
	}
	if len(gateway.requests) != 0 {
		t.Fatalf("refused requests reached the model: %d", len(gateway.requests))
	}
}

func TestWebReadQuestionReportsAGatewayFailure(t *testing.T) {
	s, gateway := webReadQuestionSession(t, `{}`)
	gateway.status = http.StatusBadGateway
	output, err := s.readWebPage(map[string]any{"url": "https://example.com/guide", "question": "Is approval covered?"})
	if err != nil || len(gateway.requests) != 2 || !strings.Contains(output, "HTTP 502") || !strings.Contains(output, "Read the body without question") {
		t.Fatalf("err=%v requests=%d output:\n%s", err, len(gateway.requests), output)
	}
	if strings.Contains(output, "gateway-key") {
		t.Fatal("the gateway credential leaked")
	}
}
