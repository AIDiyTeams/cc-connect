package core

import (
	"os"
	"path/filepath"
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

func TestLoadHarvestImagesRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "evil.png")
	if err := os.WriteFile(outside, []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	images := loadHarvestImages(dir, []string{"../" + filepath.Base(filepath.Dir(outside)) + "/" + filepath.Base(outside)}, nil)
	if len(images) != 0 {
		t.Fatalf("expected traversal reject, got %#v", images)
	}
}
