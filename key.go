package throne

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"

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

// ReadKeyFile reads a key in its text form from a file. As Throne does, it reads the file
// as UTF-8, or as UTF-16 or UTF-32 when it starts with their byte order mark, which
// Windows PowerShell writes.
func ReadKeyFile(name string) (Key, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return Key{}, fmt.Errorf("throne: read key: %w", err)
	}
	k, ok := parseKey(decodeKeyText(b))
	if !ok {
		return Key{}, fmt.Errorf("throne: invalid key in %s: %s", name, keyFormat)
	}
	return k, nil
}

// decodeKeyText checks the byte order marks in Throne's order, so FF FE 00 00 is UTF-32LE
// rather than UTF-16LE. Like Throne, it drops a partial UTF-16 or UTF-32 unit at the end.
func decodeKeyText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0x00, 0x00, 0xFE, 0xFF}):
		return decodeUTF32(b[4:], binary.BigEndian)
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE, 0x00, 0x00}):
		return decodeUTF32(b[4:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], binary.BigEndian)
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], binary.LittleEndian)
	}
	return string(b)
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(units))
}

func decodeUTF32(b []byte, order binary.ByteOrder) string {
	var s strings.Builder
	for ; len(b) >= 4; b = b[4:] {
		// WriteRune writes U+FFFD for a value that is not a code point.
		s.WriteRune(rune(order.Uint32(b)))
	}
	return s.String()
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
