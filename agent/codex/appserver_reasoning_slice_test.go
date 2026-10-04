package codex

import (
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func drainEvents(s *appServerSession) []core.Event {
	var events []core.Event
	for {
		select {
		case event := <-s.events:
			events = append(events, event)
		default:
			return events
		}
	}
}

func TestAppServerReasoningDelta_SlicesALongBlockEveryInterval(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	start := time.Date(2026, 10, 4, 4, 15, 16, 0, time.UTC)
	chunk := strings.Repeat("weigh which three questions matter ", 2) // 70 runes per delta

	for i := 0; i < 10; i++ {
		s.handleReasoningDelta("rs-1", chunk, start.Add(time.Duration(i)*time.Second))
	}
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("no slice before the interval has passed: %#v", events)
	}
	s.handleReasoningDelta("rs-1", "now compare topic options", start.Add(10*time.Second))
	events := drainEvents(s)
	if len(events) != 1 {
		t.Fatalf("one slice after the interval, got %d", len(events))
	}
	first := events[0]
	if first.Type != core.EventThinking || !first.ContentPartial || first.ContentKind != "raw" ||
		!strings.HasPrefix(first.Content, chunk) || !strings.HasSuffix(first.Content, "now compare topic options") {
		t.Fatalf("slice must be a raw partial thinking event with the text so far: %#v", first)
	}

	for i := 1; i <= 10; i++ {
		s.handleReasoningDelta("rs-1", "then market and language, then the keyword. ", start.Add(10*time.Second+time.Duration(i)*time.Second))
		if i < 10 {
			if events := drainEvents(s); len(events) != 0 {
				t.Fatalf("second slice waits a full interval, got %#v at +%ds", events, i)
			}
		}
	}
	events = drainEvents(s)
	if len(events) != 1 || strings.Contains(events[0].Content, "weigh which") || !events[0].ContentPartial {
		t.Fatalf("the next slice carries only text written since the previous one: %#v", events)
	}

	s.handleItemCompleted(map[string]any{"id": "rs-1", "type": "reasoning", "summary": []any{},
		"content": []any{map[string]any{"type": "reasoning_text", "text": "the whole block"}}})
	events = drainEvents(s)
	if len(events) != 1 || events[0].ContentPartial || events[0].Content != "the whole block" || events[0].ContentKind != "raw" {
		t.Fatalf("completion still sends the whole block unchanged: %#v", events)
	}
	if s.reasoningSlice != nil {
		t.Fatal("completion releases the slice buffer")
	}
}

func TestAppServerReasoningDelta_ShortOrQuietBlocksAreNotSliced(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	start := time.Date(2026, 10, 4, 4, 15, 16, 0, time.UTC)
	// A short block completes before the interval: only the completed item is sent, as before.
	s.handleReasoningDelta("rs-short", strings.Repeat("x", 5000), start)
	s.handleReasoningDelta("rs-short", "y", start.Add(9*time.Second))
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("blocks under the interval are not sliced: %#v", events)
	}
	// A slow block with too little text keeps accumulating instead of sending a fragment.
	s.handleReasoningDelta("rs-slow", "considering", start)
	s.handleReasoningDelta("rs-slow", " options", start.Add(12*time.Second))
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("fewer than the minimum runes are not sent: %#v", events)
	}
	// Chinese reasoning is counted in runes, not bytes.
	s.handleReasoningDelta("rs-zh", strings.Repeat("比较", 120), start)
	s.handleReasoningDelta("rs-zh", "定价", start.Add(11*time.Second))
	if events := drainEvents(s); len(events) != 0 {
		t.Fatalf("242 Chinese characters stay below the 300-rune minimum: %#v", events)
	}
	s.handleReasoningDelta("rs-zh", strings.Repeat("席位", 40), start.Add(12*time.Second))
	if events := drainEvents(s); len(events) != 1 || !events[0].ContentPartial {
		t.Fatalf("once enough text accumulates the slice is sent: %#v", events)
	}
}

func TestAppServerReasoningDelta_SliceKeepsOnlyTheEndOfVeryLongText(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	start := time.Date(2026, 10, 4, 4, 15, 16, 0, time.UTC)
	s.handleReasoningDelta("rs-long", "HEAD"+strings.Repeat("思", 6000), start)
	s.handleReasoningDelta("rs-long", "TAIL", start.Add(10*time.Second))
	events := drainEvents(s)
	if len(events) != 1 {
		t.Fatalf("one slice expected, got %d", len(events))
	}
	content := events[0].Content
	if strings.Contains(content, "HEAD") || !strings.HasSuffix(content, "TAIL") || len([]rune(content)) != reasoningSliceMaxRunes {
		t.Fatalf("slice keeps the last %d runes: %d runes, head=%v", reasoningSliceMaxRunes, len([]rune(content)), strings.Contains(content, "HEAD"))
	}
}

func TestAppServerReasoningDelta_NewTurnDropsAnUnfinishedSlice(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	start := time.Date(2026, 10, 4, 4, 15, 16, 0, time.UTC)
	s.handleReasoningDelta("rs-cut", strings.Repeat("interrupted thought ", 30), start)
	s.handleNotification("turn/started", []byte(`{"threadId":"t","turn":{"id":"turn-2"}}`))
	if s.reasoningSlice != nil {
		t.Fatal("an interrupted block must not leak into the next turn")
	}
	s.handleNotification("item/reasoning/textDelta", []byte(`{"itemId":"rs-next","delta":"fresh"}`))
	if s.reasoningSlice == nil || s.reasoningSlice.itemID != "rs-next" || s.reasoningSlice.text.String() != "fresh" {
		t.Fatalf("the delta notification feeds the slice: %#v", s.reasoningSlice)
	}
}
