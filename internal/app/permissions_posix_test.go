//go:build !windows

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// A container whose data files belong to another user gets EPERM from chmod.
// The message must tell the operator what to do instead of only "not permitted".
func TestPermissionErrorExplainsOwnershipMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	if err := os.WriteFile(path, []byte("test-key-material"), 0666); err != nil {
		t.Fatal(err)
	}
	injectChmodError(t, syscall.EPERM)

	err := protectPrivateFile(path)
	if err == nil {
		t.Fatal("a refused chmod must fail, never warn and continue")
	}
	message := err.Error()
	uid, gid := os.Geteuid(), os.Getegid()
	for _, want := range []string{
		path,
		"file owner:",
		fmt.Sprintf("service runs as: %d:%d", uid, gid),
		fmt.Sprintf("chown -R %d:%d %s", uid, gid, dir),
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q:\n%s", want, message)
		}
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("cause must stay inspectable with errors.Is: %v", err)
	}
	if strings.Contains(message, "test-key-material") {
		t.Fatalf("message leaked key material:\n%s", message)
	}
}

// The startup path must keep failing closed, and the error an operator sees must
// carry the same actionable hint.
func TestStartupRefusesToStartWhenKeyCannotBeRestricted(t *testing.T) {
	dir := t.TempDir()
	key := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(filepath.Join(dir, "master.key"), key, 0666); err != nil {
		t.Fatal(err)
	}
	creds, _ := json.Marshal(map[string]string{"admin_token": strings.Repeat("a", 32), "client_token": strings.Repeat("b", 32)})
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), creds, 0666); err != nil {
		t.Fatal(err)
	}
	injectChmodError(t, syscall.EACCES)

	a, err := New(dir)
	if err == nil {
		a.Close()
		t.Fatal("startup must refuse to run with an unreadable-only-to-others key file")
	}
	message := err.Error()
	uid, gid := os.Geteuid(), os.Getegid()
	for _, want := range []string{"master.key", fmt.Sprintf("service runs as: %d:%d", uid, gid), fmt.Sprintf("chown -R %d:%d %s", uid, gid, dir)} {
		if !strings.Contains(message, want) {
			t.Fatalf("startup error missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, string(key)) {
		t.Fatalf("startup error leaked the master key:\n%s", message)
	}
}

func injectChmodError(t *testing.T, kind syscall.Errno) {
	t.Helper()
	original := chmodOwnerOnly
	t.Cleanup(func() { chmodOwnerOnly = original })
	chmodOwnerOnly = func(path string) error {
		return &fs.PathError{Op: "chmod", Path: path, Err: kind}
	}
}
