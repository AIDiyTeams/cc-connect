package codex

import (
	"os"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestEmployeeRuntimeScopeWritesCurrentTurnAndClearsPreviousDefault(t *testing.T) {
	runtime := core.SessionRuntime{TaskID: "llm-nana", WorkspaceID: "w", BrandID: "b",
		EmployeeCommandCapabilityToken: "employee-token", EmployeeID: "employee-a", EmployeeCapabilityOrigin: "https://test.tomako.ai"}
	path, err := updateTaskRuntimeEnv("", runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTaskRuntimeEnv(path) })
	for _, employeeID := range []string{"employee-a", "employee-b", ""} {
		runtime.EmployeeID = employeeID
		if _, err = updateTaskRuntimeEnv(path, runtime); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "export EMPLOYEE_ID='"+employeeID+"'\n") ||
			!strings.Contains(string(body), "export EMPLOYEE_CAPABILITY_ORIGIN='https://test.tomako.ai'") {
			t.Fatalf("current employee routing missing: %s", body)
		}
		if employeeID != "employee-a" && strings.Contains(string(body), "employee-a") {
			t.Fatal("previous employee default leaked into current turn")
		}
	}
	runtime.EmployeeCommandCapabilityToken = ""
	runtime.ImageCapabilityToken = "image-only"
	if _, err = updateTaskRuntimeEnv(path, runtime); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "export EMPLOYEE_") {
		t.Fatal("ungranted turn retained employee authority or default routing")
	}
}

func TestEmployeeRuntimeScopeRejectsMalformedRouting(t *testing.T) {
	base := core.SessionRuntime{TaskID: "llm-nana", WorkspaceID: "w", BrandID: "b", EmployeeCommandCapabilityToken: "token"}
	for _, employeeID := range []string{"../other", "id\nInjected", strings.Repeat("a", 97)} {
		runtime := base
		runtime.EmployeeID = employeeID
		if _, err := taskRuntimeEnvContent(runtime); err == nil {
			t.Fatalf("invalid employee ID accepted: %q", employeeID)
		}
	}
	for _, origin := range []string{"http://test.tomako.ai", "https://test.tomako.ai/path", "https://user@test.tomako.ai", "https://test.tomako.ai?", "https://test.tomako.ai?other=1", "https://test.tomako.ai#fragment", "https://test.tomako.ai\nInjected"} {
		runtime := base
		runtime.EmployeeCapabilityOrigin = origin
		if _, err := taskRuntimeEnvContent(runtime); err == nil {
			t.Fatalf("invalid employee origin accepted: %q", origin)
		}
	}
}

func TestEmployeeRuntimeScopeFollowsResumedTurnAndAuthorityRefresh(t *testing.T) {
	s := pausedTurnSession(t)
	runtime := core.SessionRuntime{TaskID: "cmsg-1", WorkspaceID: "w", BrandID: "b", ImageCapabilityToken: "image",
		EmployeeCommandCapabilityToken: "employee-token", EmployeeID: "employee-a", EmployeeCapabilityOrigin: "https://test.tomako.ai"}
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	runtime.EmployeeID = "employee-b"
	if err := s.SetSessionRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(s.taskRuntimeEnvFile)
	if !strings.Contains(string(body), "export EMPLOYEE_ID='employee-b'") || strings.Contains(string(body), "employee-a") {
		t.Fatal("resumed turn did not replace the employee default")
	}
	runtime.EmployeeID = ""
	runtime.EmployeeCapabilityOrigin = "https://tomako.ai"
	if err := s.RefreshCapabilityAuthority(runtime); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(s.taskRuntimeEnvFile)
	if !strings.Contains(string(body), "export EMPLOYEE_ID=''") || !strings.Contains(string(body), "export EMPLOYEE_CAPABILITY_ORIGIN='https://tomako.ai'") || strings.Contains(string(body), "employee-b") {
		t.Fatal("authority refresh retained previous employee routing")
	}
	if s.CurrentSessionID() != "thread-1" || s.runtime.EmployeeID != "" {
		t.Fatal("routing refresh must preserve thread identity and clear the runtime default")
	}
}
