package codex

import "testing"

func TestBrandEvidencePreservesContactAndPricingContext(t *testing.T) {
	result := brandEvidenceForModel(map[string]any{
		"contacts":      []any{map[string]any{"type": "email", "value": "team@gmail.com", "context": "Recruitment: send your CV", "sourceUrl": "https://brand.test/contact", "platform": "email", "secret": "hidden"}},
		"evidencePages": []any{map[string]any{"url": "https://brand.test/pricing", "pricingEvidence": []any{"Pro USD 6 per user/month billed annually"}, "pricingStatus": "rendered"}},
	})
	contact := result["contacts"].([]any)[0].(map[string]any)
	if contact["context"] != "Recruitment: send your CV" || contact["sourceUrl"] != "https://brand.test/contact" {
		t.Fatalf("contact evidence lost: %#v", contact)
	}
	if _, exists := contact["secret"]; exists {
		t.Fatal("private data leaked")
	}
	page := result["evidencePages"].([]any)[0].(map[string]any)
	if page["pricingEvidence"].([]any)[0] != "Pro USD 6 per user/month billed annually" {
		t.Fatal("pricing terms lost")
	}
	if page["pricingStatus"] != "rendered" {
		t.Fatal("pricing observation status lost")
	}
}

func TestContactPurposePlaceholderIsNotPersistedAsFact(t *testing.T) {
	for _, purpose := range []string{"", "用途待完善", "用途未说明", "Purpose to be added"} {
		if got := normalizeBrandContactPurposes([]any{map[string]any{"type": "email", "value": "team@gmail.com", "purpose": purpose}}); len(got) != 0 {
			t.Fatalf("placeholder accepted: %#v", got)
		}
	}
	got := normalizeBrandContactPurposes([]any{map[string]any{"type": "email", "value": "team@gmail.com", "purpose": "招聘求职", "context": "untrusted extra"}})
	if len(got) != 1 || len(got[0].(map[string]any)) != 3 {
		t.Fatalf("output contract changed: %#v", got)
	}
}

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
