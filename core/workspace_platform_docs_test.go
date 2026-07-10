package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkSharedPlatformFacts(t *testing.T) {
	shared := t.TempDir()
	factsDir := filepath.Join(shared, sharedPlatformFactsDirName)
	if err := os.MkdirAll(factsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factsDir, "AGENTS.md"), []byte("# Shared Agents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factsDir, "platform-image-generation.md"), []byte("# Image Gen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factsDir, "README.md"), []byte("ignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(factsDir, "notes.md"), []byte("not platform\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := t.TempDir()
	if err := linkSharedPlatformFacts(ws, shared); err != nil {
		t.Fatal(err)
	}

	agentsLink := filepath.Join(ws, "AGENTS.md")
	fi, err := os.Lstat(agentsLink)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("AGENTS.md should be a symlink")
	}
	target, _ := os.Readlink(agentsLink)
	if filepath.Base(target) != "AGENTS.md" {
		t.Fatalf("unexpected AGENTS.md target %q", target)
	}

	factLink := filepath.Join(ws, filepath.FromSlash(workspacePlatformFactsRel), "platform-image-generation.md")
	fi, err = os.Lstat(factLink)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("platform fact should be a symlink")
	}

	// README / non-platform-*.md must not be linked into facts/
	if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(workspacePlatformFactsRel), "README.md")); !os.IsNotExist(err) {
		t.Fatal("README.md must not be linked into user facts")
	}
	if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(workspacePlatformFactsRel), "notes.md")); !os.IsNotExist(err) {
		t.Fatal("non-platform md must not be linked")
	}

	// Idempotent
	if err := linkSharedPlatformFacts(ws, shared); err != nil {
		t.Fatal(err)
	}
}

func TestLinkSharedPlatformFactsReplacesWrongLink(t *testing.T) {
	shared := t.TempDir()
	factsDir := filepath.Join(shared, sharedPlatformFactsDirName)
	_ = os.MkdirAll(factsDir, 0o755)
	_ = os.WriteFile(filepath.Join(factsDir, "platform-image-generation.md"), []byte("v2\n"), 0o644)

	ws := t.TempDir()
	dstDir := filepath.Join(ws, filepath.FromSlash(workspacePlatformFactsRel))
	_ = os.MkdirAll(dstDir, 0o755)
	wrong := filepath.Join(ws, "wrong.md")
	_ = os.WriteFile(wrong, []byte("old\n"), 0o644)
	dst := filepath.Join(dstDir, "platform-image-generation.md")
	_ = os.Symlink(wrong, dst)

	if err := linkSharedPlatformFacts(ws, shared); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(dst)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) == filepath.Clean(wrong) {
		t.Fatalf("should have replaced wrong symlink, still %q", got)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "v2\n" {
		t.Fatalf("resolved content = %q", body)
	}
}

func TestInitUserWorkspaceLinksPlatformFacts(t *testing.T) {
	home := t.TempDir()
	skills := filepath.Join(home, "Skills-OL")
	factsDir := filepath.Join(skills, sharedPlatformFactsDirName)
	_ = os.MkdirAll(factsDir, 0o755)
	_ = os.WriteFile(filepath.Join(factsDir, "AGENTS.md"), []byte("# A\n"), 0o644)
	_ = os.WriteFile(filepath.Join(factsDir, "platform-image-generation.md"), []byte("# F\n"), 0o644)

	t.Setenv("SKILLS_OL_DIR", skills)
	base := filepath.Join(home, "workspaces")
	_ = os.MkdirAll(base, 0o755)
	ws := filepath.Join(base, "user-9")
	if err := initUserWorkspace(ws, base); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(ws, "AGENTS.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("AGENTS.md symlink missing: %v", err)
	}
	fact := filepath.Join(ws, filepath.FromSlash(workspacePlatformFactsRel), "platform-image-generation.md")
	if fi, err := os.Lstat(fact); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("platform fact symlink missing: %v", err)
	}
}

func TestIsSharedPlatformFactFile(t *testing.T) {
	cases := map[string]bool{
		"platform-image-generation.md": true,
		"platform-foo.MD":              true,
		"README.md":                    false,
		"AGENTS.md":                    false,
		"agents.md":                    false,
		"notes.md":                     false,
		"platform-foo.txt":             false,
	}
	for name, want := range cases {
		if got := isSharedPlatformFactFile(name); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}
