package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStateDirDACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := StorePassword(dir, "long enough"); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	for _, name := range []string{dir, filepath.Join(dir, "password")} {
		sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		sddl := sd.String()
		for _, want := range []string{";;;SY)", ";;;BA)", ";;;" + sid + ")"} {
			if !strings.Contains(sddl, want) {
				t.Errorf("%s: %s lacks %s", name, sddl, want)
			}
		}
		if strings.Count(sddl, "(A;") != 3 || strings.Contains(sddl, ";;;BU)") || strings.Contains(sddl, ";;;WD)") {
			t.Errorf("%s: %s", name, sddl)
		}
		if name == dir && !strings.HasPrefix(sddl, "D:P") {
			t.Errorf("the state directory's DACL is not protected: %s", sddl)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "password")); err != nil {
		t.Error(err)
	}
}

func TestStateDirOwner(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		sid string
		ok  bool
	}{
		{user.User.Sid.String(), true},
		{"S-1-5-18", true},
		{"S-1-5-32-544", true},
		{"S-1-1-0", false},
		{"S-1-5-32-545", false},
		{"S-1-5-21-1-2-3-1001", false},
	} {
		sid, err := windows.StringToSid(c.sid)
		if err != nil {
			t.Fatal(err)
		}
		if got := trustedOwner(sid, user.User.Sid); got != c.ok {
			t.Errorf("owner %s: %v", c.sid, got)
		}
	}
	if trustedOwner(nil, user.User.Sid) {
		t.Error("no owner is trusted")
	}
}

func TestStateDirJunction(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("mklink: %v %s", err, out)
	}
	if err := StorePassword(link, "long enough"); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sd.String(), "D:P") {
		t.Errorf("the junction's target is not protected: %s", sd)
	}
	if _, err := os.Stat(filepath.Join(target, "password")); err != nil {
		t.Error(err)
	}
}
