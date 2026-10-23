//go:build !unix && !windows

package web

func securePrivateDir(string, func(format string, args ...any)) error { return nil }

func adoptOwner(string) error { return nil }
