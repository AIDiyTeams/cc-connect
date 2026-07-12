package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// ensureCodexProviderConfig writes or updates a [model_providers.<name>] section
// in $CODEX_HOME/config.toml so that Codex CLI can use the provider's wire_api
// and http_headers settings.
func ensureCodexProviderConfig(codexHome, name, baseURL, wireAPI string, headers map[string]string) error {
	if name == "" {
		return nil
	}
	home, err := resolveCodexHomeForConfig(codexHome)
	if err != nil {
		return fmt.Errorf("codex: resolve codex home: %w", err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("codex: mkdir codex home: %w", err)
	}

	cfgPath := filepath.Join(home, "config.toml")
	raw, _ := os.ReadFile(cfgPath)
	content := string(raw)

	section := buildProviderSection(name, baseURL, wireAPI, headers)
	updated := upsertProviderSection(content, name, section)

	if err := os.WriteFile(cfgPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("codex: write config.toml: %w", err)
	}
	slog.Debug("codex: wrote provider config", "provider", name, "path", cfgPath)
	return nil
}

// ensureCodexAuth writes $CODEX_HOME/auth.json with the provider's API key,
// matching cc-switch's approach: {"OPENAI_API_KEY": "...", "auth_mode": "api_key"}.
// This is the standard way to authenticate Codex CLI with third-party providers.
func ensureCodexAuth(codexHome, apiKey string) error {
	if apiKey == "" {
		return nil
	}
	home, err := resolveCodexHomeForConfig(codexHome)
	if err != nil {
		return fmt.Errorf("codex: resolve codex home: %w", err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("codex: mkdir codex home: %w", err)
	}

	authPath := filepath.Join(home, "auth.json")
	payload := map[string]any{
		"OPENAI_API_KEY": apiKey,
		"auth_mode":      "apikey",
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("codex: marshal auth.json: %w", err)
	}
	if err := os.WriteFile(authPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("codex: write auth.json: %w", err)
	}
	slog.Debug("codex: wrote auth.json", "path", authPath)
	return nil
}

func resolveCodexHomeForConfig(explicit string) (string, error) {
	if h := strings.TrimSpace(explicit); h != "" {
		return h, nil
	}
	if h := strings.TrimSpace(os.Getenv("CODEX_HOME")); h != "" {
		return h, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".codex"), nil
}

// ensureCodexHomeInheritedConfig ensures a per-user codex_home shares the host's
// provider/cc-switch configuration without requiring every cc-connect project to
// re-declare providers.
//
//   - auth.json is symlinked to the global one. Codex only reads it, so a shared
//     symlink is safe and rotated keys propagate to all per-user workspaces.
//   - config.toml CANNOT be symlinked: Codex writes per-user [projects.*] trust
//     entries into it on startup, which would funnel every user's trust into the
//     shared global file. Instead, the provider routing config (model_provider,
//     model, [model_providers.*], etc.) is synced from global on every
//     StartSession — per-user trust entries are preserved. Editing the global
//     config.toml's provider section thus takes effect for every user on its next
//     session, giving a single source of truth equivalent to a symlink.
//
// Later ensureCodexProviderConfig / ensureCodexAuth calls can still upsert
// per-session overrides on top of the inherited baseline.
func ensureCodexHomeInheritedConfig(codexHome string) error {
	home := strings.TrimSpace(codexHome)
	if home == "" {
		return nil
	}
	globalHome, err := resolveCodexHomeForConfig("")
	if err != nil {
		return err
	}
	if filepath.Clean(globalHome) == filepath.Clean(home) {
		return nil
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("codex: mkdir per-user codex home: %w", err)
	}

	// config.toml: sync provider routing config from global on every call,
	// preserving per-user [projects.*] trust entries.
	perUserConfig := filepath.Join(home, "config.toml")
	globalConfig := filepath.Join(globalHome, "config.toml")
	if globalData, err := os.ReadFile(globalConfig); err == nil {
		providerCfg := extractProviderConfig(string(globalData))
		if strings.TrimSpace(providerCfg) != "" {
			perUserData, perr := os.ReadFile(perUserConfig)
			switch {
			case os.IsNotExist(perr):
				if werr := os.WriteFile(perUserConfig, []byte(providerCfg+"\n"), 0o644); werr != nil {
					return fmt.Errorf("codex: write per-user config.toml: %w", werr)
				}
				slog.Debug("codex: created per-user config.toml with global provider config", "dst", perUserConfig)
			case perr == nil:
				trustPart := extractTrustOnly(string(perUserData))
				// TOML top-level keys must precede any [table] section. providerCfg
				// starts with top-level keys (model_provider, model, ...) then
				// [model_providers.*]; per-user [projects.*] trust must come after
				// to keep provider keys at the root scope.
				merged := providerCfg + "\n\n" + strings.TrimRight(trustPart, "\n") + "\n"
				if werr := os.WriteFile(perUserConfig, []byte(merged), 0o644); werr != nil {
					return fmt.Errorf("codex: sync provider config: %w", werr)
				}
				slog.Debug("codex: synced global provider config into per-user config.toml", "dst", perUserConfig)
			}
		}
	}

	// auth.json: symlink to global so updates propagate to all per-user
	// workspaces. Replace any stale regular-file copy from older versions.
	globalAuth := filepath.Join(globalHome, "auth.json")
	perUserAuth := filepath.Join(home, "auth.json")
	if _, err := os.Stat(globalAuth); err == nil {
		if fi, lerr := os.Lstat(perUserAuth); lerr == nil {
			if fi.Mode()&os.ModeSymlink == 0 {
				if rerr := os.Remove(perUserAuth); rerr == nil {
					if serr := os.Symlink(globalAuth, perUserAuth); serr == nil {
						slog.Debug("codex: replaced auth.json copy with symlink to global", "dst", perUserAuth)
					}
				}
			}
		} else if os.IsNotExist(lerr) {
			if serr := os.Symlink(globalAuth, perUserAuth); serr == nil {
				slog.Debug("codex: symlinked auth.json to global", "dst", perUserAuth, "src", globalAuth)
			}
		}
	}
	return nil
}

// extractTrustOnly returns the per-user-specific parts of a codex config.toml
// ([projects.*] trust sections and any non-provider top-level keys), stripping
// provider routing config that is re-synced from the global config on every
// StartSession. This keeps per-user trust entries intact while letting the
// provider section be overwritten with the latest global values.
func extractTrustOnly(config string) string {
	var b strings.Builder
	lines := strings.Split(config, "\n")
	inModelProviders := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[model_providers.") {
			inModelProviders = true
			continue
		}
		if inModelProviders {
			if strings.HasPrefix(trimmed, "[") {
				inModelProviders = false
			} else {
				continue
			}
		}
		if !strings.HasPrefix(trimmed, "[") && trimmed != "" {
			if strings.HasPrefix(trimmed, "model_provider") ||
				strings.HasPrefix(trimmed, "model =") ||
				strings.HasPrefix(trimmed, "model_reasoning_effort") ||
				strings.HasPrefix(trimmed, "approval_policy") ||
				strings.HasPrefix(trimmed, "sandbox_mode") ||
				strings.HasPrefix(trimmed, "disable_response_storage") {
				continue
			}
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimSpace(b.String())
}

// extractProviderConfig extracts top-level provider keys and [model_providers.*]
// sections from a codex config.toml, omitting [projects.*] trust entries that are
// per-workspace specific. Used to merge the host's cc-switch provider routing
// into a per-user config.toml without duplicating workspace trust sections.
func extractProviderConfig(config string) string {
	var b strings.Builder
	lines := strings.Split(config, "\n")
	inModelProviders := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[model_providers."):
			inModelProviders = true
			b.WriteString(line + "\n")
		case inModelProviders && strings.HasPrefix(trimmed, "["):
			inModelProviders = false
		case inModelProviders:
			b.WriteString(line + "\n")
		case !strings.HasPrefix(trimmed, "[") && trimmed != "":
			if strings.HasPrefix(trimmed, "model_provider") ||
				strings.HasPrefix(trimmed, "model =") ||
				strings.HasPrefix(trimmed, "model_reasoning_effort") ||
				strings.HasPrefix(trimmed, "approval_policy") ||
				strings.HasPrefix(trimmed, "sandbox_mode") ||
				strings.HasPrefix(trimmed, "disable_response_storage") {
				b.WriteString(line + "\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func buildProviderSection(name, baseURL, wireAPI string, headers map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[model_providers.%s]\n", name)
	fmt.Fprintf(&sb, "name = %q\n", name)
	if baseURL != "" {
		fmt.Fprintf(&sb, "base_url = %q\n", baseURL)
	}
	fmt.Fprintf(&sb, "env_key = %q\n", "OPENAI_API_KEY")
	if wireAPI != "" {
		fmt.Fprintf(&sb, "wire_api = %q\n", wireAPI)
	}
	if len(headers) > 0 {
		fmt.Fprintf(&sb, "\n[model_providers.%s.http_headers]\n", name)
		for k, v := range headers {
			fmt.Fprintf(&sb, "%q = %q\n", k, v)
		}
	}
	return sb.String()
}

// upsertProviderSection replaces an existing [model_providers.<name>] section
// or appends a new one at the end of the config content.
func upsertProviderSection(content, name, newSection string) string {
	sectionHeader := fmt.Sprintf("[model_providers.%s]", name)
	subSectionPrefix := fmt.Sprintf("[model_providers.%s.", name)

	if !strings.Contains(content, sectionHeader) {
		trimmed := strings.TrimRight(content, "\n\t ")
		if trimmed == "" {
			return newSection
		}
		return trimmed + "\n\n" + newSection
	}

	idx := strings.Index(content, sectionHeader)

	after := content[idx+len(sectionHeader):]
	end := len(content)
	lines := strings.Split(after, "\n")
	offset := idx + len(sectionHeader)
	for _, line := range lines {
		offset += len(line) + 1
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 0 && trimmed[0] == '[' && !strings.HasPrefix(trimmed, subSectionPrefix) && trimmed != sectionHeader {
			end = offset - len(line) - 1
			break
		}
	}

	return strings.TrimRight(content[:idx], "\n") + "\n\n" + newSection + "\n" + content[end:]
}
