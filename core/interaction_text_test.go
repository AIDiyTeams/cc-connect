package core

import "testing"

func TestInteractionTextDoesNotInventFieldValuesOrApproval(t *testing.T) {
	for _, pending := range []*pendingPermission{
		{Questions: []UserQuestion{{ID: "budget", InputType: "number"}, {ID: "site", InputType: "url"}}},
		{},
	} {
		result, err := interactionResultWithText(pending, "", nil, "不要执行，我想先讨论范围")
		if err != nil {
			t.Fatal(err)
		}
		if result.Behavior != "deny" || result.UpdatedInput["responseText"] != "不要执行，我想先讨论范围" {
			t.Fatalf("unsafe reply: %#v", result)
		}
		if _, exists := result.UpdatedInput["answers"]; exists {
			t.Fatal("text must not become field values")
		}
	}
	if _, err := interactionResultWithText(&pendingPermission{}, "allow", nil, "yes"); err == nil {
		t.Fatal("mixed consent accepted")
	}
	if _, err := interactionResultWithText(&pendingPermission{}, "", nil, "  "); err == nil {
		t.Fatal("blank reply accepted")
	}
}

func TestWholeFormTextResumesWaitingAgentWithoutFieldValues(t *testing.T) {
	e, rec := formInteractionEngine(t, []UserQuestion{{ID: "budget", InputType: "number", Required: true}, {ID: "site", InputType: "url", Required: true}})
	raw := "Please explain the scope first."
	if err := e.RespondInteractionTextWithAuthority("test:chat:user1", "interaction-form", "", nil, nil, raw); err != nil {
		t.Fatal(err)
	}
	if rec.lastID != "form-call-1" || rec.lastResult.Behavior != "deny" || rec.lastResult.UpdatedInput["responseText"] != raw {
		t.Fatalf("reply not delivered safely: %#v", rec.lastResult)
	}
	if _, exists := rec.lastResult.UpdatedInput["answers"]; exists {
		t.Fatal("fabricated field answers")
	}
	if err := e.RespondInteractionTextWithAuthority("test:chat:user1", "interaction-form", "", nil, nil, raw); err == nil {
		t.Fatal("resolved request delivered twice")
	}
}
