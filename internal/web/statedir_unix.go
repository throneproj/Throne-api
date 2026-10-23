//go:build unix

package web

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// securePrivateDir checks the state directory: each symbolic link that its path ends in
// must belong to root or to this user, as systemd's StateDirectory link with DynamicUser
// does, and the directory itself to this user, unless this is root, which may manage
// any. It then takes the group's and others' permissions away: for this user's own
// directory it sets 0700, and on another's only clears those bits.
func securePrivateDir(dir string, logf func(format string, args ...any)) error {
	euid := os.Geteuid()
	p := dir
	for range 40 {
		fi, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("state directory: %w", err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			break
		}
		if uid, _ := owner(fi); uid != 0 && uid != euid {
			return &ConfigError{fmt.Sprintf("the state directory %s goes through the symbolic link %s of user %d, "+
				"neither root nor this user (%d)", dir, p, uid, euid)}
		}
		target, err := os.Readlink(p)
		if err != nil {
			return fmt.Errorf("state directory: %w", err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(p), target)
		}
		p = target
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	if !fi.IsDir() {
		return &ConfigError{fmt.Sprintf("the state directory %s is not a directory", dir)}
	}
	uid, _ := owner(fi)
	if uid != euid && euid != 0 {
		return &ConfigError{fmt.Sprintf("the state directory %s belongs to user %d, not to this user (%d)", real, uid, euid)}
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		want := perm &^ 0o077
		if uid == euid {
			want = 0o700
		}
		if err := os.Chmod(real, want); err != nil {
			return fmt.Errorf("make the state directory private: %w", err)
		}
		logf("made the state directory %s private: mode %04o, was %04o", real, want, perm)
	}
	return nil
}

func owner(fi os.FileInfo) (uid, gid int) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid), int(st.Gid)
	}
	return -1, -1
}

// adoptOwner gives a file or directory that root made the owner and group of the
// directory it is in, so that the user who owns the state directory can read it.
func adoptOwner(name string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(filepath.Dir(name))
	if err != nil {
		return err
	}
	uid, gid := owner(fi)
	if uid <= 0 {
		return nil
	}
	return os.Lchown(name, uid, gid)
}
