//go:build windows

package app

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// protectPrivateFile keeps a file holding key material readable only by its
// owner. Windows ignores the 0600 mode passed to CreateFile, so a freshly
// written key file keeps the ACL it inherited from its directory — on a data
// directory outside the user profile that means BUILTIN\Users and Authenticated
// Users can read the encryption master key. Windows also ignores the
// inheritance bits in a mode, so the DACL is replaced and marked protected
// instead: owner, SYSTEM and Administrators only.
func protectPrivateFile(path string) error {
	if err := restrictToOwner(path); err != nil {
		return fmt.Errorf("restrict permissions on %s: %w", path, err)
	}
	return nil
}

func restrictToOwner(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{
		explicitAccess(user.User.Sid, windows.TRUSTEE_IS_USER),
		explicitAccess(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		explicitAccess(administrators, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	// PROTECTED_DACL_SECURITY_INFORMATION severs inheritance from the parent
	// directory, so a later change to that directory cannot widen access again.
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
}

func explicitAccess(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
