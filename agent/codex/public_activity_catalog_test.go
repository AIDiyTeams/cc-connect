package codex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommandActivityLabelUsesDefaultCatalogForKnownScriptsAndSkills(t *testing.T) {
	cases := map[string]string{
		`cd /home/ubuntu/workspaces/test && node /home/ubuntu/Skills-OL-test/tomako-document.mjs --title "PRD" --stdin`: "document",
		`sed -n '1,80p' /home/ubuntu/Skills-OL-test/skills/deep-analysis/SKILL.md`:                                      "analysis",
		`python3 "$SKILLS_OL_DIR/scripts/signals-web-search.py" --query x`:                                              "web_search",
		`node reddit-arctic-shift-search.mjs --subreddit politics`:                                                      "reddit",
		`ls -la && cat README.md`:                                  "",
		`curl -s https://example.com/skills/deep-analysis/ignored`: "analysis",
	}
	for command, want := range cases {
		if got := commandActivityLabel(command); got != want {
			t.Errorf("%q: label %q, want %q", command, got, want)
		}
	}
	step := commandPublicActivity(`node /home/ubuntu/Skills-OL-test/tomako-document.mjs --title x`, "running")
	if step == nil || step.Kind != "command" || step.Label != "document" || step.URL != "" {
		t.Fatalf("command receipt must carry the category only: %+v", step)
	}
}

func TestCommandActivityLabelPrefersDeclarationsInTheReferencedCheckout(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Skills-OL-test")
	if err := os.MkdirAll(filepath.Join(root, "skills", "my-skill", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "public-activities.json"),
		[]byte(`{"scripts":{"tomako-document.mjs":"custom_doc","weird.mjs":"Bad Label!"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", "my-skill", "SKILL.md"),
		[]byte("---\nname: my-skill\npublic_activity: \"community\"\n---\n\n# body\npublic_activity: ignored_in_body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := commandActivityLabel(`node ` + filepath.Join(root, "tomako-document.mjs")); got != "custom_doc" {
		t.Fatalf("root declaration must win over the default catalog: %q", got)
	}
	if got := commandActivityLabel(`python3 ` + filepath.Join(root, "skills", "my-skill", "scripts", "run.py")); got != "community" {
		t.Fatalf("front matter label must be used: %q", got)
	}
	if got := commandActivityLabel(`node ` + filepath.Join(root, "weird.mjs")); got != "" {
		t.Fatalf("invalid labels are rejected: %q", got)
	}
}
