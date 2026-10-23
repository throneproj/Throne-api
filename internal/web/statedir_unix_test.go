//go:build unix

package web

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStateDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var logged []string
	if err := prepareStateDir(dir, func(f string, _ ...any) { logged = append(logged, f) }); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 || len(logged) != 1 {
		t.Errorf("mode %v, %v, logged %v", fi.Mode(), err, logged)
	}

	// As systemd's StateDirectory with DynamicUser: a link of root or this user to the directory.
	target := filepath.Join(t.TempDir(), "private", "state")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "state")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := StorePassword(link, "long enough"); err != nil {
		t.Fatalf("a link to this user's directory: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("target mode %v, %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(target, "password")); err != nil {
		t.Error(err)
	}

	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	var cerr *ConfigError
	if err := prepareStateDir(file, nil); err == nil {
		t.Error("a file passes as the state directory")
	} else if !errors.As(err, &cerr) && !errors.Is(err, syscall.ENOTDIR) {
		t.Logf("file: %v", err)
	}
}

// TestStateDirRoot runs as root only: root manages another user's directory, clears only
// the group's and others' bits, and gives the files it writes to the directory's owner.
func TestStateDirRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	if err := StorePassword(dir, "long enough"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v, %v", fi.Mode(), err)
	}
	pf, err := os.Stat(filepath.Join(dir, "password"))
	if err != nil {
		t.Fatal(err)
	}
	if uid, gid := owner(pf); uid != 65534 || gid != 65534 || pf.Mode().Perm() != 0o600 {
		t.Errorf("password file of %d:%d, mode %v", uid, gid, pf.Mode())
	}
	if uid, _ := owner(fi); uid != 65534 {
		t.Errorf("the directory changed owner: %d", uid)
	}

	other := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, other)
	os.Lchown(other, 65534, 65534)
	var cerr *ConfigError
	if err := prepareStateDir(other, nil); !errors.As(err, &cerr) {
		t.Errorf("a link of another user: %v", err)
	}
}
