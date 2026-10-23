// Command fakethrone runs an in-memory Throne for developing clients of the Throne API, such
// as the web UI of thronectl web, without a real Throne:
//
//	go run ./internal/fakethrone/cmd -listen 127.0.0.1:19095 -key-file fake.key
//
// It creates the key file when it is missing, and prints the settings for clients:
//
//	THRONE_ADDR=127.0.0.1:19095 THRONE_KEY_FILE=/path/to/fake.key
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/throneproj/Throne-api/internal/fakethrone"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:19095", "listen on `host:port`")
	keyFile := flag.String("key-file", filepath.Join(os.TempDir(), "fakethrone.key"),
		"read the key from `file`, which is created when it is missing")
	seed := flag.Uint64("seed", 1, "make up the data from this `number`; the same seed makes the same Throne")
	verbose := flag.Bool("v", false, "print the fake's log to standard error too")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	key, err := loadKey(*keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakethrone:", err)
		os.Exit(1)
	}
	cfg := fakethrone.Config{Key: key, Seed: *seed}
	if *verbose {
		cfg.Log = os.Stderr
	}
	s, err := fakethrone.Start(*listen, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakethrone:", err)
		os.Exit(1)
	}
	abs, err := filepath.Abs(*keyFile)
	if err != nil {
		abs = *keyFile
	}
	fmt.Printf("THRONE_ADDR=%s THRONE_KEY_FILE=%s\n", s.Addr(), abs)
	fmt.Fprintln(os.Stderr, "fakethrone: serving a fake Throne until interrupted")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	s.Close()
}

// loadKey reads the key from a file, or writes a new key to it when it is missing.
func loadKey(name string) (string, error) {
	b, err := os.ReadFile(name)
	if errors.Is(err, os.ErrNotExist) {
		k := make([]byte, 32)
		rand.Read(k)
		text := base64.RawURLEncoding.EncodeToString(k)
		return text, os.WriteFile(name, []byte(text+"\n"), 0o600)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(string(b), "\xef\xbb\xbf")), nil
}
