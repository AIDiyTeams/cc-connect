package core

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type startupRuntimeAgent struct {
	stubAgent
	runtimes   []SessionRuntime
	ids        []string
	session    AgentSession
	failResume bool
}

func (a *startupRuntimeAgent) StartSessionWithRuntime(_ context.Context, id string, runtime SessionRuntime) (AgentSession, error) {
	a.runtimes = append(a.runtimes, runtime)
	a.ids = append(a.ids, id)
	if a.failResume && id != "" {
		return nil, fmt.Errorf("stale session")
	}
	return a.session, nil
}

type incompatibleStartupSession struct{ *controllableAgentSession }

func (*incompatibleStartupSession) SupportsSessionRuntime(SessionRuntime) bool { return false }

func TestStartupRuntime_PreservedThroughResumeFallbackAndRecycling(t *testing.T) {
	runtime := SessionRuntime{Scene: "growth_opportunity_user_voice_search", GatewayModel: "gateway/search-model", WebSearch: "live", ReasoningEffort: "high"}
	for _, resume := range []bool{false, true} {
		p := &stubPlatformEngine{n: "test"}
		fresh := newControllableSession("fresh")
		agent := &startupRuntimeAgent{session: fresh, failResume: resume}
		e := NewEngine("test", agent, []Platform{p}, "", LangEnglish)
		t.Cleanup(func() { e.Stop() })
		session := &Session{}
		if resume {
			session.AgentSessionID = "old"
			old := &incompatibleStartupSession{newControllableSession("old")}
			e.interactiveStates["test:key"] = &interactiveState{agentSession: old, platform: p, replyCtx: "ctx"}
		}
		state := e.getOrCreateInteractiveStateWith("test:key", p, "ctx", session, e.sessions, nil, "", runtime)
		if state.agentSession != fresh {
			t.Fatal("startup runtime did not reach the runtime-aware agent")
		}
		wantIDs := []string{""}
		if resume {
			wantIDs = []string{"old", ""}
		}
		if !reflect.DeepEqual(agent.ids, wantIDs) {
			t.Fatalf("startup sequence: %v", agent.ids)
		}
		for _, got := range agent.runtimes {
			if !reflect.DeepEqual(got, runtime) {
				t.Fatalf("runtime changed before startup: %+v", got)
			}
		}
	}
	if _, err := startSessionWithRuntime(context.Background(), &stubAgent{}, "", runtime); err != nil {
		t.Fatal("legacy adapter fallback regressed", err)
	}
}
