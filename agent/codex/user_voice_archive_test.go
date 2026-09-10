package codex

import (
	"encoding/json"
	"github.com/chenhg5/cc-connect/core"
	"strings"
	"testing"
)

func TestUserVoiceArchiveToolsAreScopedToDeepSeekSearch(t *testing.T) {
	for _, tc := range []struct {
		scene, model string
		want         bool
	}{
		{"growth_opportunity_user_voice_search", "DEEPSEEK_V4_FLASH_SEARCH", true},
		{"growth_opportunity_user_voice_judge", "DEEPSEEK_V4_FLASH_SEARCH", false},
		{"growth_opportunity_user_voice_search", "GPT_5_6_SOL", false},
		{"", "DEEPSEEK_V4_FLASH_SEARCH", false},
	} {
		s := &appServerSession{runtime: core.SessionRuntime{Scene: tc.scene, LogicalModel: tc.model}}
		got := s.threadRequestParams()["dynamicTools"]
		if tc.want {
			tools, ok := got.([]map[string]any)
			if !ok || len(tools) != 1 || tools[0]["name"] != "search_reddit_archive" {
				t.Fatalf("tools=%#v", got)
			}
		} else if got != nil {
			t.Fatalf("unexpected tools for %s/%s: %#v", tc.scene, tc.model, got)
		}
	}
}

func TestUserVoiceArchiveRepeatCallDoesNotLaunchCollector(t *testing.T) {
	s := &appServerSession{runtime: core.SessionRuntime{Scene: "growth_opportunity_user_voice_search", LogicalModel: "DEEPSEEK_V4_FLASH_SEARCH"}, archiveUsed: true}
	if _, err := s.collectUserVoiceArchive(map[string]any{}); err == nil || !strings.Contains(err.Error(), "already requested") {
		t.Fatalf("err=%v", err)
	}
}

func TestArchiveExecutionReceiptPreservesCoverageWithoutBodies(t *testing.T) {
	raw := `{"source":"arctic_shift","sourceUrl":"https://arctic-shift.photon-reddit.com/api/posts/search","coverage":{"successfulSubredditCount":2,"incomplete":true},"items":[{"selftext":"private-sized body omitted from public trace"}]}`
	result := archiveExecutionReceipt(raw)
	var receipt map[string]any
	if json.Unmarshal([]byte(result), &receipt) != nil || receipt["source"] != "arctic_shift" || strings.Contains(result, "selftext") {
		t.Fatalf("receipt=%s", result)
	}
	if receipt["coverage"].(map[string]any)["incomplete"] != true {
		t.Fatal("coverage gap lost")
	}
}
