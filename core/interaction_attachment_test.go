package core

import (
	"strings"
	"testing"
)

const answerImage = `<user_attachments>[{"url":"https://files.example.com/chart.png","fileName":"chart.png","mimeType":"image/png"}]</user_attachments>`

func TestInteractionAttachmentSurvivesChoiceAndFormMapping(t *testing.T) {
	q := UserQuestion{ID: "mode", Question: "Mode?", Options: []UserQuestionOption{{ID: "review", Label: "Review first"}}}
	for _, form := range []bool{false, true} {
		if form {
			q.InputType = "single_choice"
		}
		result, err := interactionResult(&pendingPermission{Questions: []UserQuestion{q}}, "", map[string][]string{"mode": {"review\n" + answerImage}})
		if err != nil {
			t.Fatal(err)
		}
		values := result.UpdatedInput["answers"].(map[string]any)
		var answer string
		if form {
			answer = values["mode"].([]string)[0]
		} else {
			answer = values["mode"].(string)
		}
		text, files, err := SplitInteractionAnswer(answer)
		if err != nil || text != "Review first" || len(files) != 1 || files[0].FileName != "chart.png" {
			t.Fatalf("answer=%s err=%v", answer, err)
		}
	}
}

func TestInteractionAttachmentRejectsBrokenAndUnsafeReferences(t *testing.T) {
	for _, value := range []string{strings.ReplaceAll(answerImage, "https://files.example.com/chart.png", "javascript:bad"), answerImage[:len(answerImage)-1], answerImage + answerImage, interactionSkippedAnswer + answerImage} {
		if _, _, err := SplitInteractionAnswer(value); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
