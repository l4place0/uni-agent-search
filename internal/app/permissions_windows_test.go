//go:build windows

package app

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// requirePrivateFile asserts the real Windows ACL rather than the mode bits Go
// synthesises from the read-only attribute (they are always 0666 here): the DACL
// must be protected, i.e. inherit nothing, and may grant only the owner, the
// current user, SYSTEM and Administrators.
//
// The check walks the ACEs instead of parsing the SDDL string, because SDDL
// prints well-known principals as aliases (SY, BA, LA) and the owner on a
// runner can differ from the account the process runs as.
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
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("read control flags of %s: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s still inherits its parent ACL", path)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("read DACL of %s: %v", path, err)
	}

	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatalf("open process token: %v", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatalf("read process user: %v", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []*windows.SID{owner, user.User.Sid, system, administrators}

	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("read ACE %d of %s: %v", i, path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("%s has a non-allow ACE (type %d)", path, ace.Header.AceType)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			t.Fatalf("%s has an inherited ACE", path)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		permitted := false
		for _, candidate := range allowed {
			if windows.EqualSid(sid, candidate) {
				permitted = true
				break
			}
		}
		if !permitted {
			t.Fatalf("%s grants access to %s", path, sid.String())
		}
	}
}
