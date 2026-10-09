package throne

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/throneproj/Throne-api/internal/wire"
)

const keyFormat = "want the 43-character base64url text that Throne shows"

// Key is a Throne API key. Printing a Key, or a value that holds one, never reveals it.
type Key struct {
	// fmt prints a func as an address and never follows it, even where it cannot call
	// Format; a pointer would be followed for some verbs.
	secret func() *[wire.KeySize]byte
}

// ParseKey parses a key in its text form: base64url without padding, 43 characters, as
// Throne shows and copies it. Surrounding whitespace is ignored.
func ParseKey(text string) (Key, error) {
	k, ok := parseKey(text)
	if !ok {
		return Key{}, errors.New("throne: invalid key: " + keyFormat)
	}
	return k, nil
}

// ReadKeyFile reads a key in its text form from a file.
func ReadKeyFile(name string) (Key, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Key{}, fmt.Errorf("throne: read key: %w", err)
	}
	// Some Windows editors start the file with a byte order mark.
	k, ok := parseKey(string(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})))
	if !ok {
		return Key{}, fmt.Errorf("throne: invalid key in %s: %s", name, keyFormat)
	}
	return k, nil
}

func parseKey(text string) (Key, bool) {
	text = strings.TrimSpace(text)
	if len(text) != base64.RawURLEncoding.EncodedLen(wire.KeySize) {
		return Key{}, false
	}
	b := new([wire.KeySize]byte)
	n, err := base64.RawURLEncoding.Strict().Decode(b[:], []byte(text))
	if err != nil || n != wire.KeySize {
		return Key{}, false
	}
	return Key{secret: func() *[wire.KeySize]byte { return b }}, true
}

// bytes returns the key; the zero Key is all zeros.
func (k Key) bytes() *[wire.KeySize]byte {
	if k.secret == nil {
		return new([wire.KeySize]byte)
	}
	return k.secret()
}

// Format prints a placeholder instead of the key, whatever the verb.
func (Key) Format(f fmt.State, _ rune) {
	io.WriteString(f, "throne.Key(redacted)")
}
