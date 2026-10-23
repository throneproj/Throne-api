//go:build windows

package web

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// securePrivateDir checks that the state directory, after links, belongs to this user,
// SYSTEM or the Administrators, who alone could change its permissions later, and gives
// it a protected DACL that only they may use, inherited by everything in it.
func securePrivateDir(dir string, _ func(format string, args ...any)) error {
	real, err := finalPath(dir)
	if err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("secure the state directory: %w", err)
	}
	sd, err := windows.GetNamedSecurityInfo(real, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("secure the state directory: %w", err)
	}
	own, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("secure the state directory: %w", err)
	}
	if !trustedOwner(own, user.User.Sid) {
		return &ConfigError{fmt.Sprintf("the state directory %s belongs to %s, not to this user, SYSTEM or the "+
			"Administrators", real, accountName(own))}
	}
	dsd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" +
		user.User.Sid.String() + ")")
	if err != nil {
		return fmt.Errorf("secure the state directory: %w", err)
	}
	dacl, _, err := dsd.DACL()
	if err != nil {
		return fmt.Errorf("secure the state directory: %w", err)
	}
	err = windows.SetNamedSecurityInfo(real, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("secure the state directory %s (it must be on a file system with permissions, such as "+
			"NTFS): %w", real, err)
	}
	return nil
}

// finalPath resolves symbolic links and junctions, which filepath.EvalSymlinks leaves.
func finalPath(dir string) (string, error) {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
	if err != nil {
		return "", &os.PathError{Op: "resolve", Path: dir, Err: err}
	}
	path := windows.UTF16ToString(buf[:n])
	if rest, ok := strings.CutPrefix(path, `\\?\UNC\`); ok {
		return `\\` + rest, nil
	}
	return strings.TrimPrefix(path, `\\?\`), nil
}

// trustedOwner says whether a directory of owner can be the state directory of user.
func trustedOwner(owner, user *windows.SID) bool {
	if owner == nil {
		return false
	}
	if owner.Equals(user) {
		return true
	}
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		if sid, err := windows.CreateWellKnownSid(t); err == nil && owner.Equals(sid) {
			return true
		}
	}
	return false
}

func accountName(sid *windows.SID) string {
	if account, domain, _, err := sid.LookupAccount(""); err == nil {
		if domain != "" {
			return domain + `\` + account
		}
		return account
	}
	return sid.String()
}

func adoptOwner(string) error { return nil }
