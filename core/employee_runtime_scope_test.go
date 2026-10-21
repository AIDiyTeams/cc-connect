package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmployeeRuntimeScopeDecodesOnlyStructuredControlPlaneFields(t *testing.T) {
	var runtime SessionRuntime
	if err := json.Unmarshal([]byte(`{"employee_id":" employee-a ","employee_capability_origin":" https://test.tomako.ai ","employee_command_capability_token":"token"}`), &runtime); err != nil {
		t.Fatal(err)
	}
	runtime = normalizeSessionRuntime(runtime)
	if runtime.EmployeeID != "employee-a" || runtime.EmployeeCapabilityOrigin != "https://test.tomako.ai" || runtime.EmployeeCommandCapabilityToken != "token" {
		t.Fatalf("employee runtime routing was lost: %#v", runtime)
	}
	for _, invalid := range []SessionRuntime{
		{EmployeeID: "employee-a\nInjected", EmployeeCapabilityOrigin: "https://test.tomako.ai\nInjected"},
		{EmployeeID: strings.Repeat("a", 97), EmployeeCapabilityOrigin: "https://" + strings.Repeat("a", 513)},
	} {
		got := normalizeSessionRuntime(invalid)
		if got.EmployeeID != "" || got.EmployeeCapabilityOrigin != "" {
			t.Fatal("invalid routing must be dropped whole, never truncated to a different target")
		}
	}
}
