//go:build windows

package config

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// restrictToOwner replaces the DACL of path with a protected DACL that grants
// full control to the current user only. Protection stops ACEs inherited from
// parent directories (for example a shared folder) from widening access. For
// directories the entry is inheritable, so new children are owner-only too.
//
// This is the Windows equivalent of chmod 0600 / 0700: os.Chmod on Windows
// only toggles the read-only attribute.
func restrictToOwner(path string, isDir bool) error {
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	inherit := uint32(windows.NO_INHERITANCE)
	if isDir {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("building owner-only ACL: %w", err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil)
	if err != nil {
		return fmt.Errorf("setting owner-only ACL: %w", err)
	}
	return nil
}

// currentUserSID returns the SID of the user the process runs as.
func currentUserSID() (*windows.SID, error) {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("reading process token user: %w", err)
	}
	// Copy: the SID points into tu's buffer.
	sid, err := tu.User.Sid.Copy()
	if err != nil {
		return nil, fmt.Errorf("copying user SID: %w", err)
	}
	return sid, nil
}
