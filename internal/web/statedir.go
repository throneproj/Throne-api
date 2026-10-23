package web

import (
	"fmt"
	"os"
)

// prepareStateDir creates the state directory and makes it private to its owner. A
// directory that someone else could have prepared for this user is refused.
func prepareStateDir(dir string, logf func(format string, args ...any)) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the state directory: %w", err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return securePrivateDir(dir, logf)
}

// makeDir creates a directory in the state directory, owned like its parent.
func makeDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return adoptOwner(dir)
}
