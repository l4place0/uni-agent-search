//go:build windows

package app

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// requirePrivateFile asserts the real Windows ACL rather than the mode bits Go
// synthesises from the read-only attribute (they are always 0666 here): the DACL
// must be protected, i.e. inherit nothing, and must grant only the owner,
// SYSTEM and Administrators.
func requirePrivateFile(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("read ACL of %s: %v", path, err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		t.Fatalf("read owner of %s: %v", path, err)
	}
	sddl := sd.String()
	// SDDL prints well-known principals as aliases (SY, BA) and everything else
	// as a full SID, so accept both spellings of the three allowed grantees.
	allowed := map[string]bool{
		owner.String(): true, "S-1-5-18": true, "SY": true,
		"S-1-5-32-544": true, "BA": true,
	}
	// The description also carries the owner, so compare only the DACL part.
	dacl := sddl[strings.Index(sddl, "D:"):]
	if !strings.HasPrefix(dacl, "D:P") {
		t.Fatalf("%s still inherits its parent ACL: %s", path, sddl)
	}
	body := strings.TrimPrefix(dacl, "D:P")
	if strings.HasPrefix(body, "AI") {
		body = strings.TrimPrefix(body, "AI")
	}
	for strings.Contains(body, "(") {
		open := strings.Index(body, "(")
		close := strings.Index(body[open:], ")")
		if close < 0 {
			t.Fatalf("unparsable ACL for %s: %s", path, sddl)
		}
		ace := body[open+1 : open+close]
		body = body[open+close+1:]
		fields := strings.Split(ace, ";")
		if len(fields) < 6 || fields[0] != "A" {
			t.Fatalf("%s has a non-allow ACE: %s", path, sddl)
		}
		if strings.Contains(fields[1], "I") {
			t.Fatalf("%s has an inherited ACE: %s", path, sddl)
		}
		if sid := fields[5]; !allowed[sid] {
			t.Fatalf("%s grants access to %s: %s", path, sid, sddl)
		}
	}
}
