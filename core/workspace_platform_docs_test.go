package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinkSharedPlatformFacts(t *testing.T) {
	shared := t.TempDir()
	factsDir := filepath.Join(shared, "platform-facts")
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
	docs := WorkspaceShareOptions{}.Normalize().PlatformDocs
	if err := linkSharedPlatformFacts(ws, shared, docs); err != nil {
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

	factLink := filepath.Join(ws, filepath.FromSlash(docs.TargetRel), "platform-image-generation.md")
	fi, err = os.Lstat(factLink)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("platform fact should be a symlink")
	}

	if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(docs.TargetRel), "README.md")); !os.IsNotExist(err) {
		t.Fatal("README.md must not be linked into user facts")
	}
	if _, err := os.Lstat(filepath.Join(ws, filepath.FromSlash(docs.TargetRel), "notes.md")); !os.IsNotExist(err) {
		t.Fatal("non-platform md must not be linked")
	}

	if err := linkSharedPlatformFacts(ws, shared, docs); err != nil {
		t.Fatal(err)
	}
}

func TestLinkSharedPlatformFactsCustomPaths(t *testing.T) {
	shared := t.TempDir()
	src := filepath.Join(shared, "agent-docs")
	_ = os.MkdirAll(src, 0o755)
	_ = os.WriteFile(filepath.Join(src, "RULES.md"), []byte("# Rules\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "factory-schedule.md"), []byte("# Sched\n"), 0o644)

	ws := t.TempDir()
	docs := PlatformDocsOptions{
		SourceSubdir: "agent-docs",
		AgentsFile:   "RULES.md",
		FilePrefix:   "factory-",
		TargetRel:    ".agent/facts",
	}
	if err := linkSharedPlatformFacts(ws, shared, docs); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(ws, "RULES.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("RULES.md symlink missing: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(ws, ".agent/facts/factory-schedule.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("factory fact symlink missing: %v", err)
	}
}

func TestLinkSharedPlatformFactsReplacesWrongLink(t *testing.T) {
	shared := t.TempDir()
	factsDir := filepath.Join(shared, "platform-facts")
	_ = os.MkdirAll(factsDir, 0o755)
	_ = os.WriteFile(filepath.Join(factsDir, "platform-image-generation.md"), []byte("v2\n"), 0o644)

	ws := t.TempDir()
	docs := WorkspaceShareOptions{}.Normalize().PlatformDocs
	dstDir := filepath.Join(ws, filepath.FromSlash(docs.TargetRel))
	_ = os.MkdirAll(dstDir, 0o755)
	wrong := filepath.Join(ws, "wrong.md")
	_ = os.WriteFile(wrong, []byte("old\n"), 0o644)
	dst := filepath.Join(dstDir, "platform-image-generation.md")
	_ = os.Symlink(wrong, dst)

	if err := linkSharedPlatformFacts(ws, shared, docs); err != nil {
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
	factsDir := filepath.Join(skills, "platform-facts")
	_ = os.MkdirAll(factsDir, 0o755)
	_ = os.WriteFile(filepath.Join(factsDir, "AGENTS.md"), []byte("# A\n"), 0o644)
	_ = os.WriteFile(filepath.Join(factsDir, "platform-image-generation.md"), []byte("# F\n"), 0o644)

	t.Setenv("SKILLS_OL_DIR", skills)
	base := filepath.Join(home, "workspaces")
	_ = os.MkdirAll(base, 0o755)
	ws := filepath.Join(base, "user-9")
	e := &Engine{baseDir: base, workspaceShare: WorkspaceShareOptions{}.Normalize()}
	if err := e.initUserWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(ws, "AGENTS.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("AGENTS.md symlink missing: %v", err)
	}
	docs := e.workspaceShare.PlatformDocs
	fact := filepath.Join(ws, filepath.FromSlash(docs.TargetRel), "platform-image-generation.md")
	if fi, err := os.Lstat(fact); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("platform fact symlink missing: %v", err)
	}
}

func TestIsSharedPlatformFactFile(t *testing.T) {
	docs := WorkspaceShareOptions{}.Normalize().PlatformDocs
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
		if got := isSharedPlatformFactFile(name, docs); got != want {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
}
