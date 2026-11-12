package codex

import (
	"fmt"
	"strings"
)

// Only the Agent's opted-in research searches can attach body evidence. The same
// reader validates public DNS, redirects and page status for each URL, and writes
// a receipt under the current session. A failed fetch still leaves the index hits.
func (s *appServerSession) readSearchResults(items []webSearchItem, count int) string {
	script := s.webReadScript()
	urls := make([]string, 0, count)
	seen := make(map[string]bool)
	for _, item := range items {
		target, _, err := s.webReadTarget(map[string]any{"url": item.URL})
		if err != nil {
			continue
		}
		target.Fragment = ""
		url := target.String()
		if !seen[url] {
			urls = append(urls, url)
			seen[url] = true
		}
		if len(urls) == count {
			break
		}
	}
	if len(urls) == 0 {
		return "No public result could be read. Index snippets alone do not verify facts."
	}
	release, err := acquireWebInventorySlot(s.ctx)
	if err != nil {
		return "Original-page reads unavailable. Index snippets alone do not verify facts."
	}
	defer release()
	args := append([]string{script, "--format=agent", "--render=never", "--max-chars=3000"}, urls...)
	body, truncated, err := s.runWebReader(args, webReadOutputLimit)
	if err != nil {
		return fmt.Sprintf("Original-page reads failed for %s: %v. Index snippets alone do not verify facts.", strings.Join(urls, ", "), err)
	}
	s.recordPageRead(urls, body)
	if truncated {
		body += "\n[Original-page output truncated; only visible body excerpts can support a claim.]"
	}
	return body
}
