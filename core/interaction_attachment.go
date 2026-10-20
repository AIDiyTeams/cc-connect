package core

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// InteractionAttachment is the existing conversation file reference, carried within one answer.
// It is untrusted user data. Reading the file is a separate Agent operation.
type InteractionAttachment struct {
	URL         string `json:"url"`
	FileName    string `json:"fileName"`
	MimeType    string `json:"mimeType"`
	ArtifactUID string `json:"artifactUid,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
}

func SplitInteractionAnswer(value string) (string, []InteractionAttachment, error) {
	const open, close = "<user_attachments>", "</user_attachments>"
	if len([]rune(value)) > 32768 {
		return "", nil, fmt.Errorf("answer is too long")
	}
	value = strings.TrimSpace(value)
	start := strings.Index(value, open)
	if start < 0 {
		return value, nil, nil
	}
	if !strings.HasSuffix(value, close) || strings.Contains(value[start+len(open):], open) {
		return "", nil, fmt.Errorf("invalid answer attachments")
	}
	var files []InteractionAttachment
	if err := json.Unmarshal([]byte(value[start+len(open):len(value)-len(close)]), &files); err != nil || len(files) == 0 || len(files) > 8 {
		return "", nil, fmt.Errorf("invalid answer attachments")
	}
	for _, file := range files {
		u, err := url.Parse(file.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || len([]rune(file.URL)) > 2048 || strings.TrimSpace(file.FileName) == "" || len([]rune(file.FileName)) > 255 || strings.TrimSpace(file.MimeType) == "" || len([]rune(file.MimeType)) > 100 || len([]rune(file.ArtifactUID)) > 255 || file.SizeBytes < 0 {
			return "", nil, fmt.Errorf("invalid answer attachment reference")
		}
	}
	text := strings.TrimSpace(value[:start])
	if text == interactionSkippedAnswer {
		return "", nil, fmt.Errorf("skips cannot contain attachments")
	}
	return text, files, nil
}

func interactionOptionAnswer(question UserQuestion, value string) string {
	text, files, err := SplitInteractionAnswer(value)
	if err != nil {
		return value
	}
	for _, option := range question.Options {
		if text == option.ID || text == option.Label {
			text = option.Label
			break
		}
	}
	if len(files) == 0 {
		return text
	}
	encoded, _ := json.Marshal(files)
	return strings.TrimSpace(text + "\n\n<user_attachments>" + string(encoded) + "</user_attachments>")
}
