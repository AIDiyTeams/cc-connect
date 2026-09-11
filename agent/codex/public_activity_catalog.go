package codex

import (
	"bufio"
	_ "embed"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Public operation categories let the conversation say "writing the document"
// instead of "one more step" without exposing the command. The bridge ships a
// default catalog; a Skills-OL checkout referenced by the command may override
// it with public-activities.json at its root and public_activity front matter
// in skills/<name>/SKILL.md, so skill authors describe their own steps.
//
//go:embed public_activity_catalog.json
var defaultActivityCatalogJSON []byte

type activityCatalog struct {
	Scripts map[string]string `json:"scripts"`
	Skills  map[string]string `json:"skills"`
}

var (
	activityLabelPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)
	scriptTokenPattern   = regexp.MustCompile(`\.(mjs|cjs|js|py|sh|ts)$`)
	commandTokenSplitter = regexp.MustCompile("[\\s;&|()<>\"'`]+")
	activityCatalogTTL   = time.Minute

	defaultActivityCatalog     activityCatalog
	defaultActivityCatalogOnce sync.Once

	catalogRootsMu sync.Mutex
	catalogRoots   = map[string]cachedActivityCatalog{}
)

type cachedActivityCatalog struct {
	catalog  activityCatalog
	loadedAt time.Time
}

func loadDefaultActivityCatalog() activityCatalog {
	defaultActivityCatalogOnce.Do(func() {
		_ = json.Unmarshal(defaultActivityCatalogJSON, &defaultActivityCatalog)
	})
	return defaultActivityCatalog
}

// commandActivityLabel names the operation category of a shell command from the
// scripts and skills it references. It matches whole path components against
// allowlisted catalogs and never interprets the command itself. Unknown commands
// return "" and stay anonymous steps.
func commandActivityLabel(command string) string {
	tokens := commandTokenSplitter.Split(command, -1)
	var overrides []activityCatalog
	for _, token := range tokens {
		if root := skillsRootOf(token); root != "" {
			if catalog, ok := loadActivityCatalogRoot(root); ok {
				overrides = append(overrides, catalog)
			}
		}
	}
	catalogs := append(overrides, loadDefaultActivityCatalog())
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if skill := skillDirOf(token); skill != "" {
			if label := lookupLabel(catalogs, func(c activityCatalog) string { return c.Skills[skill] }); label != "" {
				return label
			}
		}
		base := path.Base(strings.TrimRight(token, "/"))
		if scriptTokenPattern.MatchString(base) {
			if label := lookupLabel(catalogs, func(c activityCatalog) string { return c.Scripts[base] }); label != "" {
				return label
			}
		}
	}
	return ""
}

func lookupLabel(catalogs []activityCatalog, pick func(activityCatalog) string) string {
	for _, catalog := range catalogs {
		if label := strings.TrimSpace(pick(catalog)); label != "" && activityLabelPattern.MatchString(label) {
			return label
		}
	}
	return ""
}

// skillDirOf returns the <name> in a ".../skills/<name>/..." path token.
func skillDirOf(token string) string {
	idx := strings.Index(token, "skills/")
	if idx < 0 || (idx > 0 && token[idx-1] != '/') {
		return ""
	}
	rest := token[idx+len("skills/"):]
	if slash := strings.Index(rest, "/"); slash > 0 {
		return rest[:slash]
	}
	return ""
}

// skillsRootOf returns the absolute Skills-OL checkout a path token points into,
// identified by a "Skills-OL*" path component.
func skillsRootOf(token string) string {
	if !filepath.IsAbs(token) {
		return ""
	}
	parts := strings.Split(filepath.Clean(token), string(filepath.Separator))
	for i, part := range parts {
		if strings.HasPrefix(part, "Skills-OL") {
			return string(filepath.Separator) + filepath.Join(parts[1:i+1]...)
		}
	}
	return ""
}

func loadActivityCatalogRoot(root string) (activityCatalog, bool) {
	catalogRootsMu.Lock()
	defer catalogRootsMu.Unlock()
	if cached, ok := catalogRoots[root]; ok && time.Since(cached.loadedAt) < activityCatalogTTL {
		return cached.catalog, len(cached.catalog.Scripts)+len(cached.catalog.Skills) > 0
	}
	catalog := activityCatalog{Scripts: map[string]string{}, Skills: map[string]string{}}
	if raw, err := os.ReadFile(filepath.Join(root, "public-activities.json")); err == nil {
		var declared activityCatalog
		if json.Unmarshal(raw, &declared) == nil {
			for k, v := range declared.Scripts {
				catalog.Scripts[k] = v
			}
			for k, v := range declared.Skills {
				catalog.Skills[k] = v
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, "skills")); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			if label := skillFrontMatterLabel(filepath.Join(root, "skills", entry.Name(), "SKILL.md")); label != "" {
				catalog.Skills[entry.Name()] = label
			}
		}
	}
	catalogRoots[root] = cachedActivityCatalog{catalog: catalog, loadedAt: time.Now()}
	return catalog, len(catalog.Scripts)+len(catalog.Skills) > 0
}

// skillFrontMatterLabel reads "public_activity: <label>" from a SKILL.md front
// matter block. Only the leading block is scanned; the body is never parsed.
func skillFrontMatterLabel(skillFile string) string {
	file, err := os.Open(skillFile)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	inFrontMatter := false
	for lines := 0; scanner.Scan() && lines < 60; lines++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "---" {
			if inFrontMatter {
				return ""
			}
			inFrontMatter = true
			continue
		}
		if !inFrontMatter {
			return ""
		}
		if value, ok := strings.CutPrefix(line, "public_activity:"); ok {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}
