package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAndDiffMediaFiles(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "meme.png")
	if err := os.WriteFile(pngPath, []byte("png-bytes-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nested skip dirs should be ignored.
	_ = os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "node_modules", "x.png"), []byte("skip"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("text"), 0o644)

	before := snapshotMediaFiles(dir)
	if len(before) != 1 {
		t.Fatalf("before snapshot size = %d, want 1; got %#v", len(before), before)
	}
	if _, ok := before["meme.png"]; !ok {
		t.Fatalf("missing meme.png in snapshot: %#v", before)
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(pngPath, []byte("png-bytes-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "poster.jpg"), []byte("jpg-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	after := snapshotMediaFiles(dir)
	changed := diffNewOrChangedMedia(before, after)
	if len(changed) != 2 {
		t.Fatalf("changed = %#v, want 2 entries", changed)
	}
	images := loadHarvestImages(dir, changed, nil)
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2", len(images))
	}

	already := map[string]bool{
		mediaHarvestKey("meme.png", int64(len("png-bytes-changed"))): true,
	}
	images = loadHarvestImages(dir, changed, already)
	if len(images) != 1 || images[0].FileName != "poster.jpg" {
		t.Fatalf("dedupe failed: %#v", images)
	}
}

func TestExtractLocalImagePathsFromReply(t *testing.T) {
	dir := t.TempDir()
	wsPng := filepath.Join(dir, "ws.png")
	if err := os.WriteFile(wsPng, []byte("ws-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpPng := filepath.Join(os.TempDir(), "harvest-test-meme.png")
	if err := os.WriteFile(tmpPng, []byte("tmp-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpPng) })

	text := "preview:\n\n![Meme](" + tmpPng + ")\n\nand also `" + filepath.Base(wsPng) + "`\n"
	paths := extractLocalImagePaths(text, dir)
	if len(paths) < 1 {
		t.Fatalf("expected paths, got %#v", paths)
	}
	foundTmp := false
	foundWS := false
	for _, p := range paths {
		if p == tmpPng {
			foundTmp = true
		}
		if p == wsPng {
			foundWS = true
		}
	}
	if !foundTmp {
		t.Fatalf("missing tmp path in %#v", paths)
	}
	if !foundWS {
		t.Fatalf("missing workspace basename path in %#v", paths)
	}

	stripped := stripLocalImageMarkdown("hi\n\n![x](/tmp/nope.png)\n\nok ![y](https://cdn.example/a.png)")
	if strings.Contains(stripped, "/tmp/nope.png") {
		t.Fatalf("local md image not stripped: %q", stripped)
	}
	if !strings.Contains(stripped, "https://cdn.example/a.png") {
		t.Fatalf("remote md image should remain: %q", stripped)
	}
}
