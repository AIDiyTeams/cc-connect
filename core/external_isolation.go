package core

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateIsolationKey must never normalize an invalid key into ordinary access.
func ValidateIsolationKey(key string) error {
	if key == "" {
		return nil
	}
	if len(key) != 64 || strings.ToLower(key) != key {
		return fmt.Errorf("invalid isolation key")
	}
	if _, err := hex.DecodeString(key); err != nil {
		return fmt.Errorf("invalid isolation key")
	}
	return nil
}

// IsIsolatedWorkspace also prevents adapters from substituting a shared session base.
func IsIsolatedWorkspace(path, key string) bool {
	return key != "" && ValidateIsolationKey(key) == nil && filepath.IsAbs(path) &&
		filepath.Base(path) == key && filepath.Base(filepath.Dir(path)) == "external-conversations"
}

func (e *Engine) isolatedWorkspace(runtime SessionRuntime) (string, error) {
	if err := ValidateIsolationKey(runtime.IsolationKey); err != nil {
		return "", err
	}
	agent, ok := e.agent.(IsolatedSessionAgent)
	if !ok || !agent.SupportsIsolatedSessions() || !e.multiWorkspace || !filepath.IsAbs(e.baseDir) {
		return "", fmt.Errorf("isolated runtime is not configured")
	}
	base := filepath.Clean(e.baseDir)
	resolved, err := filepath.EvalSymlinks(base)
	if err != nil || resolved != base {
		return "", fmt.Errorf("isolation base must be a real directory")
	}
	path := base
	for _, part := range []string{"external-conversations", runtime.IsolationKey} {
		path = filepath.Join(path, part)
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return "", err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("isolation path must be a real directory")
		}
	}
	return path, nil
}
