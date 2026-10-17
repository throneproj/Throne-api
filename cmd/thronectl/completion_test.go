package main

import (
	"context"
	"errors"
	"flag"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/testserver"
)

func TestMain(m *testing.M) {
	// The completion scripts run a copy of the test binary as thronectl.
	if os.Getenv("THRONECTL_TEST_MAIN") == "1" {
		main()
	}
	os.Exit(m.Run())
}

// suggest runs the hidden completion command and returns the candidates, as texts and as
// the lines with their descriptions, and the directive line.
func suggest(t *testing.T, env map[string]string, words ...string) (texts, lines []string, directive string) {
	t.Helper()
	code, stdout, stderr := runCLI(t, env, append([]string{completeCommand}, words...)...)
	if code != 0 || stderr != "" || !strings.HasSuffix(stdout, "\n") {
		t.Fatalf("complete %q: exit %d, stdout %q, stderr %q", words, code, stdout, stderr)
	}
	all := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	directive = all[len(all)-1]
	if !regexp.MustCompile(`^:\d+$`).MatchString(directive) {
		t.Fatalf("complete %q: last line %q", words, directive)
	}
	for _, l := range all[:len(all)-1] {
		text, _, _ := strings.Cut(l, "\t")
		texts, lines = append(texts, text), append(lines, l)
	}
	return texts, lines, directive
}

func TestComplete(t *testing.T) {
	tests := []struct {
		words     []string
		want      []string
		directive string
	}{
		{[]string{"con"}, []string{"connect"}, ":0"},
		{[]string{"--json", "co"}, []string{"connect", "completion"}, ":0"},
		{[]string{"--addr", "127.0.0.1:1", "--timeout=5s", "v"}, []string{"version"}, ":0"},
		{[]string{"--"}, []string{"--addr", "--json", "--key-file", "--timeout"}, ":0"},
		{[]string{"--json", "--"}, []string{"--addr", "--key-file", "--timeout"}, ":0"},
		{[]string{"--timeout", ""}, nil, ":0"},
		{[]string{"--key-file", ""}, nil, ":2"},
		{[]string{"--key-file=~/k"}, nil, ":2"},
		{[]string{"nope", ""}, nil, ":0"},
		{[]string{"help", "tu"}, []string{"tun"}, ":0"},
		{[]string{"help", "tun", ""}, nil, ":0"},
		{[]string{"help", "--json", "tu"}, nil, ":0"},
		{[]string{"help", "--", "t"}, nil, ":0"},
		{[]string{"help", "-"}, nil, ":0"},
		{[]string{"tun", ""}, []string{"on", "off", "toggle"}, ":0"},
		{[]string{"proxy", "on", ""}, nil, ":0"},
		{[]string{"connect", ""}, []string{"last", "best"}, ":0"},
		{[]string{"connect", "--max-age", ""}, nil, ":0"},
		{[]string{"connect", "--max-age", "1h", "--re"}, []string{"--restart"}, ":0"},
		{[]string{"connect", "--restart", "b"}, []string{"best"}, ":0"},
		{[]string{"connect", "--group", "3", ""}, []string{"best"}, ":0"},
		{[]string{"connect", "--max-age=1h", "--restart", "l"}, nil, ":0"},
		{[]string{"connect", "last", "--"}, []string{"--addr", "--json", "--key-file", "--no-wait", "--restart", "--timeout"}, ":0"},
		{[]string{"connect", "5", "--g"}, nil, ":0"},
		{[]string{"connect", "best", "--g"}, []string{"--group"}, ":0"},
		{[]string{"test", "latency", "0", "--"}, []string{"--addr", "--json", "--key-file", "--no-wait", "--timeout"}, ":0"},
		{[]string{"test", "latency", "--name", "x", "--g"}, []string{"--group"}, ":0"},
		{[]string{"clear", "1,2", "--n"}, nil, ":0"},
		{[]string{"profiles", "--ids", "0", "--i"}, []string{"--ids"}, ":0"},
		{[]string{"test", ""}, []string{"latency", "ip", "speed"}, ":0"},
		{[]string{"test", "--no-wait", "i"}, []string{"ip"}, ":0"},
		{[]string{"traffic", "--by", ""}, []string{"profile", "app"}, ":0"},
		{[]string{"traffic", "--by=p"}, []string{"--by=profile"}, ":0"},
		{[]string{"traffic", "--since", "7d", "--by", "a"}, []string{"app"}, ":0"},
		{[]string{"import", ""}, []string{"-"}, ":2"},
		{[]string{"import", "-"}, []string{"--addr", "--group", "--json", "--key-file", "--no-wait", "--timeout", "--url", "-"}, ":2"},
		{[]string{"import", "links.txt", ""}, nil, ":0"},
		{[]string{"import", "--url", "http://x", ""}, nil, ":0"},
		{[]string{"import", "--url=http://x", "-"}, []string{"--addr", "--group", "--json", "--key-file", "--no-wait", "--timeout"}, ":0"},
		{[]string{"import", "-", "--u"}, nil, ":0"},
		{[]string{"sub", ""}, []string{"add", "update"}, ":0"},
		{[]string{"sub", "add", ""}, nil, ":0"},
		{[]string{"sub", "--all", ""}, []string{"update"}, ":0"},
		{[]string{"sub", "--name", "x", "--no-wait", ""}, []string{"add"}, ":0"},
		{[]string{"sub", "add", "--"}, []string{"--addr", "--json", "--key-file", "--name", "--no-auto-update", "--no-update",
			"--no-wait", "--timeout"}, ":0"},
		{[]string{"sub", "update", "--n"}, []string{"--no-wait"}, ":0"},
		{[]string{"sub", "update", "--all", ""}, nil, ":0"},
		{[]string{"sub", "update", "3", "--a"}, []string{"--addr"}, ":0"},
		{[]string{"group", "add", "x", "--n"}, nil, ":0"},
		{[]string{"group", "rm", "--n"}, []string{"--no-wait"}, ":0"},
		{[]string{"group", "--no-wait", ""}, []string{"rm"}, ":0"},
		{[]string{"group", "r"}, []string{"rm"}, ":0"},
		{[]string{"profile", ""}, []string{"rm"}, ":0"},
		{[]string{"profile", "--group", "3", ""}, []string{"rm"}, ":0"},
		{[]string{"settings", "se"}, []string{"set"}, ":0"},
		{[]string{"settings", "s"}, []string{"set", "singbox_api_port", "sub_auto_update", "sub_respect_server_interval"}, ":0"},
		{[]string{"settings", "tun_m"}, []string{"tun_mtu"}, ":0"},
		{[]string{"settings", "get", "log_level", "log"}, nil, ":0"},
		{[]string{"settings", "set", "tun_"}, []string{"tun_auto_redirect=", "tun_ipv4_cidr=", "tun_ipv6=", "tun_ipv6_cidr=", "tun_mtu=", "tun_strict_route="}, ":1"},
		{[]string{"settings", "set", "tun_mtu=1400", "tun_m"}, nil, ":1"},
		{[]string{"settings", "set", "tun_ipv6="}, []string{"tun_ipv6=true", "tun_ipv6=false"}, ":0"},
		{[]string{"settings", "set", "tun_ipv6=f"}, []string{"tun_ipv6=false"}, ":0"},
		{[]string{"settings", "set", "tun_mtu="}, nil, ":0"},
		{[]string{"backup", ""}, []string{"create", "restore"}, ":0"},
		{[]string{"backup", "restore", ""}, nil, ":2"},
		{[]string{"backup", "restore", "a.thrbackup", ""}, nil, ":0"},
		{[]string{"backup", "create", "--"}, []string{"--addr", "--json", "--key-file", "--parts", "--timeout"}, ":0"},
		{[]string{"backup", "restore", "a.thrbackup", "-"}, []string{"--addr", "--json", "--key-file", "--parts", "--timeout", "--yes"}, ":0"},
		{[]string{"backup", "--yes", ""}, []string{"restore"}, ":0"},
		{[]string{"backup", "-o", "f", ""}, []string{"create"}, ":0"},
		{[]string{"backup", "-o", "f", "--yes", ""}, nil, ":0"},
		{[]string{"backup", "create", "-o", ""}, nil, ":2"},
		{[]string{"backup", "create", "-o=b"}, nil, ":2"},
		{[]string{"backup", "create", "--parts", ""}, []string{"profiles", "routes", "settings", "otp", "ip_lists", "icons"}, ":0"},
		{[]string{"backup", "create", "--parts", "ip-lists,p"}, []string{"ip-lists,profiles"}, ":0"},
		{[]string{"backup", "create", "--parts", "ip_lists,ip"}, nil, ":0"},
		{[]string{"backup", "restore", "--parts=routes,ip-"}, []string{"--parts=routes,ip-lists"}, ":0"},
		{[]string{"rulesets", ""}, []string{"update"}, ":0"},
		{[]string{"geo", ""}, []string{"status", "update"}, ":0"},
		{[]string{"geo", "update", ""}, []string{"geoip", "geosite"}, ":0"},
		{[]string{"geo", "update", "geosite", ""}, []string{"geoip"}, ":0"},
		{[]string{"geo", "status", "--"}, []string{"--addr", "--json", "--key-file", "--timeout"}, ":0"},
		{[]string{"geo", "--only-missing", ""}, []string{"update"}, ":0"},
		{[]string{"completion", ""}, []string{"bash", "zsh", "fish", "powershell"}, ":0"},
		{[]string{"completion", "bash", ""}, nil, ":0"},
		{[]string{"call", ""}, []string{"GET", "POST", "PUT", "DELETE"}, ":0"},
		{[]string{"call", "g"}, []string{"get"}, ":0"},
		{[]string{"call", "P"}, []string{"POST", "PUT"}, ":0"},
		{[]string{"call", "--body", "{}", "d"}, []string{"delete"}, ":0"},
		{[]string{"call", "DELETE", ""}, callPathsFor(runtime.GOOS, "/v1/profiles", "/v1/connection", "/v1/results"), ":0"},
		{[]string{"call", "delete", "/"}, []string{"/v1/profiles", "/v1/connection", "/v1/results"}, ":0"},
		{[]string{"call", "put", "/v1/"}, []string{"/v1/connection", "/v1/modes", "/v1/routes/active", "/v1/settings"}, ":0"},
		{[]string{"call", "GET", "v1/stats"}, []string{"v1/stats/live", "v1/stats/traffic"}, ":0"},
		{[]string{"call", "GET", "/v1/status", ""}, nil, ":0"},
		{[]string{"keygen", "-o", ""}, nil, ":2"},
		{[]string{"status", "--", "-"}, nil, ":0"},
		{[]string{"version", ""}, nil, ":0"},
	}
	env := map[string]string{"THRONE_ADDR": "127.0.0.1:1"}
	for _, tt := range tests {
		texts, _, directive := suggest(t, env, tt.words...)
		if !reflect.DeepEqual(texts, tt.want) || directive != tt.directive {
			t.Errorf("complete %q: %q %s, want %q %s", tt.words, texts, directive, tt.want, tt.directive)
		}
	}

	_, lines, _ := suggest(t, env, "conn")
	if want := []string{"connect\tStart a profile: by ID, the last one, or the one with the lowest latency"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("command description: %q", lines)
	}
	if _, lines, _ := suggest(t, env, "geo", "--only"); !reflect.DeepEqual(lines, []string{"--only-missing\tupdate: only the files that are missing"}) {
		t.Errorf("flag description: %q", lines)
	}
	if _, lines, _ := suggest(t, env, "backup", "create", "--parts", "o"); !reflect.DeepEqual(lines, []string{"otp\tOTP accounts, secrets included"}) {
		t.Errorf("part description: %q", lines)
	}
	for _, words := range [][]string{nil, {}, {"-"}, {"---"}, {"--timeout"}, {"\x00", ""}, {"connect", "--group=", "--group"}} {
		if texts, _, _ := suggest(t, env, words...); words == nil && len(texts) != len(commands)+1 {
			t.Errorf("complete without words: %q", texts)
		}
	}
}

// callPathsFor returns paths as completion offers them for an empty word on goos: without
// the leading slash on Windows.
func callPathsFor(goos string, paths ...string) []string {
	if goos == "windows" {
		for i, p := range paths {
			paths[i] = p[1:]
		}
	}
	return paths
}

func TestCompleteFromThrone(t *testing.T) {
	f, env := startFake(t)
	f.on("GET /v1/groups", ok(`{"groups":[{"id":0,"name":"Default"},{"id":3,"name":"Provider","subscription":true}]}`))
	f.on("GET /v1/routes", ok(`{"routes":[{"id":0,"name":"Default"},{"id":2,"name":"Bypass","active":true}]}`))
	f.on("GET /v1/tests", ok(`{"jobs":[{"id":7,"kind":"speed","state":"running"},{"id":8,"kind":"latency","state":"done"}]}`))
	f.on("GET /v1/settings", ok(`{"settings":{"log_level":"info","tun_mtu":9000,"kill_switch":false,"added_later":true}}`))

	last, best := "last\tthe profile that ran last", "best\tthe profile with the lowest latency"
	tests := []struct {
		words     []string
		want      []string
		directive string
	}{
		{[]string{"connect", ""}, []string{last, best, "0\tDE Frankfurt", "1\tNL [2J", "2\tFI"}, ":0"},
		{[]string{"connect", "1"}, []string{"1\tNL [2J"}, ":0"},
		{[]string{"test", "latency", "0", ""}, []string{"1\tNL [2J", "2\tFI"}, ":0"},
		{[]string{"clear", "2", ""}, []string{"0\tDE Frankfurt", "1\tNL [2J"}, ":0"},
		{[]string{"profile", "rm", ""}, []string{"0\tDE Frankfurt", "1\tNL [2J", "2\tFI"}, ":0"},
		{[]string{"profiles", "--ids", "0,"}, []string{"0,1\tNL [2J", "0,2\tFI"}, ":0"},
		{[]string{"sub", "update", ""}, []string{"3\tProvider"}, ":0"},
		{[]string{"group", "rm", ""}, []string{"0\tDefault", "3\tProvider"}, ":0"},
		{[]string{"import", "--group", ""}, []string{"0\tDefault", "3\tProvider"}, ":0"},
		{[]string{"profile", "rm", "--group=3"}, []string{"--group=3\tProvider"}, ":0"},
		{[]string{"route", ""}, []string{"0\tDefault", "2\tBypass (active)"}, ":0"},
		{[]string{"cancel", ""}, []string{"7\tspeed test"}, ":0"},
		{[]string{"job", ""}, []string{"7\tspeed test, running", "8\tlatency test, done"}, ":0"},
		{[]string{"clear", "0,"}, []string{"0,1\tNL [2J", "0,2\tFI"}, ":0"},
		{[]string{"test", "ip", "0,"}, []string{"0,1\tNL [2J", "0,2\tFI"}, ":0"},
		{[]string{"profile", "rm", "0,1,"}, []string{"0,1,2\tFI"}, ":0"},
		{[]string{"clear", "1,2", ""}, []string{"0\tDE Frankfurt"}, ":0"},
		{[]string{"sub", "update", "3,"}, nil, ":0"},
		{[]string{"test", "latency", "--group", "3", ""}, nil, ":0"},
		{[]string{"test", "latency", "--name=x", ""}, nil, ":0"},
		{[]string{"clear", "--group", "3", ""}, nil, ":0"},
		{[]string{"profile", "rm", "--group=3", ""}, nil, ":0"},
		{[]string{"connect", "--group", "3", ""}, []string{best}, ":0"},
		{[]string{"settings", "get", ""}, []string{"added_later\ttrue", "kill_switch\tfalse", "log_level\tinfo", "tun_mtu\t9000"}, ":0"},
		{[]string{"settings", "set", "kill_switch=false", ""}, []string{"added_later=\ttrue", "log_level=\tinfo", "tun_mtu=\t9000"}, ":1"},
		{[]string{"settings", "set", "added_later="}, []string{"added_later=true", "added_later=false"}, ":0"},
		{[]string{"settings", "set", "remember_enable="}, nil, ":0"},
	}
	for _, tt := range tests {
		_, lines, directive := suggest(t, env, tt.words...)
		if !reflect.DeepEqual(lines, tt.want) || directive != tt.directive {
			t.Errorf("complete %q: %q %s, want %q %s", tt.words, lines, directive, tt.want, tt.directive)
		}
	}

	before := len(f.requests())
	if texts, _, _ := suggest(t, env, "connect", "b"); !reflect.DeepEqual(texts, []string{"best"}) || len(f.requests()) != before {
		t.Errorf("connect b: %q, %d requests", texts, len(f.requests())-before)
	}

	// The address and the key file may come from the command line, before or after the command.
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(keyFile)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, words := range [][]string{
		{"--addr", env["THRONE_ADDR"], "route", "--key-file", keyFile, ""},
		{"--key-file=~/key", "route", "--addr=" + env["THRONE_ADDR"], ""},
	} {
		if texts, _, _ := suggest(t, nil, words...); !reflect.DeepEqual(texts, []string{"0", "2"}) {
			t.Errorf("complete %q: %q", words, texts)
		}
	}
}

func TestCompleteWithoutThrone(t *testing.T) {
	f, env := startFake(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	for _, e := range []map[string]string{
		{"THRONE_ADDR": env["THRONE_ADDR"]},
		{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY": "not a key"},
		{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY_FILE": filepath.Join(t.TempDir(), "missing")},
		{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY": otherKey},
		{"THRONE_ADDR": closed, "THRONE_KEY": testKey},
	} {
		start := time.Now()
		if texts, _, directive := suggest(t, e, "connect", ""); !reflect.DeepEqual(texts, []string{"last", "best"}) || directive != ":0" {
			t.Errorf("env %v: %q %s", e, texts, directive)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("env %v: took %v", e, d)
		}
		if texts, _, _ := suggest(t, e, "settings", "set", "tun_m"); !reflect.DeepEqual(texts, []string{"tun_mtu="}) {
			t.Errorf("env %v: settings from the spec: %q", e, texts)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests reached Throne without the right key", n)
	}

	f.on("GET /v1/profiles", func(*testserver.Request) *testserver.Response {
		return testserver.Error(500, "internal", "broken")
	})
	if texts, _, _ := suggest(t, env, "clear", ""); texts != nil {
		t.Errorf("profiles failed: %q", texts)
	}

	// Throne that does not answer within the deadline.
	defer func(d time.Duration) { completionWait = d }(completionWait)
	completionWait = 200 * time.Millisecond
	f.on("GET /v1/profiles", func(r *testserver.Request) *testserver.Response {
		time.Sleep(time.Second)
		return testserver.OK(map[string]any{"profiles": []any{}})
	})
	start := time.Now()
	if texts, _, _ := suggest(t, env, "clear", ""); texts != nil || time.Since(start) > 900*time.Millisecond {
		t.Errorf("slow Throne: %q after %v", texts, time.Since(start))
	}

	// A key file that does not open in time, as on a share that is gone.
	release := make(chan struct{})
	defer close(release)
	defer func(f func(*cli) (throne.Key, error)) { completionKey = f }(completionKey)
	completionKey = func(*cli) (throne.Key, error) {
		<-release
		return throne.Key{}, errors.New("too late")
	}
	start = time.Now()
	if texts, _, _ := suggest(t, env, "connect", ""); !reflect.DeepEqual(texts, []string{"last", "best"}) || time.Since(start) > 900*time.Millisecond {
		t.Errorf("slow key file: %q after %v", texts, time.Since(start))
	}
}

// TestCompleteKeyFileNotRegular checks that completion reads no key from a directory, a
// pipe or a device, which may block: /dev/stdin is the terminal the shell completes on.
func TestCompleteKeyFileNotRegular(t *testing.T) {
	f, env := startFake(t)
	names := []string{t.TempDir()}
	if runtime.GOOS != "windows" {
		fifo := filepath.Join(t.TempDir(), "fifo")
		if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
			t.Logf("no FIFO: %v %s", err, out)
		} else {
			names = append(names, fifo)
		}
	}
	defer func(d time.Duration) { completionWait = d }(completionWait)
	completionWait = 10 * time.Second
	for _, name := range names {
		start := time.Now()
		e := map[string]string{"THRONE_ADDR": env["THRONE_ADDR"], "THRONE_KEY_FILE": name}
		if texts, _, _ := suggest(t, e, "connect", ""); !reflect.DeepEqual(texts, []string{"last", "best"}) || time.Since(start) > 5*time.Second {
			t.Errorf("key file %s: %q after %v", name, texts, time.Since(start))
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests reached Throne without a key", n)
	}
}

// flagLine is a flag in the output of flag.PrintDefaults, with the name of its value
// when it takes one.
var flagLine = regexp.MustCompile(`^  -(\S+)(?: (\S+))?(?:\t.*)?$`)

// helpFlags returns the flags that help shows, by name, true for those that take a value.
func helpFlags(t *testing.T, args ...string) map[string]bool {
	t.Helper()
	code, stdout, _ := runCLI(t, nil, args...)
	if code != 0 {
		t.Fatalf("%q: exit %d", args, code)
	}
	flags := map[string]bool{}
	for line := range strings.Lines(stdout) {
		if m := flagLine.FindStringSubmatch(strings.TrimRight(line, "\n")); m != nil {
			flags[m[1]] = m[2] != ""
		}
	}
	return flags
}

func TestCompletionCoversCommands(t *testing.T) {
	texts, _, _ := suggest(t, nil, "")
	for _, cmd := range commands {
		if !slices.Contains(texts, cmd.name) {
			t.Errorf("completion does not offer the command %s", cmd.name)
		}
		if _, ok := argValues[cmd.name]; !ok {
			t.Errorf("argValues lacks the command %s", cmd.name)
		}
	}
	for name := range argValues {
		if findCommand(name) == nil {
			t.Errorf("argValues has %s, which is no command", name)
		}
	}
	if !slices.Contains(texts, "help") || slices.Contains(texts, completeCommand) {
		t.Errorf("commands offered: %q", texts)
	}
}

func TestCompletionCoversFlags(t *testing.T) {
	global := helpFlags(t, "help")
	if len(global) != 4 {
		t.Fatalf("global flags %v", global)
	}
	known := map[string]bool{}
	for _, cmd := range commands {
		flags := helpFlags(t, "help", cmd.name)
		for name, v := range global {
			flags[name] = v
		}
		texts, _, _ := suggest(t, nil, cmd.name, "-")
		for name, takesValue := range flags {
			known[name] = true
			if !slices.Contains(texts, flagText(name)) {
				t.Errorf("thronectl %s: completion does not offer %s", cmd.name, flagText(name))
			}
			if _, ok := flagValues[name]; takesValue && !ok {
				t.Errorf("thronectl %s: flagValues lacks %s, which takes a value", cmd.name, flagText(name))
			}
			// The word after a flag that takes a value is no argument.
			after, _, _ := suggest(t, nil, cmd.name, flagText(name), "")
			positional, _, _ := suggest(t, nil, cmd.name, "")
			if takesValue && positional != nil && reflect.DeepEqual(after, positional) {
				t.Errorf("thronectl %s %s: completes an argument, not the value", cmd.name, flagText(name))
			}
		}
	}
	for name := range flagValues {
		if !known[name] {
			t.Errorf("flagValues has %s, which no command takes", name)
		}
	}
}

// TestCompletionFlagsLeftOut checks the flags that completion leaves out against what the
// commands refuse: a flag whose usage starts with a keyword, such as "add: ...", after
// another keyword, and the flags of alternatives with the arguments they stand for.
func TestCompletionFlagsLeftOut(t *testing.T) {
	env := map[string]string{"THRONE_ADDR": "127.0.0.1:1"}
	refused := func(name string, args ...string) bool {
		code, _, stderr := runCLI(t, env, args...)
		msg, _, _ := strings.Cut(stderr, "\n")
		return code == 2 && strings.Contains(msg, flagText(name))
	}
	withValue := func(f *flag.Flag) []string {
		switch {
		case isBoolFlag(f):
			return []string{flagText(f.Name)}
		case reflect.TypeOf(f.Value) == reflect.TypeOf(&optDuration{}):
			return []string{flagText(f.Name), "1h"}
		}
		return []string{flagText(f.Name), "1"}
	}
	syntax := strings.NewReplacer("[", " ", "]", " ", "(", " ", ")", " ", "|", " ")
	for _, cmd := range commands {
		keywords, _, _ := suggest(t, nil, cmd.name, "")
		commandFlags(cmd).VisitAll(func(f *flag.Flag) {
			s := scope(f)
			if s == "" {
				return
			}
			if !slices.Contains(keywords, s) || !slices.Contains(strings.Fields(syntax.Replace(cmd.args)), s) {
				t.Errorf("thronectl %s %s: %q is no keyword of the command", cmd.name, flagText(f.Name), s)
			}
			for _, k := range keywords {
				if k != s && !refused(f.Name, append([]string{cmd.name, k}, withValue(f)...)...) &&
					!refused(f.Name, append([]string{cmd.name, k, "x"}, withValue(f)...)...) {
					t.Errorf("thronectl %s %s %s: completion leaves the flag out, but the command takes it", cmd.name, k, flagText(f.Name))
				}
			}
		})
	}
	before := map[string][]string{"test": {"latency"}, "profile": {"rm"}, "sub": {"update"}}
	for name, alt := range alternatives {
		cmd := findCommand(name)
		if cmd == nil || len(before[name]) != alt.from {
			t.Errorf("alternatives[%s]: no command, or not %d arguments before", name, alt.from)
			continue
		}
		fs := commandFlags(cmd)
		for _, fl := range alt.flags {
			f := fs.Lookup(fl)
			if f == nil {
				t.Errorf("alternatives[%s]: no flag %s", name, fl)
				continue
			}
			args := append(append([]string{name}, before[name]...), withValue(f)...)
			if !refused(fl, append(args, "0")...) {
				t.Errorf("thronectl %s: takes %s with an argument, but completion offers one or the other", name, flagText(fl))
			}
		}
	}
}

// TestCompletionCoversSynopsis checks that completion offers the words of each synopsis,
// such as connect's last and best, as an argument, the argument after a first one, or the
// value of a flag.
func TestCompletionCoversSynopsis(t *testing.T) {
	placeholder := regexp.MustCompile(`<[^>]*>`)
	keyword := regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	syntax := strings.NewReplacer("[", " ", "]", " ", "(", " ", ")", " ", "|", " ", "...", " ")
	for _, cmd := range commands {
		offered := map[string]bool{}
		first, _, _ := suggest(t, nil, cmd.name, "")
		for _, w := range first {
			offered[w] = true
			second, _, _ := suggest(t, nil, cmd.name, w, "")
			for _, s := range second {
				offered[s] = true
			}
		}
		for name, takesValue := range helpFlags(t, "help", cmd.name) {
			if takesValue {
				values, _, _ := suggest(t, nil, cmd.name, flagText(name), "")
				for _, v := range values {
					offered[v] = true
				}
			}
		}
		for _, word := range strings.Fields(syntax.Replace(placeholder.ReplaceAllString(cmd.args, " "))) {
			if keyword.MatchString(word) && !offered[word] {
				t.Errorf("thronectl %s: completion does not offer %q", cmd.name, word)
			}
		}
	}
}

func TestCompletionTablesMatchSpec(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	spec := strings.ReplaceAll(string(b), "\r\n", "\n")
	_, settings, _ := strings.Cut(spec, "\n## Settings\n")
	settings, _, _ = strings.Cut(settings, "\n## ")
	names := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\| ([a-z]+) \\|").FindAllStringSubmatch(settings, -1) {
		names[m[1]] = m[2] == "bool"
	}
	if !reflect.DeepEqual(names, settingNames) {
		t.Errorf("settingNames differ from spec/api.md: %v", names)
	}
	paths := map[string][]string{}
	for _, m := range regexp.MustCompile("(?m)^### `([A-Z]+) (/[^`{]*)`$").FindAllStringSubmatch(spec, -1) {
		paths[m[1]] = append(paths[m[1]], m[2])
	}
	for method := range paths {
		slices.Sort(paths[method])
	}
	for method, list := range apiPaths {
		if got := slices.Sorted(slices.Values(list)); !reflect.DeepEqual(got, paths[method]) {
			t.Errorf("apiPaths[%s] = %q, spec/api.md has %q", method, got, paths[method])
		}
	}
	if methods := slices.Sorted(maps.Keys(paths)); !reflect.DeepEqual(methods, slices.Sorted(maps.Keys(apiPaths))) {
		t.Errorf("spec/api.md has the methods %q", methods)
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, sh := range completionShells {
		code, stdout, stderr := runCLI(t, nil, "completion", sh.name)
		if code != 0 || stderr != "" || !strings.Contains(stdout, " "+completeCommand+" ") || strings.Contains(stdout, "\r") {
			t.Errorf("completion %s: exit %d, stderr %q, %d bytes", sh.name, code, stderr, len(stdout))
		}
	}
	code, stdout, _ := runCLI(t, nil, "help", "completion")
	for _, want := range []string{"Usage: thronectl completion bash | zsh | fish | powershell", `eval "$(thronectl completion bash)"`,
		"source <(thronectl completion zsh)", "_thronectl", "~/.config/fish/completions/thronectl.fish", "Out-String | Invoke-Expression"} {
		if code != 0 || !strings.Contains(stdout, want) {
			t.Errorf("help completion lacks %q:\n%s", want, stdout)
		}
	}
}

// shell returns the path of a shell that runs, or skips the test.
func shell(t *testing.T, name string, check ...string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("no %s: %v", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// On Windows, bash may be the stub of a Windows Subsystem for Linux that is not installed.
	if out, err := exec.CommandContext(ctx, path, check...).CombinedOutput(); err != nil {
		t.Skipf("%s does not run: %v %s", name, err, out)
	}
	return path
}

// bashPath returns the path of bash, or skips the test. On Windows it must be the bash of Git
// or MSYS2, not that of the Windows Subsystem for Linux, which cannot run thronectl.exe.
func bashPath(t *testing.T) string {
	t.Helper()
	path := shell(t, "bash", "-c", "true")
	if runtime.GOOS == "windows" {
		out, _ := exec.Command(path, "-c", "uname -s").Output()
		if !regexp.MustCompile(`MINGW|MSYS|CYGWIN`).Match(out) {
			t.Skipf("%s is not the bash of Git or MSYS2: %s", path, out)
		}
	}
	return path
}

// writeScript writes the completion script for a shell to a file in dir, with the
// extension the shell wants: PowerShell opens any other file as a document.
func writeScript(t *testing.T, dir, name string) string {
	t.Helper()
	i := slices.IndexFunc(completionShells, func(sh struct{ name, file string }) bool { return sh.name == name })
	_, script, _ := runCLI(t, nil, "completion", name)
	file := filepath.Join(dir, completionShells[i].file)
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestCompletionScriptSyntax(t *testing.T) {
	dir := t.TempDir()
	t.Run("bash", func(t *testing.T) {
		bash := bashPath(t)
		if out, err := exec.Command(bash, "-n", filepath.ToSlash(writeScript(t, dir, "bash"))).CombinedOutput(); err != nil {
			t.Errorf("bash -n: %v\n%s", err, out)
		}
	})
	check := filepath.Join(dir, "check.ps1")
	if err := os.WriteFile(check, []byte(`param($path)
$errors = $null
[void][System.Management.Automation.Language.Parser]::ParseInput([IO.File]::ReadAllText($path), [ref]$null, [ref]$errors)
if ($errors) { $errors | ForEach-Object { $_.ToString() }; exit 1 }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, dir, "powershell")
	for _, ps := range []string{"pwsh", "powershell"} {
		t.Run(ps, func(t *testing.T) {
			path := shell(t, ps, "-NoProfile", "-NonInteractive", "-Command", "exit 0")
			if out, err := exec.Command(path, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", check, script).CombinedOutput(); err != nil {
				t.Errorf("parse: %v\n%s", err, out)
			}
		})
	}
}

// scriptEnv returns the environment for a shell that runs a copy of the test binary as
// thronectl, with the Throne settings in env and no others.
func scriptEnv(t *testing.T, env map[string]string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	name := "thronectl"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), b, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !strings.EqualFold(k, "PATH") && !strings.HasPrefix(strings.ToUpper(k), "THRONE") {
			out = append(out, kv)
		}
	}
	out = append(out, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "THRONECTL_TEST_MAIN=1")
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// fileDir makes a directory with files for file name completion, and the key in a
// directory below it, sub dir/throne.key.
func fileDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"k ey.txt": "", "other": "", "x@y.txt": "", "sub dir/throne.key": testKey + "\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// readlineWord returns the end of line that readline completes, which bash passes to the
// completion function as $2: the text after an opening quote that is not closed, or else
// after the last word break character outside quotes.
func readlineWord(line string) string {
	start, open, quote := 0, 0, byte(0)
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quote == '\'':
			if c == quote {
				quote = 0
			}
		case c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote, open = c, i+1
		case strings.IndexByte(" \t\n@><=;|&(:", c) >= 0:
			start = i + 1
		}
	}
	if quote != 0 {
		return line[open:]
	}
	return line[start:]
}

// TestBashCompletion runs the bash script on the words bash makes of a line: bash 4 and
// later split them at = and :, bash 3.2 does not.
func TestBashCompletion(t *testing.T) {
	bash := bashPath(t)
	script := filepath.ToSlash(writeScript(t, t.TempDir(), "bash"))
	_, throne := startFake(t)
	env := scriptEnv(t, map[string]string{"THRONE_ADDR": throne["THRONE_ADDR"]})
	dir := fileDir(t)
	const driver = `[[ $1 == old ]] && enable -n compopt
source "$2"
COMP_WORDBREAKS=$' \t\n"\'@><=;|&(:'
COMP_LINE=$3
COMP_POINT=${#3}
COMP_TYPE=$4
COMP_CWORD=$5
COLUMNS=100
word=$6
shift 6
COMP_WORDS=("$@")
_thronectl thronectl "$word" "${COMP_WORDS[COMP_CWORD-1]}"
for c in ${COMPREPLY[@]+"${COMPREPLY[@]}"}; do printf '%s\n' "$c"; done`
	profiles := []string{"0", "1", "2", "best", "last"}
	// An absolute path, which on Windows bash splits at the colon after the drive.
	abs := filepath.ToSlash(dir) + "/k"
	absWords, absWant := []string{"thronectl", "import", abs}, abs+" ey.txt"
	if before, after, ok := strings.Cut(abs, ":"); ok {
		absWords = []string{"thronectl", "import", before, ":", after}
		_, absWant, _ = strings.Cut(absWant, ":")
	}
	tests := []struct {
		line  string
		words []string
		want  []string
		descs bool // after a TAB, which shows descriptions in a list, not menu-complete
		old   bool // without compopt, as bash 3.2 runs; it does not split at = and : either
	}{
		{line: "thronectl con", words: []string{"thronectl", "con"}, want: []string{"connect"}},
		{line: "thronectl tun ", words: []string{"thronectl", "tun", ""}, want: []string{"off", "on", "toggle"}},
		{line: "thronectl settings set kill_switch=", words: []string{"thronectl", "settings", "set", "kill_switch", "="},
			want: []string{"false", "true"}},
		{line: "thronectl settings set kill_switch=t", words: []string{"thronectl", "settings", "set", "kill_switch", "=", "t"},
			want: []string{"true"}},
		{line: "thronectl settings set kill_switch=t", words: []string{"thronectl", "settings", "set", "kill_switch=t"},
			want: []string{"true"}},
		{line: "thronectl traffic --by=a", words: []string{"thronectl", "traffic", "--by", "=", "a"}, want: []string{"app"}},
		{line: "thronectl traffic --by=a", words: []string{"thronectl", "traffic", "--by=a"}, want: []string{"app"}},
		{line: "thronectl traffic --by=", words: []string{"thronectl", "traffic", "--by="}, want: []string{"app", "profile"}},
		{line: "thronectl backup create --parts profiles,ro", words: []string{"thronectl", "backup", "create", "--parts", "profiles,ro"},
			want: []string{"profiles,routes"}},
		{line: `thronectl --key-file "x y" tu`, words: []string{"thronectl", "--key-file", `"x y"`, "tu"}, want: []string{"tun"}},
		{line: "thronectl --addr=127.0.0.1:1 tun t", words: []string{"thronectl", "--addr", "=", "127.0.0.1", ":", "1", "tun", "t"},
			want: []string{"toggle"}},
		{line: "thronectl --key-file=k", words: []string{"thronectl", "--key-file", "=", "k"}, want: []string{"k ey.txt"}},
		{line: "thronectl --key-file=k", words: []string{"thronectl", "--key-file=k"}, want: []string{"k ey.txt"}},
		{line: "thronectl import ", words: []string{"thronectl", "import", ""}, want: []string{"-", "k ey.txt", "other", "sub dir", "x@y.txt"}},
		{line: "thronectl import x@y", words: []string{"thronectl", "import", "x", "@", "y"}, want: []string{"y.txt"}},
		{line: "thronectl import a=", words: []string{"thronectl", "import", "a", "="}},
		{line: `thronectl import k\ e`, words: []string{"thronectl", "import", `k\ e`}, want: []string{"k ey.txt"}},
		{line: "thronectl import " + abs, words: absWords, want: []string{absWant}},
		{line: "thronectl status ", words: []string{"thronectl", "status", ""}},
		{line: "thronectl call GET /v1/s", words: []string{"thronectl", "call", "GET", "/v1/s"},
			want: []string{"/v1/settings", "/v1/stats/live", "/v1/stats/traffic", "/v1/status"}},

		{line: "thronectl connect ", words: []string{"thronectl", "connect", ""}, want: []string{"best", "last"}},
		{line: `thronectl --key-file "sub dir/throne.key" connect `, words: []string{"thronectl", "--key-file", `"sub dir/throne.key"`, "connect", ""},
			want: profiles},
		{line: `thronectl --key-file sub\ dir/throne.key connect `, words: []string{"thronectl", "--key-file", `sub\ dir/throne.key`, "connect", ""},
			want: profiles},
		{line: `thronectl --key-file="sub dir/throne.key" connect `,
			words: []string{"thronectl", "--key-file", "=", `"sub dir/throne.key"`, "connect", ""}, want: profiles},
		{line: `thronectl --key-file 'sub dir'/throne.key connect `, words: []string{"thronectl", "--key-file", `'sub dir'/throne.key`, "connect", ""},
			want: profiles},

		{line: "thronectl co", words: []string{"thronectl", "co"}, descs: true, want: []string{
			"completion  (Print a shell completion script)",
			"connect     (Start a profile: by ID, the last one, or the one with the lowest latency)"}},
		{line: "thronectl conn", words: []string{"thronectl", "conn"}, descs: true, want: []string{"connect"}},

		{line: "thronectl conn", words: []string{"thronectl", "conn"}, old: true, want: []string{"connect "}},
		{line: "thronectl settings set ki", words: []string{"thronectl", "settings", "set", "ki"}, old: true, want: []string{"kill_switch="}},
		{line: "thronectl traffic --by=a", words: []string{"thronectl", "traffic", "--by=a"}, old: true, want: []string{"app "}},
		{line: "thronectl import k", words: []string{"thronectl", "import", "k"}, old: true, want: []string{`k\ ey.txt `}},
		{line: "thronectl import su", words: []string{"thronectl", "import", "su"}, old: true, want: []string{`sub\ dir/`}},
		{line: `thronectl import "k`, words: []string{"thronectl", "import", `"k`}, old: true, want: []string{"k ey.txt"}},
		{line: "thronectl --key-file=k", words: []string{"thronectl", "--key-file=k"}, old: true, want: []string{`k\ ey.txt `}},
	}
	for _, tt := range tests {
		version, typ := "", "37"
		if tt.old {
			version = "old"
		}
		if tt.descs {
			typ = "9"
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		args := []string{"-c", driver, "bash", version, script, tt.line, typ, strconv.Itoa(len(tt.words) - 1), readlineWord(tt.line)}
		cmd := exec.CommandContext(ctx, bash, append(args, tt.words...)...)
		cmd.Env, cmd.Dir = env, dir
		out, err := cmd.Output()
		cancel()
		var got []string
		for _, c := range strings.Split(string(out), "\n") {
			if c != "" {
				got = append(got, c)
			}
		}
		slices.Sort(got)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q (%q, old %v): %q, %v, want %q", tt.line, tt.words, tt.old, got, err, tt.want)
		}
	}
}

func TestPowerShellCompletion(t *testing.T) {
	_, throne := startFake(t)
	dir := fileDir(t)
	here := "." + string(filepath.Separator)
	profiles := []string{"last", "best", "0", "1", "2"}
	tests := []struct {
		line string
		want []string
		cd   string // a directory to go to with Set-Location first
	}{
		{line: "thronectl con", want: []string{"connect"}},
		{line: "thronectl tun ", want: []string{"on", "off", "toggle"}},
		{line: "thronectl backup create --parts profiles,ro", want: []string{"routes"}},
		{line: "thronectl traffic --by=a", want: []string{"--by=app"}},
		{line: `thronectl --key-file 'my dir\k' tun t`, want: []string{"toggle"}},
		{line: `thronectl --key-file "C:\a b\" tun t`, want: []string{"toggle"}},
		{line: "thronectl connect ", want: profiles},
		{line: "thronectl import ", want: []string{"-", "'" + here + "k ey.txt'", here + "other", here + "x@y.txt", "'" + here + "sub dir'"}},
		{line: "thronectl --key-file=k", want: []string{"--key-file='" + here + "k ey.txt'"}},
		{line: "thronectl status "},
		{line: "thronectl --addr "},
		{line: "thronectl connect --max-age "},
		{line: "thronectl --key-file " + here + "throne.key connect ", want: profiles, cd: "sub dir"},
		{line: `thronectl --key-file "$PWD` + string(filepath.Separator) + `throne.key" connect `, want: profiles},
		{line: "thronectl --key-file $PWD" + string(filepath.Separator) + "throne.key connect ", want: profiles},
		{line: `thronectl --key-file "$(Get-Location)` + string(filepath.Separator) + `throne.key" connect `, want: []string{"last", "best"}},
	}
	var driver strings.Builder
	driver.WriteString("param($script)\n. $script\n")
	for i, tt := range tests {
		if tt.cd != "" {
			driver.WriteString("Set-Location -LiteralPath '" + tt.cd + "'\n")
		}
		line := strings.ReplaceAll(tt.line, "'", "''")
		driver.WriteString("'=== " + strconv.Itoa(i) + "'\n")
		// Where there is nothing to complete, CompleteInput throws on the script's empty string.
		driver.WriteString("try { (TabExpansion2 -inputScript '" + line + "' -cursorColumn " + strconv.Itoa(len(tt.line)) +
			").CompletionMatches | ForEach-Object CompletionText } catch { }\n")
	}
	scripts := t.TempDir()
	file := filepath.Join(scripts, "driver.ps1")
	if err := os.WriteFile(file, []byte(driver.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, scripts, "powershell")
	env := scriptEnv(t, throne)
	for _, ps := range []string{"pwsh", "powershell"} {
		t.Run(ps, func(t *testing.T) {
			path := shell(t, ps, "-NoProfile", "-NonInteractive", "-Command", "exit 0")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, path, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file, script)
			cmd.Env, cmd.Dir = env, dir
			// The output decides: on a loaded machine PowerShell has crashed on exit after it.
			out, err := cmd.CombinedOutput()
			defer func() {
				if err != nil && t.Failed() {
					t.Logf("%s: %v\n%s", ps, err, out)
				}
			}()
			got := map[int][]string{}
			i := -1
			for line := range strings.Lines(strings.ReplaceAll(string(out), "\r\n", "\n")) {
				line = strings.TrimSuffix(line, "\n")
				if n, ok := strings.CutPrefix(line, "=== "); ok {
					i, _ = strconv.Atoi(n)
					continue
				}
				got[i] = append(got[i], line)
			}
			for i, tt := range tests {
				// PowerShell sorts the file names, which may differ by platform.
				slices.Sort(got[i])
				if want := slices.Sorted(slices.Values(tt.want)); !reflect.DeepEqual(got[i], want) {
					t.Errorf("%q: %q, want %q", tt.line, got[i], want)
				}
			}
		})
	}
}
