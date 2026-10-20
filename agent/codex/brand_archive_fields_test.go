package codex

import "testing"

func TestBrandArchiveFieldsSurviveStageNormalization(t *testing.T) {
	_, result, err := validateBrandAnalysisStage(map[string]any{
		"stage": "core", "result": map[string]any{
			"brandName": "Example", "productType": "SaaS", "platforms": []any{"Web"},
			"keyFeatures": []any{"Create", "Review", "Publish"}, "businessModel": "Subscription", "pricing": "USD 20 per month",
			"audienceSegments": []any{map[string]any{"name": "Founder", "task": "Find buyers", "alternative": "Manual work", "channels": []any{"Community"}, "channelDetails": map[string]any{"Community": "Launch discussions"}}},
			"contactPurposes":  []any{map[string]any{"type": "email", "value": "help@example.com", "purpose": "Customer support"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["businessModel"] != "Subscription" || result["pricing"] != "USD 20 per month" {
		t.Fatalf("commercial facts lost: %#v", result)
	}
	persona := result["audienceSegments"].([]any)[0].(map[string]any)
	if persona["task"] != "Find buyers" || persona["alternative"] != "Manual work" {
		t.Fatalf("persona facts lost: %#v", persona)
	}
	if persona["channelDetails"].(map[string]any)["Community"] != "Launch discussions" {
		t.Fatal("channel details lost")
	}
	if result["contactPurposes"].([]any)[0].(map[string]any)["purpose"] != "Customer support" {
		t.Fatal("purpose lost")
	}
}

func TestBrandEvidenceIncludesCollectedContactIdentityWithoutPrivateFields(t *testing.T) {
	result := brandEvidenceForModel(map[string]any{"contacts": []any{map[string]any{"type": "email", "value": "help@example.com", "secret": "not for model"}}})
	contact := result["contacts"].([]any)[0].(map[string]any)
	if contact["value"] != "help@example.com" {
		t.Fatal("identity missing")
	}
	if _, found := contact["secret"]; found {
		t.Fatal("unapproved field exposed")
	}
}
