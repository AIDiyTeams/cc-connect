//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package codex

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Each descriptor pins its parent. O_NOFOLLOW applies to every component, not
// just the final filename; neither symlink swaps nor a FIFO can escape/block the
// trusted image adapter after its scope validation.
func openImageWithoutSymlinks(base, relative string) (*os.File, error) {
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			_ = unix.Close(fd)
			return nil, os.ErrPermission
		}
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), filepath.Join(base, relative)), nil
}
