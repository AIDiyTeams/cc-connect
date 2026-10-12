package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/chenhg5/cc-connect/core"
)

func TestWorkbenchSkillsHideCatalogAndFilesWithoutChangingTaskSkills(t *testing.T) {
	root, work := t.TempDir(), t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	for _, name := range []string{"old-tool", "employee"} {
		if err := os.MkdirAll(filepath.Join(root, "skills", name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "skills", name, "SKILL.md"), []byte("synthetic"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "workbench-skills.json"), []byte(`{"version":1,"excluded":["old-tool"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := workbenchExcludedSkillPaths(root, true)
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	args, err := workspaceSecurityStartupArgs("fence", work, filepath.Join(work, ".tmp", "current"), "", root, paths...)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Permissions map[string]struct{ Filesystem map[string]any }
		Skills      struct {
			Config []struct {
				Path    string
				Enabled bool
			}
		}
	}
	if _, err = toml.Decode(args[1]+"\n"+args[3], &config); err != nil {
		t.Fatal(err)
	}
	if config.Permissions["fence"].Filesystem[paths[0]] != "none" || len(config.Skills.Config) != 1 || config.Skills.Config[0].Enabled || config.Skills.Config[0].Path != filepath.Join(paths[0], "SKILL.md") {
		t.Fatalf("missing exclusion: %#v", config)
	}
	if _, ok := config.Permissions["fence"].Filesystem[filepath.Join(root, "skills", "employee")]; ok {
		t.Fatal("employee access changed")
	}
	if taskPaths, err := workbenchExcludedSkillPaths(root, false); err != nil || len(taskPaths) != 0 {
		t.Fatal("standalone tasks changed")
	}
}

func TestWorkbenchPolicyRejectsMissingMalformedEscapingAndSymlinkedSkills(t *testing.T) {
	for _, data := range []string{"", `{"version":2,"excluded":[]}`, `{"version":1,"excluded":["../other"]}`, `{"version":1,"excluded":[],"other":true}`, `{"version":1,"excluded":[]} {}`, `{"version":1,"excluded":["missing"]}`} {
		root := t.TempDir()
		if data != "" {
			_ = os.WriteFile(filepath.Join(root, "workbench-skills.json"), []byte(data), 0600)
		}
		if _, err := workbenchExcludedSkillPaths(root, true); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "skills"), 0700)
	_ = os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("synthetic"), 0600)
	_ = os.Symlink(outside, filepath.Join(root, "skills", "escape"))
	_ = os.WriteFile(filepath.Join(root, "workbench-skills.json"), []byte(`{"version":1,"excluded":["escape"]}`), 0600)
	if _, err := workbenchExcludedSkillPaths(root, true); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestWorkbenchProcessCannotBeReusedAsUnrestrictedTask(t *testing.T) {
	workbench := true
	s := &appServerSession{workbenchSkillScope: &workbench}
	s.alive.Store(true)
	if err := s.SetSessionRuntime(core.SessionRuntime{}); err == nil || !strings.Contains(err.Error(), "skill scope changed") {
		t.Fatalf("scope widening accepted: %v", err)
	}
	workbench = false
	if err := s.SetSessionRuntime(core.SessionRuntime{ChatSessionID: "chat"}); err == nil {
		t.Fatal("unrestricted process reused by workbench")
	}
}
