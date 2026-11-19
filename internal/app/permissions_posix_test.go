//go:build !windows

package app

import (
	"os"
	"testing"
)

// requirePrivateFile asserts the POSIX notion of "only the owner can read this":
// the strict 0600 mode check, unchanged.
func requirePrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("unsafe permissions for %s: %o", path, perm)
	}
}
