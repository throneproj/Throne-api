package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/testserver"
)

// Keys from spec/vectors/v1.json.
const (
	testKey  = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	otherKey = "bljyVf7GZBkFc4ehd35siy7LduS9QD-st76wDYoRFsE"
)

const profilesJSON = `{"profiles":[
	{"id":0,"group":0,"name":"DE Frankfurt","type":"vless","address":"de1.example.com:443","latency_ms":85,
	 "download_bps":48300000,"upload_bps":-1,"country":"DE","ip_out":"203.0.113.7","running":true},
	{"id":1,"group":0,"name":"NL\u001b[2J","type":"trojan","address":"nl.example.com:443","latency_ms":-1},
	{"id":2,"group":0,"name":"FI","type":"vmess","address":"fi.example.com:443","latency_ms":-2}]}`

// fakeThrone answers like Throne, keeping a little state.
type fakeThrone struct {
	mu     sync.Mutex
	state  string
	tun    bool
	reqs   []*testserver.Request
	routes map[string]func(r *testserver.Request) *testserver.Response
}

// on makes the fake answer requests to "METHOD path" with h, which runs with f locked.
func (f *fakeThrone) on(route string, h func(r *testserver.Request) *testserver.Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.routes == nil {
		f.routes = map[string]func(*testserver.Request) *testserver.Response{}
	}
	f.routes[route] = h
}

func (f *fakeThrone) handle(_ context.Context, r *testserver.Request) *testserver.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	if h := f.routes[r.Method+" "+r.Path]; h != nil {
		return h(r)
	}
	switch r.Method + " " + r.Path {
	case "GET /v1/status":
	case "PUT /v1/connection":
		f.state = "running"
	case "DELETE /v1/connection":
		f.state = "idle"
	case "PUT /v1/modes":
		if on, ok := r.Fields()["tun"].(bool); ok {
			f.tun = on
		}
	case "GET /v1/profiles":
		return testserver.OK(json.RawMessage(profilesJSON))
	case "POST /v1/tests":
		return testserver.OK(json.RawMessage(`{"id":7,"kind":"latency","state":"done","profile_ids":[0,1],` +
			`"started_at":1760000000,"finished_at":1760000042,"results":[{"id":0,"name":"DE Frankfurt","latency_ms":85}]}`))
	case "PUT /v1/routes/active":
		return testserver.Error(409, "busy", "Another profile is starting...")
	default:
		return testserver.Error(404, "not_found", "Unknown path")
	}
	st := map[string]any{"state": f.state, "profile": nil, "last_profile_id": 0, "tun": f.tun, "system_proxy": false,
		"route": map[string]any{"id": 1, "name": "Default", "active": true}, "kill_switch": "off", "core": true,
		"inbound": map[string]any{"enabled": true, "address": "127.0.0.1", "port": 2080}, "version": "1.4.0",
		"capabilities": map[string]any{"tun": true, "kill_switch": false, "headless": true}, "restart_needed": true,
		"degraded": "core_unavailable", "added_later": "kept"}
	if f.state == "running" {
		st["profile"] = map[string]any{"id": 0, "name": "DE Frankfurt", "type": "vless", "running": true}
	}
	return testserver.OK(st)
}

func (f *fakeThrone) requests() []*testserver.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*testserver.Request(nil), f.reqs...)
}

func startFake(t *testing.T) (*fakeThrone, map[string]string) {
	t.Helper()
	f := &fakeThrone{state: "idle"}
	srv := testserver.Start(t, testserver.Config{Key: testKey, Handler: f.handle})
	return f, map[string]string{"THRONE_ADDR": srv.Addr, "THRONE_KEY": testKey}
}

func runCLI(t *testing.T, env map[string]string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return runInput(t, env, "", args...)
}

// runInput runs thronectl with input on its standard input.
func runInput(t *testing.T, env map[string]string, input string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(input), &out, &errOut, func(k string) string { return env[k] })
	for _, key := range []string{testKey, otherKey} {
		if strings.Contains(out.String(), key) || strings.Contains(errOut.String(), key) {
			t.Fatalf("thronectl %v printed a key", args)
		}
	}
	return code, out.String(), errOut.String()
}

func TestUsageErrors(t *testing.T) {
	env := map[string]string{"THRONE_ADDR": "127.0.0.1:1", "THRONE_KEY": testKey}
	for _, args := range [][]string{
		{},
		{"nope"},
		{"--key", "not-a-key", "status"},
		{"status", "extra"},
		{"connect"},
		{"connect", "x"},
		{"connect", "12", "--group", "1"},
		{"connect", "last", "--name", "DE"},
		{"connect", "best", "--group", "x"},
		{"connect", "best", "--max-age", "soon"},
		{"tun"},
		{"tun", "maybe"},
		{"proxy", "on", "off"},
		{"route", "x"},
		{"test", "fast", "--group", "1"},
		{"test", "latency"},
		{"test", "latency", "--group", "1", "5"},
		{"test", "latency", "--name", "DE", "5"},
		{"test", "latency", "x"},
		{"clear"},
		{"job"},
		{"cancel", "x"},
		{"traffic", "--by", "day"},
		{"traffic", "--since", "-1h"},
		{"profiles", "--ids", "1,x"},
		{"call", "GET"},
		{"call", "GET", "/v1/status", "novalue"},
		{"call", "PUT", "/v1/modes", "--body", "{bad"},
		{"help", "nope"},
		{"import"},
		{"import", "links.txt", "--url", "https://example.com/x"},
		{"import", "--url", "ftp://example.com/x"},
		{"import", "a", "b"},
		{"sub"},
		{"sub", "nope"},
		{"sub", "add"},
		{"sub", "add", "ftp://example.com/x"},
		{"sub", "add", "https://example.com/x", "extra"},
		{"sub", "add", "https://example.com/x", "--all"},
		{"sub", "update"},
		{"sub", "update", "3", "--all"},
		{"sub", "update", "--all", "--no-update"},
		{"sub", "update", "x"},
		{"group", "add"},
		{"group", "move", "1"},
		{"group", "add", "Mine", "--no-wait"},
		{"group", "rm", "x"},
		{"profile", "rm"},
		{"profile", "del", "1"},
		{"profile", "rm", "--name", "DE", "1"},
		{"settings", "set"},
		{"settings", "set", "novalue"},
		{"settings", "set", "=1"},
		{"settings", "set", "a=1", "a=2"},
		{"backup"},
		{"backup", "make"},
		{"backup", "create", "extra"},
		{"backup", "create", "--yes"},
		{"backup", "create", "--parts", "profiles,nope"},
		{"backup", "restore"},
		{"backup", "restore", "a.thrbackup", "-o", "b"},
		{"backup", "restore", "a.thrbackup"},
		{"rulesets"},
		{"rulesets", "go"},
		{"geo", "fetch"},
		{"geo", "status", "x"},
		{"geo", "--only-missing"},
		{"geo", "update", "geocity"},
		{"restart", "now"},
		{"shutdown"},
		{"keygen", "x"},
		{"completion"},
		{"completion", "tcsh"},
		{"completion", "bash", "zsh"},
	} {
		code, stdout, stderr := runCLI(t, env, args...)
		if code != 2 || stderr == "" || stdout != "" {
			t.Errorf("thronectl %q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestHelpAndVersion(t *testing.T) {
	if code, stdout, _ := runCLI(t, nil, "help"); code != 0 || !strings.Contains(stdout, "connect") {
		t.Errorf("help: exit %d, %q", code, stdout)
	}
	if code, stdout, _ := runCLI(t, nil, "help", "test"); code != 0 || !strings.Contains(stdout, "Usage: thronectl test") {
		t.Errorf("help test: exit %d, %q", code, stdout)
	}
	if code, _, stderr := runCLI(t, nil, "connect", "-h"); code != 0 || !strings.Contains(stderr, "-max-age") {
		t.Errorf("connect -h: exit %d, %q", code, stderr)
	}
	defer func(v string) { version = v }(version)
	version = "v1.2.3"
	if code, stdout, _ := runCLI(t, nil, "version"); code != 0 || stdout != "thronectl v1.2.3\n" {
		t.Errorf("version: exit %d, %q", code, stdout)
	}
}

func TestKeySources(t *testing.T) {
	f, env := startFake(t)
	if code, _, stderr := runCLI(t, map[string]string{"THRONE_ADDR": env["THRONE_ADDR"]}, "status"); code != 2 ||
		!strings.Contains(stderr, "THRONE_KEY") {
		t.Errorf("no key: exit %d, %q", code, stderr)
	}
	if code, _, stderr := runCLI(t, map[string]string{"THRONE_KEY": "abc"}, "status"); code != 2 || stderr == "" {
		t.Errorf("invalid THRONE_KEY: exit %d, %q", code, stderr)
	}

	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileEnv := map[string]string{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY_FILE": keyFile}
	if code, _, stderr := runCLI(t, fileEnv, "status"); code != 0 {
		t.Errorf("THRONE_KEY_FILE: exit %d, %q", code, stderr)
	}
	flagEnv := map[string]string{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY": otherKey}
	if code, _, stderr := runCLI(t, flagEnv, "status", "--key-file", keyFile); code != 0 {
		t.Errorf("--key-file over THRONE_KEY: exit %d, %q", code, stderr)
	}
	if n := len(f.requests()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestStatus(t *testing.T) {
	_, env := startFake(t)
	code, stdout, stderr := runCLI(t, env, "status")
	if code != 0 || !strings.Contains(stdout, "idle") || !strings.Contains(stdout, "127.0.0.1:2080") {
		t.Fatalf("status: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, want := range []string{"Restart needed:  yes", "Degraded:        core_unavailable", "Capabilities:    tun, headless"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status output lacks %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = runCLI(t, env, "--json", "status")
	var body map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &body) != nil || body["added_later"] != "kept" {
		t.Fatalf("status --json: exit %d, %q", code, stdout)
	}
}

func TestStatusTunCapability(t *testing.T) {
	f, env := startFake(t)
	for _, tc := range []struct{ tun, want string }{
		{"true", "Capabilities:    tun, headless\n"},
		{"false", "Capabilities:    headless\n"},
		{"null", "Capabilities:    tun (unknown), headless\n"},
	} {
		f.on("GET /v1/status", ok(`{"state":"idle","profile":null,"last_profile_id":-1,`+
			`"capabilities":{"tun":`+tc.tun+`,"kill_switch":false,"headless":true},"restart_needed":false}`))
		if code, stdout, stderr := runCLI(t, env, "status"); code != 0 || !strings.Contains(stdout, tc.want) {
			t.Errorf("tun %s: exit %d, stderr %q, output lacks %q:\n%s", tc.tun, code, stderr, tc.want, stdout)
		}
	}
}

func TestConnect(t *testing.T) {
	f, env := startFake(t)
	tests := []struct {
		args []string
		want map[string]any
	}{
		{[]string{"connect", "0", "--restart", "--no-wait"}, map[string]any{"profile": 0.0, "restart": true, "wait": false}},
		{[]string{"connect", "last", "--no-wait"}, map[string]any{"select": "last", "wait": false}},
		{[]string{"connect", "best", "--group", "0", "--name", "DE|NL", "--max-age", "0", "--no-wait"},
			map[string]any{"select": "lowest_latency", "group": 0.0, "name": "DE|NL", "max_age": 0.0, "wait": false}},
		{[]string{"connect", "--no-wait", "best", "--max-age", "2d"},
			map[string]any{"select": "lowest_latency", "max_age": 172800.0, "wait": false}},
	}
	for _, tt := range tests {
		if code, stdout, stderr := runCLI(t, env, tt.args...); code != 0 || !strings.Contains(stdout, "running") {
			t.Fatalf("%q: exit %d, stdout %q, stderr %q", tt.args, code, stdout, stderr)
		}
		reqs := f.requests()
		if got := reqs[len(reqs)-1].Fields(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: body %v, want %v", tt.args, got, tt.want)
		}
	}

	if code, _, _ := runCLI(t, env, "--timeout", "20s", "connect", "0"); code != 0 {
		t.Fatalf("connect with --timeout: exit %d", code)
	}
	reqs := f.requests()
	if timeout, _ := reqs[len(reqs)-1].Fields()["timeout"].(float64); timeout < 16 || timeout > 18 {
		t.Errorf("timeout field %v for --timeout 20s", timeout)
	}
}

func TestToggles(t *testing.T) {
	f, env := startFake(t)
	steps := []struct {
		args   []string
		method string
		body   map[string]any
	}{
		{[]string{"toggle"}, "PUT", map[string]any{"select": "last"}},
		{[]string{"toggle"}, "DELETE", nil},
		{[]string{"tun", "toggle"}, "PUT", map[string]any{"tun": true}},
		{[]string{"tun", "toggle"}, "PUT", map[string]any{"tun": false}},
		{[]string{"proxy", "on", "--no-wait"}, "PUT", map[string]any{"system_proxy": true, "wait": false}},
	}
	for _, s := range steps {
		before := len(f.requests())
		if code, _, stderr := runCLI(t, env, append(s.args, "--timeout", "0")...); code != 0 {
			t.Fatalf("%q: exit %d, %q", s.args, code, stderr)
		}
		reqs := f.requests()[before:]
		last := reqs[len(reqs)-1]
		if last.Method != s.method || !reflect.DeepEqual(last.Fields(), s.body) {
			t.Errorf("%q: sent %s %s %s", s.args, last.Method, last.Path, last.Body)
		}
		if strings.Contains(strings.Join(s.args, " "), "toggle") && (len(reqs) != 2 || reqs[0].Path != "/v1/status") {
			t.Errorf("%q: %d requests, want a status read first", s.args, len(reqs))
		}
	}
}

func TestProfilesAndTest(t *testing.T) {
	f, env := startFake(t)
	code, stdout, stderr := runCLI(t, env, "profiles", "--group", "0", "--name", "DE", "--ids", "0,1")
	if code != 0 {
		t.Fatalf("profiles: exit %d, %q", code, stderr)
	}
	for _, want := range []string{"85 ms", "unavailable", "connect only", "48.30 Mbps", "failed", "*", "NL [2J"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("profiles output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "\x1b") {
		t.Error("profiles printed an escape character from a name")
	}
	reqs := f.requests()
	if q := reqs[len(reqs)-1].Query; !reflect.DeepEqual(q, map[string]string{"group": "0", "name": "DE", "ids": "0,1"}) {
		t.Errorf("profiles query %v", q)
	}

	code, stdout, stderr = runCLI(t, env, "test", "latency", "0", "1")
	if code != 0 || !strings.Contains(stdout, "done") || !strings.Contains(stdout, "DE Frankfurt") {
		t.Fatalf("test: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	reqs = f.requests()
	body := reqs[len(reqs)-1].Fields()
	if body["kind"] != "latency" || body["wait"] != true || !reflect.DeepEqual(body["profiles"], []any{0.0, 1.0}) {
		t.Errorf("test body %v", body)
	}
}

func TestErrors(t *testing.T) {
	_, env := startFake(t)
	if code, _, stderr := runCLI(t, env, "route", "2"); code != 1 || stderr != "error: busy: Another profile is starting...\n" {
		t.Errorf("API error: exit %d, %q", code, stderr)
	}
	wrong := map[string]string{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY": otherKey}
	if code, _, stderr := runCLI(t, wrong, "status"); code != 3 || !strings.Contains(stderr, "key is probably wrong") {
		t.Errorf("wrong key: exit %d, %q", code, stderr)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := map[string]string{"THRONE_ADDR": ln.Addr().String(), "THRONE_KEY": testKey}
	ln.Close()
	if code, _, stderr := runCLI(t, closed, "status"); code != 3 || !strings.Contains(stderr, "Is Throne running") {
		t.Errorf("nothing listening: exit %d, %q", code, stderr)
	}
}

func TestCall(t *testing.T) {
	f, env := startFake(t)
	code, stdout, stderr := runCLI(t, env, "call", "get", "v1/status", "a=1", "b=x=y", "--body", `{"x":[1]}`)
	if code != 0 {
		t.Fatalf("call: exit %d, %q", code, stderr)
	}
	r := f.requests()[0]
	if r.Method != "GET" || r.Path != "/v1/status" || !reflect.DeepEqual(r.Query, map[string]string{"a": "1", "b": "x=y"}) ||
		string(r.Body) != `{"x":[1]}` {
		t.Errorf("request %s %s %v %s", r.Method, r.Path, r.Query, r.Body)
	}
	var body map[string]any
	if json.Unmarshal([]byte(stdout), &body) != nil || body["state"] != "idle" || !strings.Contains(stdout, "\n  ") {
		t.Errorf("call output %q", stdout)
	}
	if code, _, stderr := runCLI(t, env, "call", "GET", "/v1/nope"); code != 1 || !strings.Contains(stderr, "not_found") {
		t.Errorf("call to an unknown path: exit %d, %q", code, stderr)
	}
}

// last returns the last request the fake received.
func (f *fakeThrone) last(t *testing.T) *testserver.Request {
	t.Helper()
	reqs := f.requests()
	if len(reqs) == 0 {
		t.Fatal("no request")
	}
	return reqs[len(reqs)-1]
}

// routesOf lists requests as "METHOD path".
func routesOf(reqs []*testserver.Request) []string {
	var out []string
	for _, r := range reqs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func ok(body string) func(*testserver.Request) *testserver.Response {
	return func(*testserver.Request) *testserver.Response { return testserver.OK(json.RawMessage(body)) }
}

// asTerminal makes thronectl take its standard input for a terminal until the test ends.
func asTerminal(t *testing.T) {
	old := isTerminal
	isTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { isTerminal = old })
}

func TestImport(t *testing.T) {
	f, env := startFake(t)
	f.on("POST /v1/import", ok(`{"group":3,"imported":2,"profile_ids":[40,41],"failed":1,"warned":0,"issues":[`+
		`{"severity":"error","field":"port","message":"Port out of range","count":1,"examples":["vless://h:70000 #x"]}],"errors":[]}`))
	dir := t.TempDir()
	file := filepath.Join(dir, "links.txt")
	if err := os.WriteFile(file, []byte("\xEF\xBB\xBFvless://a\nss://b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		args  []string
		input string
		want  map[string]any
	}{
		{[]string{"import", file, "--group", "0"}, "", map[string]any{"as": "profiles", "text": "vless://a\nss://b\n", "group": 0.0}},
		{[]string{"import", "-", "--no-wait"}, "trojan://c", map[string]any{"as": "profiles", "text": "trojan://c", "wait": false}},
		{[]string{"import", "--url", "https://example.com/list.txt"}, "", map[string]any{"as": "url", "url": "https://example.com/list.txt"}},
	}
	for _, tt := range tests {
		code, stdout, stderr := runInput(t, env, tt.input, append(tt.args, "--timeout", "0")...)
		if code != 0 {
			t.Fatalf("%q: exit %d, %q", tt.args, code, stderr)
		}
		for _, want := range []string{"Imported 2 profiles into group 3.", "1 entries failed", "Port out of range", "vless://h:70000 #x"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%q: output lacks %q:\n%s", tt.args, want, stdout)
			}
		}
		if got := f.last(t).Fields(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: body %v, want %v", tt.args, got, tt.want)
		}
	}

	f.on("POST /v1/import", func(*testserver.Request) *testserver.Response {
		return testserver.Accepted(map[string]any{"group": 3, "state": "queued"})
	})
	if code, stdout, _ := runCLI(t, env, "import", file, "--no-wait"); code != 0 || !strings.Contains(stdout, "still running") {
		t.Errorf("queued import: exit %d, %q", code, stdout)
	}

	before := len(f.requests())
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, bytes.Repeat([]byte("a"), maxImport+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, env, "import", big); code != 2 || !strings.Contains(stderr, "split it") {
		t.Errorf("file too large: exit %d, %q", code, stderr)
	}
	if code, _, stderr := runInput(t, env, strings.Repeat("a", maxImport+1), "import", "-"); code != 2 ||
		!strings.Contains(stderr, "standard input holds more than 900 KiB") {
		t.Errorf("standard input too large: exit %d, %q", code, stderr)
	}
	if code, _, stderr := runInput(t, env, " \n", "import", "-"); code != 2 || !strings.Contains(stderr, "empty") {
		t.Errorf("empty input: exit %d, %q", code, stderr)
	}
	if code, _, _ := runCLI(t, env, "import", filepath.Join(dir, "missing.txt")); code != 3 {
		t.Errorf("missing file: exit %d", code)
	}
	if n := len(f.requests()) - before; n != 0 {
		t.Errorf("%d requests for refused imports", n)
	}
}

func TestSub(t *testing.T) {
	f, env := startFake(t)
	f.on("POST /v1/groups", func(r *testserver.Request) *testserver.Response {
		fields := r.Fields()
		g := map[string]any{"id": 5, "name": fields["name"], "subscription": true}
		switch {
		case fields["update"] == false:
			return testserver.OK(map[string]any{"group": g, "update": nil})
		case fields["name"] == "broken":
			return testserver.OK(map[string]any{"group": g, "update": map[string]any{"ok": false, "error": "HTTP 404"}})
		}
		return testserver.OK(map[string]any{"group": g, "update": map[string]any{"ok": true, "added": 3}})
	})
	f.on("POST /v1/groups/5/update", ok(`{"group":{"id":5,"name":"A","subscription":true},`+
		`"update":{"ok":true,"added":1,"updated":2,"removed":3,"kept":4}}`))
	f.on("POST /v1/groups/6/update", func(*testserver.Request) *testserver.Response {
		return testserver.Error(502, "fetch_failed", "HTTP 503")
	})
	f.on("POST /v1/groups/update", func(r *testserver.Request) *testserver.Response {
		if r.Fields()["wait"] == false {
			return testserver.Accepted(map[string]any{"queued": []int{5, 6}})
		}
		return testserver.OK(json.RawMessage(`{"results":[{"id":5,"ok":true,"added":1},{"id":6,"ok":false,"error":"HTTP 503"}]}`))
	})

	steps := []struct {
		args   []string
		code   int
		stdout string
		body   map[string]any
	}{
		{[]string{"sub", "add", "https://sub.example.com/x"}, 0, "Update: 3 added, 0 updated",
			map[string]any{"name": "sub.example.com", "url": "https://sub.example.com/x"}},
		{[]string{"sub", "add", "https://sub.example.com/x", "--name", "Provider", "--no-auto-update", "--no-update"}, 0,
			"Added group 5 Provider.", map[string]any{"name": "Provider", "url": "https://sub.example.com/x", "auto_update": false, "update": false}},
		{[]string{"sub", "add", "--name", "broken", "http://sub.example.com:8080/x"}, 1, "Update: failed: HTTP 404",
			map[string]any{"name": "broken", "url": "http://sub.example.com:8080/x"}},
		{[]string{"sub", "update", "5"}, 0, "1 added, 2 updated, 3 removed, 4 kept", nil},
		{[]string{"sub", "update", "--all"}, 1, "failed: HTTP 503", nil},
		{[]string{"sub", "update", "--all", "--no-wait"}, 0, "Still updating in Throne: 5, 6", map[string]any{"wait": false}},
	}
	for _, s := range steps {
		code, stdout, stderr := runCLI(t, env, append(s.args, "--timeout", "0")...)
		if code != s.code || !strings.Contains(stdout, s.stdout) {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", s.args, code, stdout, stderr)
		}
		if s.code == 1 && !strings.Contains(stderr, "failed") {
			t.Errorf("%q: stderr %q", s.args, stderr)
		}
		if got := f.last(t).Fields(); !reflect.DeepEqual(got, s.body) {
			t.Errorf("%q: body %v, want %v", s.args, got, s.body)
		}
	}

	if code, _, stderr := runCLI(t, env, "sub", "update", "6"); code != 1 || stderr != "error: fetch_failed: HTTP 503\n" {
		t.Errorf("failed update: exit %d, %q", code, stderr)
	}
	before := len(f.requests())
	code, stdout, stderr := runCLI(t, env, "--json", "sub", "update", "5,6", "--timeout", "0")
	var got struct {
		Results []map[string]any `json:"results"`
	}
	if code != 1 || json.Unmarshal([]byte(stdout), &got) != nil || !strings.Contains(stderr, "1 of 2 updates failed") {
		t.Fatalf("update of two groups: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if len(got.Results) != 2 || got.Results[0]["id"] != 5.0 || got.Results[0]["kept"] != 4.0 ||
		got.Results[1]["id"] != 6.0 || got.Results[1]["ok"] != false || got.Results[1]["error"] != "HTTP 503" {
		t.Errorf("results %v", got.Results)
	}
	want := []string{"POST /v1/groups/5/update", "POST /v1/groups/6/update"}
	if routes := routesOf(f.requests()[before:]); !reflect.DeepEqual(routes, want) {
		t.Errorf("requests %v, want %v", routes, want)
	}
}

func TestGroupAndProfile(t *testing.T) {
	f, env := startFake(t)
	f.on("POST /v1/groups", ok(`{"group":{"id":7,"name":"Mine"},"update":null}`))
	f.on("DELETE /v1/groups/0", ok(`{"deleted":0}`))
	f.on("DELETE /v1/groups/3", func(*testserver.Request) *testserver.Response {
		return testserver.Accepted(map[string]any{"deleted": 3, "state": "stopping"})
	})
	f.on("DELETE /v1/profiles", func(r *testserver.Request) *testserver.Response {
		n := 4
		if ids, ok := r.Fields()["profiles"].([]any); ok {
			n = len(ids)
		}
		return testserver.OK(map[string]any{"deleted": n})
	})
	steps := []struct {
		args         []string
		method, path string
		stdout       string
		body         map[string]any
	}{
		{[]string{"group", "add", "Mine"}, "POST", "/v1/groups", "Added group 7 Mine.\n", map[string]any{"name": "Mine"}},
		{[]string{"group", "rm", "0"}, "DELETE", "/v1/groups/0", "Deleted group 0.\n", nil},
		{[]string{"group", "rm", "3", "--no-wait"}, "DELETE", "/v1/groups/3",
			"Throne is stopping the running profile; it deletes group 3 after that.\n", map[string]any{"wait": false}},
		{[]string{"profile", "rm", "1", "2,0"}, "DELETE", "/v1/profiles", "Deleted 3 profiles.\n",
			map[string]any{"profiles": []any{1.0, 2.0, 0.0}}},
		{[]string{"profile", "rm", "--group", "0", "--name", "DE"}, "DELETE", "/v1/profiles", "Deleted 4 profiles.\n",
			map[string]any{"group": 0.0, "name": "DE"}},
	}
	for _, s := range steps {
		code, stdout, stderr := runCLI(t, env, append(s.args, "--timeout", "0")...)
		if code != 0 || stdout != s.stdout {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", s.args, code, stdout, stderr)
		}
		r := f.last(t)
		if r.Method != s.method || r.Path != s.path || !reflect.DeepEqual(r.Fields(), s.body) {
			t.Errorf("%q: sent %s %s %s", s.args, r.Method, r.Path, r.Body)
		}
	}
}

func TestSettings(t *testing.T) {
	f, env := startFake(t)
	f.on("GET /v1/settings", ok(`{"settings":{"log_level":"info","tun_mtu":9000,"kill_switch":false,"ntp_server":"",`+
		`"test_url":"https://cp.cloudflare.com/"}}`))
	f.on("PUT /v1/settings", ok(`{"settings":{"log_level":"debug","tun_mtu":1400,"kill_switch":true,"ntp_server":"",`+
		`"test_url":"https://cp.cloudflare.com/"},"changed":["log_level","tun_mtu"],"restart_needed":true}`))

	want := "SETTING      VALUE\n" +
		"kill_switch  false\n" +
		"log_level    info\n" +
		"ntp_server   \"\"\n" +
		"test_url     https://cp.cloudflare.com/\n" +
		"tun_mtu      9000\n"
	for _, args := range [][]string{{"settings"}, {"settings", "get"}} {
		if code, stdout, stderr := runCLI(t, env, args...); code != 0 || stdout != want {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	if code, stdout, _ := runCLI(t, env, "settings", "get", "tun_mtu", "log_level"); code != 0 ||
		stdout != "SETTING    VALUE\ntun_mtu    9000\nlog_level  info\n" {
		t.Errorf("settings get two: exit %d, %q", code, stdout)
	}
	code, stdout, _ := runCLI(t, env, "--json", "settings", "tun_mtu")
	var picked map[string]map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &picked) != nil ||
		!reflect.DeepEqual(picked, map[string]map[string]any{"settings": {"tun_mtu": 9000.0}}) {
		t.Errorf("settings --json tun_mtu: exit %d, %q", code, stdout)
	}
	if code, _, stderr := runCLI(t, env, "settings", "nope"); code != 2 || !strings.Contains(stderr, `no setting "nope"`) {
		t.Errorf("unknown setting: exit %d, %q", code, stderr)
	}

	code, stdout, stderr := runCLI(t, env, "settings", "set", "log_level=debug", "tun_mtu=1400", "kill_switch=true",
		"ntp_interval=30m", "remote_dns=1.1.1.1", "inbound_port=-5", "ntp_server=", "test_url=https://x.example/?a=b")
	if code != 0 {
		t.Fatalf("settings set: exit %d, %q", code, stderr)
	}
	r := f.last(t)
	wantBody := map[string]any{"settings": map[string]any{"log_level": "debug", "tun_mtu": 1400.0, "kill_switch": true,
		"ntp_interval": "30m", "remote_dns": "1.1.1.1", "inbound_port": -5.0, "ntp_server": "", "test_url": "https://x.example/?a=b"}}
	if r.Method != "PUT" || !reflect.DeepEqual(r.Fields(), wantBody) || !strings.Contains(string(r.Body), `"tun_mtu":1400`) {
		t.Errorf("settings set sent %s %s", r.Method, r.Body)
	}
	for _, want := range []string{"log_level  debug\n", "tun_mtu    1400\n", "thronectl connect last --restart"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("settings set output lacks %q:\n%s", want, stdout)
		}
	}
	f.on("PUT /v1/settings", ok(`{"settings":{"log_level":"debug"},"changed":[],"restart_needed":false}`))
	if code, stdout, _ := runCLI(t, env, "settings", "set", "log_level=debug"); code != 0 || stdout != "Nothing changed.\n" {
		t.Errorf("unchanged setting: exit %d, %q", code, stdout)
	}
}

// fakeArchive passes for a backup archive as far as thronectl looks.
var fakeArchive = append([]byte("THRN\x01\x00\x00\x00"), bytes.Repeat([]byte("groups and profiles "), 50)...)

// serveBackup makes f serve archive as backup 1 with this SHA-256.
func serveBackup(f *fakeThrone, archive []byte, sha string) {
	var parts []any
	f.on("POST /v1/backups", func(r *testserver.Request) *testserver.Response {
		parts, _ = r.Fields()["parts"].([]any)
		if parts == nil {
			parts = []any{"profiles", "routes", "settings", "otp", "ip_lists", "icons"}
		}
		return testserver.OK(map[string]any{"id": 1, "size": len(archive), "sha256": sha, "parts": parts})
	})
	f.on("GET /v1/backups/1", func(r *testserver.Request) *testserver.Response {
		offset, _ := strconv.Atoi(r.Query["offset"])
		length, _ := strconv.Atoi(r.Query["length"])
		end := min(offset+length, len(archive))
		return testserver.OK(map[string]any{"id": 1, "size": len(archive), "sha256": sha, "parts": parts, "offset": offset,
			"data": archive[offset:end], "eof": end == len(archive)})
	})
	f.on("DELETE /v1/backups/1", ok(`{"deleted":1}`))
}

func TestBackupCreate(t *testing.T) {
	f, env := startFake(t)
	sum := sha256.Sum256(fakeArchive)
	sha := hex.EncodeToString(sum[:])
	serveBackup(f, fakeArchive, sha)
	dir := t.TempDir()
	out := filepath.Join(dir, "b.thrbackup")
	code, stdout, stderr := runCLI(t, env, "backup", "create", "-o", out, "--parts", "profiles,ip-lists", "--timeout", "0")
	if code != 0 {
		t.Fatalf("backup create: exit %d, %q", code, stderr)
	}
	if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, fakeArchive) {
		t.Fatalf("archive file: %v", err)
	}
	if fi, err := os.Stat(out); runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o600) {
		t.Errorf("archive file mode %v, %v", fi.Mode(), err)
	}
	for _, want := range []string{sha, "profiles, ip_lists", "plain text"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("backup create output lacks %q:\n%s", want, stdout)
		}
	}
	reqs := f.requests()
	want := []string{"POST /v1/backups", "GET /v1/backups/1", "DELETE /v1/backups/1"}
	if routes := routesOf(reqs); !reflect.DeepEqual(routes, want) {
		t.Errorf("requests %v, want %v", routes, want)
	}
	if body := reqs[0].Fields(); !reflect.DeepEqual(body, map[string]any{"parts": []any{"profiles", "ip_lists"}}) {
		t.Errorf("POST body %v", body)
	}

	t.Chdir(dir)
	code, stdout, _ = runCLI(t, env, "--json", "backup", "create", "--timeout", "0")
	var info map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &info) != nil || info["sha256"] != sha || info["size"] != float64(len(fakeArchive)) {
		t.Errorf("backup create --json: exit %d, %q", code, stdout)
	}
	if named, _ := filepath.Glob(filepath.Join(dir, "throne-backup-*.thrbackup")); len(named) != 1 {
		t.Errorf("default file names %v", named)
	}

	serveBackup(f, fakeArchive, strings.Repeat("0", 64))
	if code, _, stderr := runCLI(t, env, "backup", "create", "-o", "bad.thrbackup"); code != 3 || !strings.Contains(stderr, "SHA-256") {
		t.Errorf("corrupt backup: exit %d, %q", code, stderr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); name != "b.thrbackup" && !strings.HasPrefix(name, "throne-backup-") {
			t.Errorf("left behind: %s", name)
		}
	}
}

// serveRestore makes f take uploads of fakeArchive, which has these parts, as upload 2.
// With late, Throne's wait ends while it checks the archive; refuse, unless "", is why the
// check refuses the archive.
func serveRestore(f *fakeThrone, parts []string, late bool, refuse string) {
	var size int
	var received []byte
	status := func() map[string]any {
		st := map[string]any{"id": 2, "size": size, "received": len(received), "state": "uploading"}
		switch {
		case len(received) < size || !bytes.Equal(received, fakeArchive):
		case refuse != "":
			st["state"], st["error"] = "failed", refuse
		default:
			st["state"] = "complete"
			st["archive"] = map[string]any{"created_at": "Thu Oct 16 15:04:05 2026", "platform": "linux", "parts": parts}
		}
		return st
	}
	f.on("POST /v1/restores", func(r *testserver.Request) *testserver.Response {
		size, received = int(r.Fields()["size"].(float64)), nil
		return testserver.OK(map[string]any{"id": 2, "chunk_limit": 524288})
	})
	f.on("PUT /v1/restores/2", func(r *testserver.Request) *testserver.Response {
		var chunk struct {
			Data []byte `json:"data"`
		}
		json.Unmarshal(r.Body, &chunk)
		received = append(received, chunk.Data...)
		st := status()
		switch {
		case st["state"] == "uploading":
			delete(st, "state")
			return testserver.OK(st)
		case late:
			return testserver.Accepted(map[string]any{"id": 2, "state": "inspecting"})
		case refuse != "":
			return testserver.Error(422, "invalid_archive", refuse)
		}
		delete(st, "state")
		return testserver.OK(st)
	})
	f.on("GET /v1/restores/2", func(*testserver.Request) *testserver.Response { return testserver.OK(status()) })
	f.on("POST /v1/restores/2/apply", func(r *testserver.Request) *testserver.Response {
		return testserver.OK(map[string]any{"restored": r.Fields()["parts"], "skipped_rules": 2, "restarting": true})
	})
	f.on("DELETE /v1/restores/2", ok(`{"deleted":2}`))
}

func TestBackupRestore(t *testing.T) {
	f, env := startFake(t)
	parts := []string{"profiles", "routes", "settings"}
	serveRestore(f, parts, false, "")
	dir := t.TempDir()
	file := filepath.Join(dir, "b.thrbackup")
	if err := os.WriteFile(file, fakeArchive, 0o600); err != nil {
		t.Fatal(err)
	}
	restore := func(input string, args ...string) (int, string, string, []*testserver.Request) {
		t.Helper()
		before := len(f.requests())
		code, stdout, stderr := runInput(t, env, input, append([]string{"backup", "restore", file, "--timeout", "0"}, args...)...)
		return code, stdout, stderr, f.requests()[before:]
	}
	uploaded := []string{"POST /v1/restores", "PUT /v1/restores/2"}
	applied := append(slices.Clone(uploaded), "POST /v1/restores/2/apply")
	discarded := append(slices.Clone(uploaded), "DELETE /v1/restores/2")

	code, stdout, stderr, reqs := restore("", "--yes")
	if code != 0 || stdout != "Restored profiles, routes. Throne restarts now.\n"+
		"2 routing rules of the archive could not be read and were left out.\n" {
		t.Fatalf("restore --yes: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if routes := routesOf(reqs); !reflect.DeepEqual(routes, applied) {
		t.Fatalf("requests %v, want %v", routes, applied)
	}
	sum := sha256.Sum256(fakeArchive)
	if body := reqs[0].Fields(); body["sha256"] != hex.EncodeToString(sum[:]) || body["size"] != float64(len(fakeArchive)) {
		t.Errorf("upload opened with %v", body)
	}
	if body := reqs[2].Fields(); !reflect.DeepEqual(body, map[string]any{"parts": []any{"profiles", "routes"}}) {
		t.Errorf("apply body %v", body)
	}

	code, _, stderr, reqs = restore("", "--yes", "--parts", "settings,ip-lists")
	if code != 2 || !strings.Contains(stderr, "the archive has no ip_lists") || !reflect.DeepEqual(routesOf(reqs), discarded) {
		t.Errorf("missing part: exit %d, %q, requests %v", code, stderr, routesOf(reqs))
	}

	// Throne's wait ends during the check: thronectl follows the upload until it is checked.
	checked := append(slices.Clone(uploaded), "GET /v1/restores/2")
	serveRestore(f, parts, true, "")
	code, stdout, stderr, reqs = restore("", "--yes")
	if code != 0 || !strings.HasPrefix(stdout, "Restored profiles, routes.") ||
		!reflect.DeepEqual(routesOf(reqs), append(slices.Clone(checked), "POST /v1/restores/2/apply")) {
		t.Errorf("checked after the wait: exit %d, %q, %q, requests %v", code, stdout, stderr, routesOf(reqs))
	}
	refusal := "The archive holds nothing Throne can restore"
	serveRestore(f, parts, true, refusal)
	code, _, stderr, reqs = restore("", "--yes")
	if code != 1 || stderr != "error: invalid_archive: "+refusal+"\n" ||
		!reflect.DeepEqual(routesOf(reqs), append(slices.Clone(checked), "DELETE /v1/restores/2")) {
		t.Errorf("refused after the wait: exit %d, %q, requests %v", code, stderr, routesOf(reqs))
	}
	serveRestore(f, parts, false, refusal)
	code, _, stderr, reqs = restore("", "--yes")
	if code != 1 || !strings.Contains(stderr, "invalid_archive") || !reflect.DeepEqual(routesOf(reqs), discarded) {
		t.Errorf("refused: exit %d, %q, requests %v", code, stderr, routesOf(reqs))
	}
	serveRestore(f, parts, false, "")

	asTerminal(t)
	code, _, stderr, reqs = restore("n\n")
	for _, want := range []string{"Holds:", "profiles, routes, settings", "on linux", "Restores:", "Restore? [y/N]", "not confirmed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("declined restore: stderr lacks %q:\n%s", want, stderr)
		}
	}
	if code != 1 || !reflect.DeepEqual(routesOf(reqs), discarded) {
		t.Errorf("declined restore: exit %d, requests %v", code, routesOf(reqs))
	}
	code, _, _, reqs = restore("yes\n", "--parts", "settings")
	if code != 0 || !reflect.DeepEqual(routesOf(reqs), applied) ||
		!reflect.DeepEqual(reqs[2].Fields(), map[string]any{"parts": []any{"settings"}}) {
		t.Errorf("confirmed restore: exit %d, requests %v", code, routesOf(reqs))
	}

	other := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(other, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := len(f.requests())
	if code, _, stderr := runCLI(t, env, "backup", "restore", other, "--yes"); code != 3 || !strings.Contains(stderr, "not a Throne backup") {
		t.Errorf("not an archive: exit %d, %q", code, stderr)
	}
	if n := len(f.requests()) - before; n != 0 {
		t.Errorf("%d requests for a file that is not an archive", n)
	}
}

func TestRuleSets(t *testing.T) {
	f, env := startFake(t)
	steps := []struct {
		resp   *testserver.Response
		args   []string
		code   int
		stdout string
		body   map[string]any
	}{
		{testserver.OK(json.RawMessage(`{"updated":3,"failures":[]}`)), nil, 0, "Updated 3 rule-sets.\n", nil},
		{testserver.OK(json.RawMessage(`{"updated":2,"failures":[{"tag":"geosite-x","error":"HTTP 404"}]}`)), nil, 1,
			"Updated 2 rule-sets.\n\nTAG        ERROR\ngeosite-x  HTTP 404\n", nil},
		{testserver.Accepted(map[string]any{"state": "updating"}), []string{"--no-wait"}, 0,
			"The update is still running in Throne.\n", map[string]any{"wait": false}},
	}
	for _, s := range steps {
		f.on("POST /v1/rule-sets/update", func(*testserver.Request) *testserver.Response { return s.resp })
		code, stdout, stderr := runCLI(t, env, append([]string{"rulesets", "update", "--timeout", "0"}, s.args...)...)
		if code != s.code || stdout != s.stdout {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", s.args, code, stdout, stderr)
		}
		if got := f.last(t).Fields(); !reflect.DeepEqual(got, s.body) {
			t.Errorf("%q: body %v, want %v", s.args, got, s.body)
		}
	}
}

func TestGeo(t *testing.T) {
	f, env := startFake(t)
	f.on("GET /v1/geo-assets", ok(`{"files":[`+
		`{"name":"geoip.dat","url":"https://x.example/geoip.dat","present":true,"size":2048,"updated_at":1760000000},`+
		`{"name":"geosite.dat","url":"https://x.example/geosite.dat","present":false,"size":0,"updated_at":0}]}`))
	f.on("POST /v1/geo-assets/update", func(r *testserver.Request) *testserver.Response {
		if r.Fields()["wait"] == false {
			return testserver.Accepted(map[string]any{"state": "downloading"})
		}
		return testserver.OK(json.RawMessage(`{"files":[{"name":"geoip.dat","ok":true,"skipped":true},` +
			`{"name":"geosite.dat","ok":false,"error":"HTTP 404"}]}`))
	})
	for _, args := range [][]string{{"geo"}, {"geo", "status"}} {
		code, stdout, stderr := runCLI(t, env, args...)
		if code != 0 || !strings.Contains(stdout, "2.00 KiB") || !strings.Contains(stdout, "https://x.example/geosite.dat") {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	code, stdout, stderr := runCLI(t, env, "geo", "update", "geosite", "geoip", "geosite", "--only-missing", "--timeout", "0")
	if code != 1 || stdout != "NAME         RESULT\ngeoip.dat    present, skipped\ngeosite.dat  failed: HTTP 404\n" ||
		stderr != "error: 1 of 2 files failed to update\n" {
		t.Errorf("geo update: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if got := f.last(t).Fields(); !reflect.DeepEqual(got, map[string]any{"files": []any{"geosite", "geoip"}, "only_missing": true}) {
		t.Errorf("geo update body %v", got)
	}
	if code, stdout, _ := runCLI(t, env, "geo", "update", "--no-wait"); code != 0 || !strings.Contains(stdout, "still running") {
		t.Errorf("geo update --no-wait: exit %d, %q", code, stdout)
	}
	if got := f.last(t).Fields(); !reflect.DeepEqual(got, map[string]any{"wait": false}) {
		t.Errorf("geo update --no-wait body %v", got)
	}
}

func TestRestartAndShutdown(t *testing.T) {
	f, env := startFake(t)
	f.on("POST /v1/restart", func(*testserver.Request) *testserver.Response {
		return testserver.Accepted(map[string]any{"restarting": true, "resume": true})
	})
	f.on("POST /v1/shutdown", func(*testserver.Request) *testserver.Response {
		return testserver.Accepted(map[string]any{"stopping": true})
	})
	if code, stdout, _ := runCLI(t, env, "restart"); code != 0 || stdout != "Throne is restarting; it starts the running profile again.\n" {
		t.Errorf("restart: exit %d, %q", code, stdout)
	}
	if code, stdout, _ := runCLI(t, env, "shutdown", "--yes"); code != 0 || stdout != "Throne is exiting.\n" ||
		f.last(t).Path != "/v1/shutdown" {
		t.Errorf("shutdown --yes: exit %d, %q", code, stdout)
	}
	if code, stdout, _ := runCLI(t, env, "--json", "shutdown", "--yes"); code != 0 || stdout != "{\n  \"stopping\": true\n}\n" {
		t.Errorf("shutdown --json: exit %d, %q", code, stdout)
	}

	asTerminal(t)
	before := len(f.requests())
	if code, _, stderr := runInput(t, env, "\n", "shutdown"); code != 1 || !strings.Contains(stderr, "Shut Throne down? [y/N]") {
		t.Errorf("declined shutdown: exit %d, %q", code, stderr)
	}
	if n := len(f.requests()) - before; n != 0 {
		t.Errorf("%d requests for a declined shutdown", n)
	}
	if code, _, _ := runInput(t, env, "y\n", "shutdown"); code != 0 || len(f.requests()) != before+1 {
		t.Errorf("confirmed shutdown: exit %d", code)
	}
}

func TestKeygen(t *testing.T) {
	code, stdout, _ := runCLI(t, nil, "keygen")
	if _, err := throne.ParseKey(stdout); code != 0 || err != nil || len(stdout) != 44 {
		t.Fatalf("keygen: exit %d, %q, %v", code, stdout, err)
	}
	if _, again, _ := runCLI(t, nil, "keygen"); again == stdout {
		t.Error("keygen made the same key twice")
	}

	file := filepath.Join(t.TempDir(), "key")
	if code, stdout, stderr := runCLI(t, nil, "keygen", "-o", file); code != 0 || stdout != "Wrote a new key to "+file+".\n" {
		t.Fatalf("keygen -o: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := throne.ReadKeyFile(file); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(file); runtime.GOOS != "windows" && (err != nil || fi.Mode().Perm() != 0o600) {
		t.Errorf("key file mode %v, %v", fi.Mode(), err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := runCLI(t, nil, "keygen", "-o", file); code != 3 || !strings.Contains(stderr, "exists") {
		t.Errorf("keygen -o over a file: exit %d, %q", code, stderr)
	}
	if again, _ := os.ReadFile(file); !bytes.Equal(again, first) {
		t.Error("keygen -o overwrote a key")
	}
}
