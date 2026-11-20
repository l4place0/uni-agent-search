//go:build !windows

package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// chmodOwnerOnly is a variable so a test can inject the EPERM a container gets
// when the key file belongs to another user.
var chmodOwnerOnly = func(path string) error { return os.Chmod(path, 0600) }

// protectPrivateFile keeps a file holding key material readable only by its
// owner. On POSIX the mode bits are the whole contract, so 0600 is enough. A
// failure is fatal: starting with a key file other users can read would defeat
// the encryption, so there is no warn-and-continue path.
func protectPrivateFile(path string) error {
	if err := chmodOwnerOnly(path); err != nil {
		return permissionError(path, err)
	}
	return nil
}

// permissionError turns a refused chmod into something an operator can act on:
// who owns the file, which account the service runs as, and the exact command
// that fixes it. It reports paths, ownership and mode only — never key material.
func permissionError(path string, cause error) error {
	if !errors.Is(cause, fs.ErrPermission) {
		return fmt.Errorf("restrict permissions on %s: %w", path, cause)
	}
	owner, mode := "unknown", "unknown"
	if info, err := os.Stat(path); err == nil {
		mode = fmt.Sprintf("%04o", info.Mode().Perm())
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf("%d:%d", stat.Uid, stat.Gid)
		}
	}
	uid, gid := os.Geteuid(), os.Getegid()
	return fmt.Errorf(`cannot restrict permissions on %s: %w
  file owner: %s, mode %s
  service runs as: %d:%d
  fix (then restart the service):
    chown -R %d:%d %s
  refusing to start while other local users may read the key file`,
		path, cause, owner, mode, uid, gid, uid, gid, filepath.Dir(path))
}
