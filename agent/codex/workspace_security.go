package codex

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// workspaceSecurityStartupArgs builds process-local overrides for a trusted
// named brand profile. The app-server keeps its existing CODEX_HOME, while its
// sandboxed commands cannot read that home's credentials or session history.
//
// The caller must prepare scratchDir as a real private directory below .tmp
// before launch. brokerPublicDir, when set, must be a private directory outside
// the workspace containing only the session socket and non-secret handles. This
// helper performs no I/O; the caller must reject symlinked directories. Inherited
// profiles are rebuilt by fencedInheritedPermissions so stale host paths and
// local network/socket exceptions cannot survive recursive CLI merging. Broker mode requires the Linux proxy's exact UDS relay and
// read-only child mounts in the matching Codex release.
func workspaceSecurityStartupArgs(profile, workDir, scratchDir, brokerPublicDir, sharedSkillsDir string) ([]string, error) {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return nil, nil
	}
	if strings.HasPrefix(profile, ":") || strings.ContainsAny(profile, "\r\n\x00") {
		return nil, fmt.Errorf("codex: workspace security requires a named host profile")
	}
	if !filepath.IsAbs(workDir) || !filepath.IsAbs(scratchDir) ||
		strings.ContainsAny(workDir+scratchDir, "\r\n\x00") {
		return nil, fmt.Errorf("codex: workspace security requires absolute workspace and scratch paths")
	}
	workDir = filepath.Clean(workDir)
	scratchDir = filepath.Clean(scratchDir)
	tmpDir := filepath.Join(workDir, ".tmp")
	rel, err := filepath.Rel(tmpDir, scratchDir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("codex: workspace security requires a private scratch child")
	}
	scratchKey := filepath.ToSlash(filepath.Join(".tmp", rel))
	quote := func(value string) string {
		data, _ := json.Marshal(value) // JSON quoted strings are valid TOML basic strings.
		return string(data)
	}
	// :minimal includes /etc for libc, TLS and fonts. Hide only deployment
	// credentials and service definitions; Unix owner mode is not a sandbox
	// boundary because the app-server and its commands use the same UID.
	filesystemRules := `, "/etc/cc-connect" = "none", "/etc/systemd" = "none"`
	inside := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if brokerPublicDir != "" {
		if !filepath.IsAbs(brokerPublicDir) || strings.ContainsAny(brokerPublicDir, "\r\n\x00") {
			return nil, fmt.Errorf("codex: workspace security requires an absolute broker public path")
		}
		brokerPublicDir = filepath.Clean(brokerPublicDir)
		// Keep public handles outside the private state trees and forbid
		// granting an ancestor or workspace-wide read.
		if inside(workDir, brokerPublicDir) || inside(brokerPublicDir, workDir) {
			return nil, fmt.Errorf("codex: broker public directory must be outside the workspace and its ancestors")
		}
		filesystemRules += ", " + quote(brokerPublicDir) + " = " + quote("read")
	}
	if sharedSkillsDir != "" {
		if !filepath.IsAbs(sharedSkillsDir) || strings.ContainsAny(sharedSkillsDir, "\r\n\x00") {
			return nil, fmt.Errorf("codex: workspace security requires an absolute shared Skills path")
		}
		sharedSkillsDir = filepath.Clean(sharedSkillsDir)
		if inside(sharedSkillsDir, workDir) || inside(workDir, sharedSkillsDir) {
			return nil, fmt.Errorf("codex: shared Skills must be independent of the workspace")
		}
		// Exact runtime-package exclusions require no per-command tree scan.
		// Inheritance grants only the active shared root. Keep visual caches,
		// examples, platform-facts and public Skill implementation dependencies.
		for _, relative := range []string{
			".git", ".github", ".codex", ".agents", "test", "test-fixtures", "docs", "deploy",
			"AGENTS.md", "CONTRIBUTING.md", "tomako-video-generate.test.mjs",
			"brand-analysis-pipeline.mjs", "onboarding-activation-pipeline.mjs",
			"scripts/__pycache__", "skills/brand-name-finder-kit/scripts/__pycache__",
			"skills/result-writer/scripts/__pycache__",
		} {
			filesystemRules += ", " + quote(filepath.Join(sharedSkillsDir, filepath.FromSlash(relative))) + " = " + quote("none")
		}
	}
	// Codex splits CLI override keys on every dot, without parsing TOML quoted
	// key segments. Keep paths and profile names in the inline table value so
	// dots, quotes and spaces cannot change the permission hierarchy.
	override := fmt.Sprintf(
		"permissions = { %s = { filesystem = { %s = { %s = %s, %s = %s, %s = %s, %s = %s }%s } } }",
		quote(profile), quote(":workspace_roots"),
		quote(".codex"), quote("none"),
		quote(".codex/memories"), quote("write"),
		quote(".tmp"), quote("none"),
		quote(scratchKey), quote("write"),
		filesystemRules,
	)
	args := []string{"-c", override}
	if brokerPublicDir != "" {
		// Keep general public web access, but route it through the managed proxy
		// so direct localhost/private connections cannot bypass capability scope.
		// The proxy alone can connect to this session's immutable socket path.
		network := fmt.Sprintf(
			`features.network_proxy = { enabled = true, proxy_url = "http://127.0.0.1:0", enable_socks5 = false, allow_upstream_proxy = false, allow_local_binding = false, dangerously_allow_all_unix_sockets = false, mode = "full", domains = { "*" = "allow" }, unix_sockets = { %s = "allow" } }`,
			quote(filepath.Join(brokerPublicDir, "cap.sock")),
		)
		args = append(args, "-c", network)
	}
	return args, nil
}
