package core

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

func interactionResultWithText(pending *pendingPermission, decision string, answers map[string][]string, responseText string) (PermissionResult, error) {
	if responseText == "" {
		return interactionResult(pending, decision, answers)
	}
	if decision != "" || len(answers) != 0 {
		return PermissionResult{}, fmt.Errorf("a text reply cannot include decisions or field values")
	}
	text, files, err := SplitInteractionAnswer(responseText)
	if err != nil {
		return PermissionResult{}, err
	}
	if (strings.TrimSpace(text) == "" && len(files) == 0) || utf8.RuneCountInString(text) > 4000 {
		return PermissionResult{}, fmt.Errorf("invalid text reply")
	}
	// Deny is intentional even for a question: no proposed tool arguments were approved.
	// Adapters return responseText separately or as the tool's explanatory rejection.
	return PermissionResult{Behavior: "deny", Message: "The user replied to the request in their own words. No approval or individual field values were supplied. User reply:\n" + responseText,
		UpdatedInput: map[string]any{"responseText": responseText}}, nil
}
