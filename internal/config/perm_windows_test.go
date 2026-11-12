//go:build windows

package config

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func assertOwnerOnlyFile(t *testing.T, path string) {
	t.Helper()
	assertOwnerOnlyACL(t, path)
}

func assertOwnerOnlyDir(t *testing.T, path string) {
	t.Helper()
	assertOwnerOnlyACL(t, path)
}

// assertOwnerOnlyACL checks that the DACL is protected (no inherited entries)
// and that every access-allowed entry belongs to the current user.
func assertOwnerOnlyACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%s): %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Errorf("%s: DACL is not protected; inherited ACEs may grant other users access", path)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if dacl == nil {
		t.Fatalf("%s: NULL DACL grants everyone full access", path)
	}

	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	if user == nil {
		t.Fatal("currentUserSID returned nil")
	}
	if dacl.AceCount == 0 {
		t.Fatalf("%s: empty DACL", path)
	}
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("GetAce(%d): %v", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user) {
			t.Errorf("%s: ACE %d grants access to %s, want only the current user %s", path, i, sid, user)
		}
	}
}
