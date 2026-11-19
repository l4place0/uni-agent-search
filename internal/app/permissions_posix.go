//go:build !windows

package app

import "os"

// protectPrivateFile keeps a file holding key material readable only by its
// owner. On POSIX the mode bits are the whole contract, so 0600 is enough.
func protectPrivateFile(path string) error {
	return os.Chmod(path, 0600)
}
