package codex

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestWholeFormTextIsReturnedWithoutTypedFieldValues(t *testing.T) {
	result := core.PermissionResult{Behavior: "deny", UpdatedInput: map[string]any{"responseText": "先解释为什么需要预算，不要填数字"}}
	raw, ok := formToolResult([]core.UserQuestion{{ID: "budget", InputType: "number"}}, result)
	if !ok {
		t.Fatal(raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["status"] != "replied" || decoded["responseText"] != result.UpdatedInput["responseText"] {
		t.Fatal(raw)
	}
	if _, exists := decoded["values"]; exists {
		t.Fatal("invented fields")
	}
	raw, ok = browserHelpResult(core.UserQuestion{ID: "browser"}, result)
	if !ok || !strings.Contains(raw, `"status":"replied"`) {
		t.Fatal(raw)
	}
}
