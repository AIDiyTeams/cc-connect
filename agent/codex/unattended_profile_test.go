package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

// A scheduled run with nobody watching gets no tool that waits on a person.
func TestUnattendedRunOffersNoQuestionOrFormTool(t *testing.T) {
	s := &appServerSession{workDir: "/srv/tomako"}
	s.runtime = core.SessionRuntime{ChatSessionID: "csess-1", ExecutionProfile: core.ExecutionProfileUnattended}

	params := s.threadRequestParams()
	config := params["config"].(map[string]any)
	if config["features.default_mode_request_user_input"] != false {
		t.Fatalf("unattended run still offers request_user_input: %#v", config)
	}
	tools, _ := params["dynamicTools"].([]map[string]any)
	for _, tool := range tools {
		if tool["name"] == formDynamicTool()["name"] {
			t.Fatalf("unattended run still offers the form tool: %#v", tools)
		}
	}

	s.runtime.ExecutionProfile = ""
	attended := s.threadRequestParams()
	if attended["config"].(map[string]any)["features.default_mode_request_user_input"] != true || !s.formToolAvailable() {
		t.Fatalf("a watched conversation must keep its question and form tools: %#v", attended)
	}
}

func TestUnattendedQuestionIsAnsweredAtOnceInsteadOfWaiting(t *testing.T) {
	response := unattendedRequestUserInputResponse([]appServerRequestUserInputQuestion{{ID: "q1", Question: "Which market?"}, {ID: " "}})
	if len(response.Answers) != 1 || !strings.Contains(response.Answers["q1"].Answers[0], "unattended") {
		t.Fatalf("unexpected unattended answer: %#v", response)
	}
	raw, _ := json.Marshal(response)
	if !strings.Contains(string(raw), "q1") {
		t.Fatalf("answer not keyed by question id: %s", raw)
	}
}

func TestOnlyTheUnattendedProfileSurvivesNormalization(t *testing.T) {
	if !(core.SessionRuntime{ExecutionProfile: core.ExecutionProfileUnattended}).Unattended() {
		t.Fatal("unattended runtime not recognised")
	}
	if (core.SessionRuntime{ExecutionProfile: "lean"}).Unattended() {
		t.Fatal("an unknown profile must not count as unattended")
	}
}
