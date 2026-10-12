package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The runtime package owns the catalog, not the bridge or the user's wording.
// Other task kinds keep their original skills. A configured workbench package
// must carry a valid policy; an old or malformed package cannot silently widen it.
func workbenchExcludedSkillPaths(sharedDir string, workbench bool) ([]string, error) {
	if !workbench {
		return nil, nil
	}
	root, err := filepath.EvalSymlinks(sharedDir)
	if err != nil || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("codex: invalid shared skills root")
	}
	data, err := os.ReadFile(filepath.Join(root, "workbench-skills.json"))
	if err != nil {
		return nil, fmt.Errorf("codex: workbench skill policy unavailable: %w", err)
	}
	var policy struct {
		Version  int      `json:"version"`
		Excluded []string `json:"excluded"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if len(data) > 65536 || decoder.Decode(&policy) != nil || policy.Version != 1 {
		return nil, fmt.Errorf("codex: invalid workbench skill policy")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("codex: trailing workbench skill policy data")
	}
	seen := make(map[string]bool)
	paths := make([]string, 0, len(policy.Excluded))
	for _, name := range policy.Excluded {
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || seen[name] {
			return nil, fmt.Errorf("codex: invalid or duplicate workbench skill identifier")
		}
		seen[name] = true
		path := filepath.Join(root, "skills", name)
		canonical, err := filepath.EvalSymlinks(filepath.Join(path, "SKILL.md"))
		if err != nil || canonical != filepath.Join(path, "SKILL.md") {
			return nil, fmt.Errorf("codex: workbench skill must be a real package document: %s", name)
		}
		paths = append(paths, path)
	}
	return paths, nil
}
