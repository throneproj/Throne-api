package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
	mu    sync.Mutex
	state string
	tun   bool
	reqs  []*testserver.Request
}

func (f *fakeThrone) handle(_ context.Context, r *testserver.Request) *testserver.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
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
		"added_later": "kept"}
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
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut, func(k string) string { return env[k] })
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
	code, stdout, _ = runCLI(t, env, "--json", "status")
	var body map[string]any
	if code != 0 || json.Unmarshal([]byte(stdout), &body) != nil || body["added_later"] != "kept" {
		t.Fatalf("status --json: exit %d, %q", code, stdout)
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
