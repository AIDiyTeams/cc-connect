package codex

import (
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestPageReadReceiptCarriesTheReaderTextOnlyForResearchTurns(t *testing.T) {
	s := webSearchTestSession(t)
	s.runtime.ToolProfile = ""
	s.recordPageRead([]string{"https://a1.art/"}, "[Web page text.]\nURL: https://a1.art/")
	if receipts := drainUsageReceipts(s); len(receipts) != 0 {
		t.Fatalf("an ordinary conversation must not ship page text: %v", receipts)
	}
	s.runtime.ToolProfile = core.ToolProfilePublicResearch
	s.recordPageRead([]string{"https://a1.art/"}, "")
	s.recordPageRead(nil, "text")
	s.recordPageRead([]string{"https://a1.art/"}, "[Web page text.]\nURL: https://a1.art/")
	receipts := drainUsageReceipts(s)
	if len(receipts) != 1 {
		t.Fatalf("receipts=%v", receipts)
	}
	got := receipts[0]
	urls, _ := got["requested_urls"].([]string)
	if got["kind"] != webReadUsageKind || len(urls) != 1 || urls[0] != "https://a1.art/" || got["output"] != "[Web page text.]\nURL: https://a1.art/" {
		t.Fatalf("receipt=%v", got)
	}
}
