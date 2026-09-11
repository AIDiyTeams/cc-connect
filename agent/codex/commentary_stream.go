package codex

import (
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

type commentaryStream struct {
	text       string
	version    int64
	lastSentAt time.Time
}

// Native public prose streams here until its completed phase is known. Cumulative snapshots
// let adapters replace one note rather than append tokens as separate stages.
// A real delta triggers at most one checkpoint per second; completion always
// flushes. There is no timer, translation call, or fabricated activity.
func (s *appServerSession) emitCommentarySnapshot(itemID, text string, done, provisional bool) {
	if itemID == "" {
		if done {
			s.emit(core.Event{Type: core.EventCommentary, Content: text})
		}
		return
	}
	s.stateMu.Lock()
	if s.commentaryStreams == nil {
		s.commentaryStreams = make(map[string]*commentaryStream)
	}
	stream := s.commentaryStreams[itemID]
	if stream == nil {
		stream = &commentaryStream{}
		s.commentaryStreams[itemID] = stream
	}
	if done {
		stream.text = text
	} else {
		stream.text += text
	}
	now := time.Now()
	if !done && !stream.lastSentAt.IsZero() && now.Sub(stream.lastSentAt) < time.Second {
		s.stateMu.Unlock()
		return
	}
	stream.version++
	stream.lastSentAt = now
	event := core.Event{Type: core.EventCommentary, TraceID: itemID, Content: stream.text,
		ContentVersion: stream.version, ContentDone: done,
		ContentProvisional: provisional}
	if done {
		delete(s.commentaryItems, itemID)
		delete(s.commentaryStreams, itemID)
		if !provisional {
			s.lastCommentary = recentCommentary{itemID: itemID, text: stream.text, version: stream.version, at: now}
		}
	}
	s.stateMu.Unlock()
	s.emit(event)
}

// recentCommentary is the last completed public note; when a tool call follows
// it within a few seconds, it was the Agent's purpose line for that action.
type recentCommentary struct {
	itemID  string
	text    string
	version int64
	at      time.Time
	tagged  bool
}

// stepCaptionWindow bounds how long after a note a tool call may start for the
// note to count as that action's caption. Findings the Agent states and then
// keeps thinking about stay ordinary notes.
const stepCaptionWindow = 4 * time.Second

// tagRecentCommentaryAsStep re-sends the last completed note as a step caption
// when an action starts right after it. Same trace id, next version, so the
// application updates the existing entry instead of adding a second one.
func (s *appServerSession) tagRecentCommentaryAsStep() {
	s.stateMu.Lock()
	last := s.lastCommentary
	if last.itemID == "" || last.tagged || time.Since(last.at) > stepCaptionWindow ||
		len([]rune(strings.TrimSpace(last.text))) > 80 {
		s.stateMu.Unlock()
		return
	}
	s.lastCommentary.tagged = true
	s.stateMu.Unlock()
	s.emit(core.Event{Type: core.EventCommentary, TraceID: last.itemID, Content: last.text,
		ContentVersion: last.version + 1, ContentDone: true, ContentKind: "step"})
}
