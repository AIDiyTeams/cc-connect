package core

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	mediaHarvestMaxBytes   = 8 << 20 // 8 MiB
	mediaHarvestMaxFiles   = 5
	mediaHarvestMaxDepth   = 4
	mediaHarvestScanBudget = 2000 // max filesystem entries visited per scan
)

var mediaHarvestImageExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

var mediaHarvestSkipDir = map[string]bool{
	".git":        true,
	".cc-connect": true,
	"node_modules": true,
	".codex":      true,
	".agents":     true,
	"__pycache__": true,
	".venv":       true,
	"venv":        true,
}

// mediaFileMeta is a lightweight workspace file fingerprint used for turn diffs.
type mediaFileMeta struct {
	Size    int64
	ModTime time.Time
}

// mediaFileSnapshot maps workspace-relative paths to fingerprints.
type mediaFileSnapshot map[string]mediaFileMeta

// mediaHarvestKey identifies a delivered image for de-duplication within a turn.
func mediaHarvestKey(fileName string, size int64) string {
	return strings.ToLower(strings.TrimSpace(fileName)) + "|" + itoa64(size)
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// snapshotMediaFiles walks workDir for image files (bounded depth/count).
func snapshotMediaFiles(workDir string) mediaFileSnapshot {
	out := make(mediaFileSnapshot)
	if strings.TrimSpace(workDir) == "" {
		return out
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return out
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return out
	}

	visited := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		visited++
		if visited > mediaHarvestScanBudget {
			return fs.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return fs.SkipDir
			}
			depth := strings.Count(rel, string(os.PathSeparator)) + 1
			if depth > mediaHarvestMaxDepth {
				return fs.SkipDir
			}
			if strings.HasPrefix(name, ".") || mediaHarvestSkipDir[name] {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if _, ok := mediaHarvestImageExt[ext]; !ok {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		fi, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		if fi.Size() <= 0 || fi.Size() > mediaHarvestMaxBytes {
			return nil
		}
		out[filepath.ToSlash(rel)] = mediaFileMeta{Size: fi.Size(), ModTime: fi.ModTime()}
		return nil
	})
	return out
}

// diffNewOrChangedMedia returns image paths created/changed since before.
func diffNewOrChangedMedia(before, after mediaFileSnapshot) []string {
	if len(after) == 0 {
		return nil
	}
	var out []string
	for rel, meta := range after {
		prev, ok := before[rel]
		if !ok || prev.Size != meta.Size || !prev.ModTime.Equal(meta.ModTime) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	if len(out) > mediaHarvestMaxFiles {
		out = out[:mediaHarvestMaxFiles]
	}
	return out
}

func mimeForImagePath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if mt, ok := mediaHarvestImageExt[ext]; ok {
		return mt
	}
	return "application/octet-stream"
}

// loadHarvestImages reads changed image files from workDir into ImageAttachments.
// alreadySent keys (fileName|size) are skipped to avoid double-delivery after
// an explicit `cc-connect send --image`.
func loadHarvestImages(workDir string, relPaths []string, alreadySent map[string]bool) []ImageAttachment {
	if len(relPaths) == 0 {
		return nil
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return nil
	}
	absPaths := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		rel = filepath.Clean(filepath.FromSlash(rel))
		if rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		abs := filepath.Join(root, rel)
		if !strings.HasPrefix(abs, root+string(os.PathSeparator)) && abs != root {
			continue
		}
		absPaths = append(absPaths, abs)
	}
	return loadImagesFromAbsolutePaths(absPaths, alreadySent)
}

var (
	// ![alt](/abs/or/rel/path.png) — agents often write /tmp/... which browsers 404.
	mdLocalImageRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)\)`)
	// `foo.png` or `/tmp/foo.png` in backticks
	backtickImageRe = regexp.MustCompile("`([^`\\n]+\\.(?i:png|jpe?g|webp|gif))`")
)

// extractLocalImagePaths finds filesystem image paths mentioned in assistant text.
// Resolves relative paths against workDir. Absolute paths under workDir or /tmp are kept.
func extractLocalImagePaths(text, workDir string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		raw = strings.Trim(raw, `"'`)
		if raw == "" || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "data:") {
			return
		}
		ext := strings.ToLower(filepath.Ext(raw))
		if _, ok := mediaHarvestImageExt[ext]; !ok {
			return
		}
		abs := resolveHarvestableImagePath(raw, workDir)
		if abs == "" || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, m := range mdLocalImageRe.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range backtickImageRe.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	if len(out) > mediaHarvestMaxFiles {
		out = out[:mediaHarvestMaxFiles]
	}
	return out
}

// resolveHarvestableImagePath returns an absolute readable image path, or "".
// Allowed roots: workDir and the process temp dir (/tmp).
func resolveHarvestableImagePath(raw, workDir string) string {
	raw = filepath.Clean(raw)
	var abs string
	if filepath.IsAbs(raw) {
		abs = raw
	} else if workDir != "" {
		root, err := filepath.Abs(workDir)
		if err != nil {
			return ""
		}
		abs = filepath.Join(root, raw)
	} else {
		return ""
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return ""
	}
	if !isAllowedHarvestPath(abs, workDir) {
		return ""
	}
	fi, err := os.Stat(abs)
	if err != nil || fi.IsDir() || fi.Size() <= 0 || fi.Size() > mediaHarvestMaxBytes {
		return ""
	}
	return abs
}

func isAllowedHarvestPath(abs, workDir string) bool {
	tmp := os.TempDir()
	if tmp != "" {
		tmpAbs, err := filepath.Abs(tmp)
		if err == nil && (abs == tmpAbs || strings.HasPrefix(abs, tmpAbs+string(os.PathSeparator))) {
			return true
		}
	}
	// Also allow literal /tmp on Unix even if TempDir differs.
	if strings.HasPrefix(abs, "/tmp"+string(os.PathSeparator)) || abs == "/tmp" {
		return true
	}
	if workDir == "" {
		return false
	}
	root, err := filepath.Abs(workDir)
	if err != nil {
		return false
	}
	return abs == root || strings.HasPrefix(abs, root+string(os.PathSeparator))
}

// loadImagesFromAbsolutePaths reads absolute image files into attachments.
func loadImagesFromAbsolutePaths(absPaths []string, alreadySent map[string]bool) []ImageAttachment {
	var images []ImageAttachment
	for _, abs := range absPaths {
		data, err := os.ReadFile(abs)
		if err != nil {
			slog.Debug("media harvest: read failed", "path", abs, "error", err)
			continue
		}
		if len(data) == 0 || int64(len(data)) > mediaHarvestMaxBytes {
			continue
		}
		fileName := filepath.Base(abs)
		key := mediaHarvestKey(fileName, int64(len(data)))
		if alreadySent != nil && alreadySent[key] {
			continue
		}
		images = append(images, ImageAttachment{
			MimeType: mimeForImagePath(abs),
			Data:     data,
			FileName: fileName,
		})
		if alreadySent != nil {
			alreadySent[key] = true
		}
		if len(images) >= mediaHarvestMaxFiles {
			break
		}
	}
	return images
}

// stripLocalImageMarkdown removes ![alt](/local/path.png) so browsers don't 404
// on agent workspace paths; the binary is delivered via the image event instead.
func stripLocalImageMarkdown(text string) string {
	if text == "" {
		return text
	}
	return mdLocalImageRe.ReplaceAllStringFunc(text, func(m string) string {
		sub := mdLocalImageRe.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		src := strings.TrimSpace(sub[1])
		if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "data:") {
			return m
		}
		ext := strings.ToLower(filepath.Ext(src))
		if _, ok := mediaHarvestImageExt[ext]; !ok {
			return m
		}
		// Drop the broken markdown image entirely (binary goes out-of-band).
		return ""
	})
}
