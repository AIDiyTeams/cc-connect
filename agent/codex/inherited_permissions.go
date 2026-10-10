package codex

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// fencedInheritedPermissions copies provider routing while rebuilding the
// selected brand profile from its runtime contract. Host permission tables can
// contain obsolete shared checkouts, wider parents or inherited profiles; none
// of those paths are session capabilities. Codex grants its own helper files.
func fencedInheritedPermissions(config, profile, sharedSkillsDir string) (string, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return config, nil
	}
	if strings.HasPrefix(profile, ":") || strings.ContainsAny(profile, "\r\n\x00") {
		return "", fmt.Errorf("codex: fenced inheritance requires a named profile")
	}
	var root map[string]any
	if _, err := toml.Decode(config, &root); err != nil {
		return "", fmt.Errorf("codex: decode inherited runtime config: %w", err)
	}
	permissions, _ := root["permissions"].(map[string]any)
	selected, ok := permissions[profile].(map[string]any)
	if !ok {
		return "", fmt.Errorf("codex: inherited permission profile %q is missing", profile)
	}
	filesystem := map[string]any{
		":minimal": "read",
		":workspace_roots": map[string]any{
			".": "write", ".codex": "none", ".codex/memories": "write", ".tmp": "none",
		},
	}
	if dir := strings.TrimSpace(sharedSkillsDir); dir != "" {
		if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "\r\n\x00") || filepath.Clean(dir) == string(filepath.Separator) {
			return "", fmt.Errorf("codex: fenced inheritance requires an absolute shared Skills directory")
		}
		filesystem[filepath.Clean(dir)] = "read"
	}
	// Preserve the host's network enablement, without inheriting domain/socket
	// exceptions. Process-local startup args own the scoped managed proxy.
	network, _ := selected["network"].(map[string]any)
	enabled, _ := network["enabled"].(bool)
	root["default_permissions"] = profile
	root["permissions"] = map[string]any{profile: map[string]any{
		"filesystem": filesystem,
		"network":    map[string]any{"enabled": enabled},
	}}
	var output bytes.Buffer
	if err := toml.NewEncoder(&output).Encode(root); err != nil {
		return "", fmt.Errorf("codex: encode inherited runtime config: %w", err)
	}
	return strings.TrimSpace(output.String()), nil
}
