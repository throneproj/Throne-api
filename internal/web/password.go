package web

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// MinPasswordLength is the shortest password the gateway accepts, in characters.
const MinPasswordLength = 8

// maxPasswordLength bounds what a login hashes, in bytes.
const maxPasswordLength = 4096

// The argon2id parameters of new hashes: OWASP's 19 MiB, 2 passes, 1 lane.
const (
	argonMemory  = 19456
	argonTime    = 2
	argonThreads = 1
	argonSaltLen = 16
	argonKeyLen  = 32
)

const (
	passwordName = "password"
	tempPassword = ".password-"
)

// HashPassword returns the argon2id hash of password as a PHC string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// phc is a parsed argon2id PHC string.
type phc struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    []byte
	key     []byte
}

// parsePHC parses an argon2id PHC string, refusing parameters that would make a login
// cost more than a few times the defaults.
func parsePHC(s string) (*phc, error) {
	bad := errors.New("not an argon2id hash of the form $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>")
	parts := strings.Split(strings.TrimSpace(s), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return nil, bad
	}
	var h phc
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, _ := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, bad
		}
		switch k {
		case "m":
			h.memory = uint32(n)
		case "t":
			h.time = uint32(n)
		case "p":
			if n > 255 {
				return nil, bad
			}
			h.threads = uint8(n)
		default:
			return nil, bad
		}
	}
	if h.memory < 8 || h.memory > 256<<10 || h.time < 1 || h.time > 16 || h.threads < 1 || h.threads > 16 {
		return nil, bad
	}
	var err error
	if h.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(h.salt) < 8 {
		return nil, bad
	}
	if h.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(h.key) < 16 || len(h.key) > 64 {
		return nil, bad
	}
	return &h, nil
}

func (h *phc) matches(password string) bool {
	key := argon2.IDKey([]byte(password), h.salt, h.time, h.memory, h.threads, uint32(len(h.key)))
	return subtle.ConstantTimeCompare(key, h.key) == 1
}

// passwordAlphabet leaves out letters and digits that look alike.
const passwordAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GeneratePassword returns a random password of 20 characters in groups of 5, such as
// k7mfq-2xhpa-9tnwe-c3rjd.
func GeneratePassword() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(passwordAlphabet)))
	for i := range 20 {
		if i > 0 && i%5 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(passwordAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// CheckPassword says what is wrong with a new password, or returns nil.
func CheckPassword(password string) error {
	switch {
	case len([]rune(password)) < MinPasswordLength:
		return fmt.Errorf("the password needs at least %d characters", MinPasswordLength)
	case len(password) > maxPasswordLength:
		return fmt.Errorf("the password is longer than %d bytes", maxPasswordLength)
	}
	return nil
}

// StorePassword hashes password and saves the hash in stateDir, which it creates and
// makes private to this user. A problem with the directory is a *ConfigError.
func StorePassword(stateDir, password string) error {
	if err := prepareStateDir(stateDir, nil); err != nil {
		return err
	}
	return writePasswordHash(stateDir, password)
}

func writePasswordHash(stateDir, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(stateDir, passwordName), tempPassword, []byte(hash+"\n"))
}

// ReadPasswordFile reads a plain-text password file, such as a Docker secret: its
// content without one trailing newline and without a byte order mark.
func ReadPasswordFile(name string) (string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	return plainPassword(name, b)
}

func plainPassword(name string, b []byte) (string, error) {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if t, ok := bytes.CutSuffix(b, []byte("\n")); ok {
		b = bytes.TrimSuffix(t, []byte("\r"))
	}
	if len(b) == 0 {
		return "", fmt.Errorf("the password file %s is empty", name)
	}
	return string(b), nil
}

// writeFileAtomic replaces name with data through a temporary file of mode 0600 in the
// same directory.
func writeFileAtomic(name, tempPrefix string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), tempPrefix+"*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		os.Chmod(tmp, 0o600)
		err = adoptOwner(tmp)
	}
	if err == nil {
		err = os.Rename(tmp, name)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// passwordStore holds the hash that logins are checked against, and follows the file it
// comes from.
type passwordStore struct {
	file  string
	plain bool
	// fileMu keeps changed from reading the file between set's write and its load.
	fileMu sync.Mutex

	mu   sync.Mutex
	hash *phc
	raw  []byte
	sig  fileSig
}

// fileSig tells a changed file apart.
type fileSig struct {
	mod  time.Time
	size int64
}

func statSig(name string) (fileSig, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return fileSig{}, err
	}
	return fileSig{fi.ModTime(), fi.Size()}, nil
}

// openPasswordStore loads the password: from plainFile when set, else the hash in
// stateDir, which it creates with a new password when there is none; created is that
// password.
func openPasswordStore(stateDir, plainFile string) (ps *passwordStore, created string, err error) {
	if plainFile != "" {
		ps = &passwordStore{file: plainFile, plain: true}
		if err := ps.load(); err != nil {
			return nil, "", &ConfigError{err.Error()}
		}
		return ps, "", nil
	}
	ps = &passwordStore{file: filepath.Join(stateDir, passwordName)}
	if _, err := os.Stat(ps.file); errors.Is(err, os.ErrNotExist) {
		if created, err = GeneratePassword(); err != nil {
			return nil, "", err
		}
		if err := writePasswordHash(stateDir, created); err != nil {
			return nil, "", fmt.Errorf("save the password: %w", err)
		}
	}
	if err := ps.load(); err != nil {
		return nil, "", &ConfigError{err.Error()}
	}
	return ps, created, nil
}

// load reads the file; it keeps the current hash when the file is unusable.
func (ps *passwordStore) load() error {
	sig, err := statSig(ps.file)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(ps.file)
	if err != nil {
		return err
	}
	var h *phc
	if ps.plain {
		pw, err := plainPassword(ps.file, raw)
		if err != nil {
			return err
		}
		hash, err := HashPassword(pw)
		if err != nil {
			return err
		}
		h, _ = parsePHC(hash)
	} else {
		line, _, _ := bytes.Cut(raw, []byte("\n"))
		if h, err = parsePHC(string(line)); err != nil {
			return fmt.Errorf("%s: %v; set a new password with 'thronectl web passwd'", ps.file, err)
		}
	}
	ps.mu.Lock()
	ps.hash, ps.raw, ps.sig = h, raw, sig
	ps.mu.Unlock()
	return nil
}

// current returns the hash to check against.
func (ps *passwordStore) current() *phc {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.hash
}

// changed reloads the file when it changed on disk and reports whether the password
// changed with it.
func (ps *passwordStore) changed() (bool, error) {
	ps.fileMu.Lock()
	defer ps.fileMu.Unlock()
	sig, err := statSig(ps.file)
	ps.mu.Lock()
	same, raw := sig == ps.sig, ps.raw
	ps.mu.Unlock()
	if err != nil || same {
		return false, err
	}
	b, err := os.ReadFile(ps.file)
	if err != nil {
		return false, err
	}
	if bytes.Equal(b, raw) {
		ps.mu.Lock()
		ps.sig = sig
		ps.mu.Unlock()
		return false, nil
	}
	if err := ps.load(); err != nil {
		return false, err
	}
	return true, nil
}

// set saves a new password; the store follows the new file without counting it as a
// change made elsewhere.
func (ps *passwordStore) set(stateDir, password string) error {
	ps.fileMu.Lock()
	defer ps.fileMu.Unlock()
	if err := writePasswordHash(stateDir, password); err != nil {
		return err
	}
	return ps.load()
}
