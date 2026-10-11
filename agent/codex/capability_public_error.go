package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// This is a transport schema, not a list of business errors. Portal owns which
// operations may expose a public explanation and authoritative recovery values.
func validBrokerPublicError(body []byte, status int) bool {
	var public struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    *struct {
			CurrentRevision         *int64 `json:"currentRevision"`
			CurrentOffset           *int64 `json:"currentOffset"`
			PendingProposalRevision *int64 `json:"pendingProposalRevision"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&public) != nil || decoder.Decode(new(any)) != io.EOF ||
		public.Code != status || strings.TrimSpace(public.Message) == "" || utf8.RuneCountInString(public.Message) > 1024 {
		return false
	}
	if public.Data != nil {
		for _, value := range []*int64{public.Data.CurrentRevision, public.Data.CurrentOffset, public.Data.PendingProposalRevision} {
			if value != nil && *value < 0 {
				return false
			}
		}
	}
	return true
}

// brokerClientErrorMessage keeps only the person-facing message of a Portal
// client error; data, paths and any other fields are dropped.
func brokerClientErrorMessage(body []byte, status int) []byte {
	var portal struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &portal) != nil {
		return nil
	}
	message := strings.TrimSpace(portal.Message)
	if message == "" || utf8.RuneCountInString(message) > 1024 {
		return nil
	}
	result, err := json.Marshal(struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{status, message})
	if err != nil {
		return nil
	}
	return result
}
