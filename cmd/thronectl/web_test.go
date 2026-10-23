package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/throneproj/Throne-api/internal/web"
	"golang.org/x/crypto/argon2"
)

const webKey = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"

// lockedBuffer is a bytes.Buffer that a running command and the test share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func webRun(t *testing.T, env map[string]string, stdin string, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut, func(k string) string { return env[k] })
	if strings.Contains(out.String()+errOut.String(), webKey) {
		t.Fatalf("thronectl %v printed the key", args)
	}
	return code, out.String() + errOut.String()
}

func TestWebUsageErrors(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"THRONE_KEY": webKey, "THRONE_WEB_STATE_DIR": dir}
	empty := filepath.Join(dir, "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	cases := []struct {
		env  map[string]string
		args []string
		want string
	}{
		{map[string]string{"THRONE_WEB_STATE_DIR": dir}, []string{"web"}, "no key"},
		{env, []string{"web", "--listen", "localhost:80"}, "--listen: \"localhost\" is not an IP address"},
		{map[string]string{"THRONE_KEY": webKey, "THRONE_WEB_LISTEN": "::1:80"}, []string{"web"}, "THRONE_WEB_LISTEN:"},
		{env, []string{"web", "--tls", "maybe"}, "--tls: want auto, on or off"},
		{env, []string{"web", "--tls-cert", "c.pem"}, "--tls-cert needs a key too"},
		{env, []string{"web", "--tls", "off", "--tls-cert", "c.pem", "--tls-key", "k.pem"}, "turns TLS off"},
		{env, []string{"web", "--tls-cert", "missing.pem", "--tls-key", "missing.pem"}, "TLS certificate:"},
		{env, []string{"web", "--password-file", empty}, "is empty"},
		{env, []string{"web", "--password-file", filepath.Join(dir, "missing")}, "missing"},
		{env, []string{"web", "serve"}, "the only subcommand is passwd"},
		{env, []string{"web", "passwd", "--listen", "9000"}, "--listen does not go with passwd"},
		{env, []string{"web", "passwd", "extra"}, "unexpected argument"},
	}
	for _, c := range cases {
		code, out := webRun(t, c.env, "", c.args...)
		if code != 2 || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d, %q, want %q", c.args, code, out, c.want)
		}
	}
}

func TestWebConfig(t *testing.T) {
	env := map[string]string{
		"THRONE_WEB_LISTEN":        "*:9443",
		"THRONE_WEB_STATE_DIR":     "/env/state",
		"THRONE_WEB_TLS":           "off",
		"THRONE_WEB_ALLOW_HOST":    "a.lan, b.lan c.lan",
		"THRONE_WEB_PASSWORD_FILE": "/run/secrets/pw",
		"STATE_DIRECTORY":          "/var/lib/a" + string(os.PathListSeparator) + "/var/lib/b",
	}
	c := &cli{getenv: func(k string) string { return env[k] }}
	cfg, err := c.webConfig(webFlags{})
	if err != nil {
		t.Fatal(err)
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	if cfg.Listen != ":9443" || cfg.StateDir != abs("/env/state") || cfg.TLS || cfg.PasswordFile != "/run/secrets/pw" ||
		strings.Join(cfg.AllowHosts, "|") != "a.lan|b.lan|c.lan" {
		t.Errorf("from the environment: %+v", cfg)
	}
	cfg, err = c.webConfig(webFlags{listen: "9000", stateDir: "flag-state", tls: "on", allow: "x.lan", passwordFile: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9000" || cfg.StateDir != abs("flag-state") || !cfg.TLS || cfg.PasswordFile != "pw" ||
		strings.Join(cfg.AllowHosts, "|") != "x.lan" {
		t.Errorf("from flags: %+v", cfg)
	}

	env = map[string]string{"STATE_DIRECTORY": env["STATE_DIRECTORY"]}
	cfg, _ = c.webConfig(webFlags{})
	if cfg.Listen != web.DefaultListen || cfg.TLS || cfg.StateDir != abs("/var/lib/a") || len(cfg.AllowHosts) != 0 {
		t.Errorf("defaults: %+v", cfg)
	}
	cfg, _ = c.webConfig(webFlags{listen: "[::]:9096"})
	if !cfg.TLS {
		t.Errorf("TLS is off on every address")
	}
	cfg, _ = c.webConfig(webFlags{cert: "c.pem", key: "k.pem"})
	if !cfg.TLS || cfg.TLSCert != "c.pem" || cfg.TLSKey != "k.pem" {
		t.Errorf("a certificate on loopback: %+v", cfg)
	}
	env = map[string]string{}
	if cfg, _ = c.webConfig(webFlags{}); !strings.HasSuffix(cfg.StateDir, filepath.Join("thronectl", "web")) {
		t.Errorf("default state directory %s", cfg.StateDir)
	}
}

func TestWebPasswd(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"THRONE_WEB_STATE_DIR": dir}
	if code, out := webRun(t, env, "short\n", "web", "passwd"); code != 2 || !strings.Contains(out, "at least 8") {
		t.Errorf("short password: exit %d, %q", code, out)
	}
	code, out := webRun(t, env, "a long secret\r\n", "web", "passwd")
	if code != 0 || !strings.Contains(out, "saved the new password") {
		t.Fatalf("passwd: exit %d, %q", code, out)
	}
	if !hashMatches(t, filepath.Join(dir, "password"), "a long secret") {
		t.Error("the stored hash does not match")
	}
	other := t.TempDir()
	if code, _ := webRun(t, nil, "flagged secret", "web", "passwd", "--state-dir", other); code != 0 ||
		!hashMatches(t, filepath.Join(other, "password"), "flagged secret") {
		t.Errorf("--state-dir: exit %d", code)
	}
	env["THRONE_WEB_PASSWORD_FILE"] = "/run/secrets/pw"
	if code, out := webRun(t, env, "a long secret\n", "web", "passwd"); code != 2 || !strings.Contains(out, "change that file") {
		t.Errorf("with a password file: exit %d, %q", code, out)
	}
	delete(env, "THRONE_WEB_PASSWORD_FILE")

	old, oldHidden := isTerminal, readHidden
	t.Cleanup(func() { isTerminal, readHidden = old, oldHidden })
	isTerminal = func(io.Reader) bool { return true }
	answers := []string{"typed secret", "typed secret"}
	readHidden = func(io.Reader) (string, error) {
		a := answers[0]
		answers = answers[1:]
		return a, nil
	}
	if code, out := webRun(t, env, "", "web", "passwd"); code != 0 || !strings.Contains(out, "Repeat it") ||
		!hashMatches(t, filepath.Join(dir, "password"), "typed secret") {
		t.Errorf("terminal: exit %d, %q", code, out)
	}
	answers = []string{"typed secret", "typed secreT"}
	if code, out := webRun(t, env, "", "web", "passwd"); code != 2 || !strings.Contains(out, "differ") {
		t.Errorf("different answers: exit %d, %q", code, out)
	}
}

// startWeb runs thronectl web until the returned stop, which returns the exit status,
// and waits for its first serving line.
func startWeb(t *testing.T, env map[string]string, args ...string) (stderr *lockedBuffer, url string, stop func() int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := webContext
	webContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { webContext = old })
	stderr = new(lockedBuffer)
	exited := make(chan int, 1)
	go func() {
		exited <- run(append([]string{"web"}, args...), strings.NewReader(""), io.Discard, stderr,
			func(k string) string { return env[k] })
	}()
	stop = func() int {
		cancel()
		select {
		case code := <-exited:
			return code
		case <-time.After(10 * time.Second):
			t.Fatal("thronectl web did not stop")
			return -1
		}
	}
	serving := regexp.MustCompile(`thronectl web: serving on (https?://127\.0\.0\.1:\d+/) for Throne at 127\.0\.0\.1:1\n`)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := serving.FindStringSubmatch(stderr.String()); m != nil {
			return stderr, m[1], stop
		}
		select {
		case code := <-exited:
			t.Fatalf("thronectl web exited with %d: %q", code, stderr.String())
		default:
		}
	}
	stop()
	t.Fatalf("no serving line: %q", stderr.String())
	return
}

func TestWebServe(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"THRONE_KEY": webKey, "THRONE_ADDR": "127.0.0.1:1"}
	stderr, url, stop := startWeb(t, env, "--listen", "127.0.0.1:0", "--state-dir", dir)
	m := regexp.MustCompile(`thronectl web: created the web UI password: (\S+)\nthronectl web: change it with 'thronectl web passwd'\n`).
		FindStringSubmatch(stderr.String())
	if m == nil {
		stop()
		t.Fatalf("no password line: %q", stderr.String())
	}
	body, _ := json.Marshal(map[string]string{"password": m[1]})
	resp, err := http.Post(url+"auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("login with the printed password: %d", resp.StatusCode)
	}
	if code := stop(); code != 0 {
		t.Errorf("exit %d: %q", code, stderr.String())
	}
	if out := stderr.String(); strings.Contains(out, "fingerprint") || strings.Contains(out, "warning") {
		t.Errorf("TLS messages on loopback: %q", out)
	}

	stderr, url, stop = startWeb(t, env, "--listen", "127.0.0.1:0", "--state-dir", dir, "--tls", "on")
	code := stop()
	out := stderr.String()
	if code != 0 || !strings.HasPrefix(url, "https://") || strings.Contains(out, "created the web UI password") ||
		!regexp.MustCompile(`TLS certificate SHA-256 fingerprint: ([0-9A-F]{2}:){31}[0-9A-F]{2}\n`).MatchString(out) {
		t.Errorf("TLS: exit %d, %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls", "cert.pem")); err != nil {
		t.Error(err)
	}

	host := strings.TrimSuffix(strings.TrimPrefix(url, "https://"), "/")
	holder, err := net.Listen("tcp", host)
	if err != nil {
		t.Skipf("cannot hold %s: %v", host, err)
	}
	defer holder.Close()
	code, out = webRun(t, env, "", "web", "--listen", host, "--state-dir", dir)
	if code != 3 || !strings.Contains(out, "--listen picks another address") {
		t.Errorf("address in use: exit %d, %q", code, out)
	}
}

// hashMatches checks a password against the argon2id hash in a password file.
func hashMatches(t *testing.T, name, password string) bool {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var m, it, p uint32
	parts := strings.Split(strings.TrimSpace(string(b)), "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		t.Fatalf("hash %q", b)
	}
	fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &it, &p)
	salt, _ := base64.RawStdEncoding.DecodeString(parts[4])
	want, _ := base64.RawStdEncoding.DecodeString(parts[5])
	return bytes.Equal(argon2.IDKey([]byte(password), salt, it, m, uint8(p), uint32(len(want))), want)
}
