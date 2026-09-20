package hostos

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	if err := Protect(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "credential")
	if err := os.WriteFile(p, []byte("test-value"), 0600); err != nil {
		t.Fatal(err)
	}
	i, e := os.Lstat(p)
	if e != nil {
		t.Fatal(e)
	}
	if !Private(p, i, 0077) {
		t.Fatal("new file did not inherit private directory ACL")
	}
	sid, _ := currentSID()
	sd, e := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")(A;;FR;;;WD)")
	if e != nil {
		t.Fatal(e)
	}
	acl, _, e := sd.DACL()
	if e != nil {
		t.Fatal(e)
	}
	if e = windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); e != nil {
		t.Fatal(e)
	}
	if Private(p, i, 0077) {
		t.Fatal("world-readable credential accepted")
	}
	if e = Protect(p, 0600); e != nil {
		t.Fatal(e)
	}
	if !Private(p, i, 0077) {
		t.Fatal("protected credential rejected")
	}
}
