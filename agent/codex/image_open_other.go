//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package codex

import "os"

// Managed Tomako sessions require descriptor-relative, no-symlink opens. Keep
// the unfenced upstream adapter unchanged on unsupported platforms.
func openImageWithoutSymlinks(_, _ string) (*os.File, error) {
	return nil, os.ErrPermission
}
