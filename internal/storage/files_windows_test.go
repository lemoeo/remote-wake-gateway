package storage

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func assertPrivateACL(t *testing.T, path string, directory bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("ACL is not protected: %s %v", path, err)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 3 {
		t.Fatalf("unexpected ACL: %s %v", path, err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"S-1-5-18": true, "S-1-5-32-544": true, user.User.Sid.String(): true}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !want[sid] || ace.Mask != 0x001f01ff {
			t.Fatalf("unexpected access entry: %s %s %#x", path, sid, ace.Mask)
		}
		delete(want, sid)
		if directory && ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
			t.Fatalf("directory ACL does not protect children: %s", path)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing runtime/system/admin permissions: %v", want)
	}
}

func TestWindowsPrivateACLAndReplacement(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "网关 data"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{"", "secrets", "jobs"} {
		assertPrivateACL(t, s.Path(name), true)
	}
	assertPrivateACL(t, s.Path(".lock"), false)
	for _, name := range []string{"config.json", "secrets/admin.json", "secrets/credentials.key", "secrets/setup.token", "jobs/job_example.json"} {
		for _, data := range [][]byte{[]byte("first value"), []byte("replacement value")} {
			if err := s.WriteBytes(name, data); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(s.Path(name))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("replacement failed: %s %v", name, err)
			}
			assertPrivateACL(t, s.Path(name), false)
		}
	}
}

func TestWindowsFailedReplacementPreservesOldData(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.WriteBytes("config.json", []byte("original")); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(s.Path("config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an external reader that does not allow delete/rename sharing.
	handle, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := s.WriteBytes("config.json", []byte("new")); err == nil {
		t.Fatal("replaced a file held without delete sharing")
	}
	got, err := os.ReadFile(s.Path("config.json"))
	if err != nil || string(got) != "original" {
		t.Fatalf("old data lost after failed replace: %s %v", got, err)
	}
	leftovers, err := filepath.Glob(s.Path(".write-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files leaked: %v %v", leftovers, err)
	}
}
