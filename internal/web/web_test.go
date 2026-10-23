package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/testserver"
)

// Keys from spec/vectors/v1.json.
const (
	testKey  = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	otherKey = "bljyVf7GZBkFc4ehd35siy7LduS9QD-st76wDYoRFsE"
)

var fastTiming = timing{
	eventsWait:    time.Second,
	retry:         10 * time.Millisecond,
	minPoll:       time.Millisecond,
	statusPoll:    10 * time.Millisecond,
	fullRefresh:   time.Hour,
	ping:          time.Hour,
	loginFloor:    0,
	passwordCheck: time.Hour,
	shutdown:      time.Second,
	streamWrite:   5 * time.Second,
	bodyRead:      5 * time.Second,
	recentOK:      10 * time.Minute,
}

// fakeThrone answers requests by "METHOD path", and records them.
type fakeThrone struct {
	mu     sync.Mutex
	routes map[string]func(r *testserver.Request) *testserver.Response
	reqs   []*testserver.Request
}

func (f *fakeThrone) on(route string, h func(r *testserver.Request) *testserver.Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[route] = h
}

func (f *fakeThrone) handle(_ context.Context, r *testserver.Request) *testserver.Response {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	h := f.routes[r.Method+" "+r.Path]
	f.mu.Unlock()
	if h == nil {
		return testserver.Error(404, "not_found", "Unknown path")
	}
	return h(r)
}

func (f *fakeThrone) last() *testserver.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		return nil
	}
	return f.reqs[len(f.reqs)-1]
}

func respond(body string) func(*testserver.Request) *testserver.Response {
	return func(*testserver.Request) *testserver.Response { return testserver.OK(json.RawMessage(body)) }
}

// gateway is a Server under test behind an httptest server.
type gateway struct {
	t        *testing.T
	s        *Server
	ts       *httptest.Server
	fake     *fakeThrone
	throne   *testserver.Server
	password string
	clock    *clock
}

// clock is a settable time for sessions and the login limiter.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newGateway starts a fake Throne and a gateway for it; edit adjusts the Config.
func newGateway(t *testing.T, edit func(*Config)) *gateway {
	t.Helper()
	f := &fakeThrone{routes: map[string]func(*testserver.Request) *testserver.Response{}}
	srv := testserver.Start(t, testserver.Config{Key: testKey, Handler: f.handle})
	key, err := throne.ParseKey(testKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Throne: throne.New(srv.Addr, key), ThroneAddr: srv.Addr, Version: "v9.9.9", Listen: "127.0.0.1:0",
		StateDir: t.TempDir(), Logf: func(format string, args ...any) { t.Logf("gateway: "+format, args...) }}
	if edit != nil {
		edit(&cfg)
	}
	return startGateway(t, cfg, f, srv)
}

func startGateway(t *testing.T, cfg Config, f *fakeThrone, srv *testserver.Server) *gateway {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.t = fastTiming
	c := &clock{t: time.Unix(1760000000, 0)}
	s.now = c.now
	s.assets = fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>Throne</title>")},
		"app.js":           {Data: []byte("export {}")},
		"style.css":        {Data: []byte("body{}")},
		"icon.svg":         {Data: []byte("<svg/>")},
		"vendor/preact.js": {Data: []byte("export const h = 1")},
		"vendor/sub/x.txt": {Data: []byte("x")},
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(s.Close)
	return &gateway{t: t, s: s, ts: ts, fake: f, throne: srv, password: s.created, clock: c}
}

// browser is a client with its own cookies and CSRF token.
type browser struct {
	g    *gateway
	c    *http.Client
	csrf string
}

func (g *gateway) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{g: g, c: &http.Client{Jar: jar}}
}

// login signs in with the gateway's password and fails the test otherwise.
func (g *gateway) login() *browser {
	g.t.Helper()
	b := g.browser()
	resp, body := b.do("POST", "/auth/login", `{"password":`+strconv.Quote(g.password)+`}`, nil)
	if resp.StatusCode != 200 {
		g.t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	var st struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(body, &st)
	b.csrf = st.CSRF
	return b
}

// do sends a request; non-GET requests carry the CSRF token unless headers set one.
func (b *browser) do(method, path, body string, headers map[string]string) (*http.Response, []byte) {
	b.g.t.Helper()
	req, err := http.NewRequest(method, b.g.ts.URL+path, strings.NewReader(body))
	if err != nil {
		b.g.t.Fatal(err)
	}
	if b.csrf != "" {
		req.Header.Set("X-CSRF-Token", b.csrf)
	}
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.g.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error.Code
}

func TestLoginAndState(t *testing.T) {
	g := newGateway(t, nil)
	if !regexp.MustCompile(`^[a-z2-9]{5}(-[a-z2-9]{5}){3}$`).MatchString(g.password) {
		t.Fatalf("generated password %q", g.password)
	}
	b := g.browser()
	if resp, body := b.do("GET", "/auth/state", "", nil); resp.StatusCode != 200 || string(body) != `{"authenticated":false}` {
		t.Fatalf("state before login: %d %s", resp.StatusCode, body)
	}
	if resp, body := b.do("POST", "/auth/login", `{"password":"wrong password"}`, nil); resp.StatusCode != 401 || errorCode(body) != "bad_password" {
		t.Fatalf("wrong password: %d %s", resp.StatusCode, body)
	}
	if resp, body := b.do("POST", "/auth/login", `{"password":`, nil); resp.StatusCode != 400 || errorCode(body) != "bad_request" {
		t.Fatalf("malformed login: %d %s", resp.StatusCode, body)
	}
	if resp, _ := b.do("GET", "/auth/login", "", nil); resp.StatusCode != 405 {
		t.Fatalf("GET login: %d", resp.StatusCode)
	}
	resp, body := b.do("POST", "/auth/login", `{"password":"`+g.password+`"}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	cookie := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"throne_web=", "Path=/", "HttpOnly", "SameSite=Strict", "Max-Age=604800"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie %q lacks %s", cookie, want)
		}
	}
	if strings.Contains(cookie, "Secure") {
		t.Errorf("cookie %q is Secure without TLS", cookie)
	}
	var st map[string]any
	json.Unmarshal(body, &st)
	want := map[string]any{"version": "v9.9.9", "throne": g.throne.Addr, "tls": false, "fingerprint": nil, "password_file": false}
	if st["authenticated"] != true || len(st["csrf"].(string)) != 43 || !reflect.DeepEqual(st["gateway"], want) {
		t.Errorf("login body %s", body)
	}
	if _, again := b.do("GET", "/auth/state", "", nil); string(again) != `{"authenticated":false}` {
		t.Errorf("state from the cookie alone: %s", again)
	}
	b.csrf = st["csrf"].(string)
	if _, again := b.do("GET", "/auth/state", "", nil); string(again) != string(body) {
		t.Errorf("state %s, want %s", again, body)
	}
	if _, again := b.do("GET", "/auth/state?token="+b.csrf, "", map[string]string{"X-CSRF-Token": ""}); string(again) != `{"authenticated":false}` {
		t.Errorf("state with the token in the query: %s", again)
	}

	if resp, body := b.do("POST", "/auth/logout", "", map[string]string{"X-CSRF-Token": "nope"}); resp.StatusCode != 401 || errorCode(body) != "unauthenticated" {
		t.Errorf("logout without the token: %d %s", resp.StatusCode, body)
	}
	resp, _ = b.do("POST", "/auth/logout", "", nil)
	if resp.StatusCode != 204 || !strings.Contains(resp.Header.Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("logout: %d, cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	if _, body := b.do("GET", "/auth/state", "", nil); string(body) != `{"authenticated":false}` {
		t.Errorf("state after logout: %s", body)
	}
	if g.s.sessions.count() != 0 {
		t.Errorf("%d sessions after logout", g.s.sessions.count())
	}
}

func TestSecureCookieAndHeaders(t *testing.T) {
	g := newGateway(t, func(c *Config) { c.TLS = true })
	b := g.browser()
	resp, body := b.do("POST", "/auth/login", `{"password":"`+g.password+`"}`, nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Set-Cookie"), "; Secure") {
		t.Fatalf("login: %d, cookie %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
	if !strings.Contains(string(body), `"tls":true,"fingerprint":"`+g.s.Fingerprint()+`"`) {
		t.Errorf("login body %s", body)
	}
	for path, cache := range map[string]string{"/": "no-store", "/auth/state": "no-store", "/nope": "no-store",
		"/vendor/preact.js": "max-age=86400"} {
		resp, _ := b.do("GET", path, "", nil)
		h := resp.Header
		if h.Get("Cache-Control") != cache || h.Get("X-Content-Type-Options") != "nosniff" ||
			h.Get("Referrer-Policy") != "no-referrer" || h.Get("X-Frame-Options") != "DENY" ||
			!strings.HasPrefix(h.Get("Content-Security-Policy"), "default-src 'self'; script-src 'self';") ||
			h.Get("Strict-Transport-Security") != "" {
			t.Errorf("%s headers %v", path, h)
		}
	}
}

func TestRateLimit(t *testing.T) {
	g := newGateway(t, nil)
	b := g.browser()
	for i := range 5 {
		if resp, _ := b.do("POST", "/auth/login", `{"password":"wrong"}`, nil); resp.StatusCode != 401 {
			t.Fatalf("failure %d: %d", i+1, resp.StatusCode)
		}
	}
	resp, body := b.do("POST", "/auth/login", `{"password":"`+g.password+`"}`, nil)
	var e struct {
		Error struct {
			Code       string `json:"code"`
			RetryAfter int    `json:"retry_after"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "60" || e.Error.Code != "rate_limited" || e.Error.RetryAfter != 60 {
		t.Fatalf("locked login: %d, Retry-After %q, %s", resp.StatusCode, resp.Header.Get("Retry-After"), body)
	}
	g.clock.add(61 * time.Second)
	if resp, _ := b.do("POST", "/auth/login", `{"password":"wrong"}`, nil); resp.StatusCode != 401 {
		t.Fatalf("after the lock: %d", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/auth/login", `{"password":"wrong"}`, nil); resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "120" {
		t.Fatalf("doubled lock: %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	g.clock.add(121 * time.Second)
	if resp, _ := b.do("POST", "/auth/login", `{"password":"`+g.password+`"}`, nil); resp.StatusCode != 200 {
		t.Fatalf("login after the lock: %d", resp.StatusCode)
	}
	for range 4 {
		b.do("POST", "/auth/login", `{"password":"wrong"}`, nil)
	}
	if resp, _ := b.do("POST", "/auth/login", `{"password":"wrong"}`, nil); resp.StatusCode != 401 {
		t.Errorf("a success did not reset the count: %d", resp.StatusCode)
	}
}

func TestRateLimitParallel(t *testing.T) {
	g := newGateway(t, nil)
	codes := make(chan int, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			resp, err := http.Post(g.ts.URL+"/auth/login", "application/json", strings.NewReader(`{"password":"wrong"}`))
			if err != nil {
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	n := map[int]int{}
	for c := range codes {
		n[c]++
	}
	if n[401] < 5 || n[401] > 6 || n[401]+n[429] != 20 {
		t.Errorf("parallel failures: %v", n)
	}
}

func TestLoginFloor(t *testing.T) {
	g := newGateway(t, nil)
	g.s.t.loginFloor = 200 * time.Millisecond
	b := g.browser()
	start := time.Now()
	if resp, _ := b.do("POST", "/auth/login", `{"password":"wrong"}`, nil); resp.StatusCode != 401 {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Errorf("failure took %v", d)
	}
}

func TestLimiter(t *testing.T) {
	l := newLoginLimiter()
	now := time.Unix(1760000000, 0)
	a, b := clientKey("192.0.2.1:5000"), clientKey("[2001:db8:1:2::5]:5000")
	if b != clientKey("[2001:db8:1:2:ffff::1]:80") || b == clientKey("[2001:db8:1:3::5]:80") {
		t.Errorf("IPv6 clients are not grouped by /64")
	}
	if clientKey("[::ffff:192.0.2.1]:1") != a {
		t.Errorf("an IPv4-mapped address is a different client")
	}
	for i := range 30 {
		l.fail(clientKey("198.51.100."+strconv.Itoa(i)+":1"), now)
	}
	if l.blocked(a, now) != 0 {
		t.Fatal("30 failures lock everyone")
	}
	l.fail(clientKey("198.51.100.200:1"), now.Add(10*time.Second))
	if d := l.blocked(a, now.Add(10*time.Second)); d != 50*time.Second {
		t.Fatalf("31 failures lock for %v", d)
	}
	if d := l.blocked(a, now.Add(60*time.Second)); d != 0 {
		t.Errorf("still locked after the minute: %v", d)
	}
	for i := range 20 {
		lock := l.fail(b, now.Add(time.Duration(i)*time.Hour))
		if i == 0 && lock != 0 {
			t.Errorf("first failure locks")
		}
	}
	if l.blocked(b, now.Add(19*time.Hour+time.Minute)) != 0 {
		t.Errorf("failures hours apart lock")
	}
	var lock time.Duration
	for i := range 12 {
		lock = l.fail(a, now.Add(time.Duration(i)*time.Second))
	}
	if lock != maxLock {
		t.Errorf("lock after 12 failures %v", lock)
	}
}

func TestCSRFAndOrigin(t *testing.T) {
	g := newGateway(t, nil)
	g.fake.on("PUT /v1/modes", respond(`{"state":"idle"}`))
	b := g.login()
	host := strings.TrimPrefix(g.ts.URL, "http://")
	cases := []struct {
		headers map[string]string
		status  int
	}{
		{map[string]string{"X-CSRF-Token": ""}, 401},
		{map[string]string{"X-CSRF-Token": b.csrf + "x"}, 401},
		{map[string]string{"Origin": "http://evil.example"}, 403},
		{map[string]string{"Origin": "null"}, 403},
		{map[string]string{"Origin": "http://" + host}, 200},
		{map[string]string{"Origin": "https://" + host}, 200},
		{nil, 200},
	}
	for _, c := range cases {
		resp, body := b.do("PUT", "/api/v1/modes", `{"tun":true}`, c.headers)
		if resp.StatusCode != c.status || (c.status == 403 && errorCode(body) != "csrf") {
			t.Errorf("%v: %d %s", c.headers, resp.StatusCode, body)
		}
	}
	if resp, _ := g.browser().do("POST", "/auth/login", `{"password":"`+g.password+`"}`,
		map[string]string{"Origin": "http://evil.example"}); resp.StatusCode != 403 {
		t.Errorf("cross-site login: %d", resp.StatusCode)
	}
	if resp, body := g.browser().do("GET", "/api/v1/status", "", nil); resp.StatusCode != 401 || errorCode(body) != "unauthenticated" {
		t.Errorf("without a session: %d %s", resp.StatusCode, body)
	}
	if resp, body := g.browser().do("GET", "/api/nope", "", nil); resp.StatusCode != 401 {
		t.Errorf("unknown path without a session: %d %s", resp.StatusCode, body)
	}
	if resp, body := b.do("GET", "/api/nope", "", nil); resp.StatusCode != 404 || errorCode(body) != "not_found" {
		t.Errorf("unknown path: %d %s", resp.StatusCode, body)
	}
	g.fake.on("GET /v1/status", respond(`{"state":"idle"}`))
	for _, h := range []map[string]string{{"X-CSRF-Token": ""}, {"X-CSRF-Token": "x" + b.csrf[1:]}} {
		if resp, body := b.do("GET", "/api/v1/status", "", h); resp.StatusCode != 401 || errorCode(body) != "unauthenticated" {
			t.Errorf("GET with %v: %d %s", h, resp.StatusCode, body)
		}
	}
	if resp, body := b.do("GET", "/api/v1/status?token="+b.csrf, "", map[string]string{"X-CSRF-Token": ""}); resp.StatusCode != 401 {
		t.Errorf("proxy GET with the token in the query: %d %s", resp.StatusCode, body)
	}
	if resp, body := b.do("GET", "/api/v1/status", "", nil); resp.StatusCode != 200 {
		t.Errorf("GET with the token: %d %s", resp.StatusCode, body)
	}
}

func TestHostCheck(t *testing.T) {
	cases := []struct {
		listen string
		allow  []string
		host   string
		ok     bool
	}{
		{"127.0.0.1:9096", nil, "127.0.0.1:9096", true},
		{"127.0.0.1:9096", nil, "LOCALHOST:9096", true},
		{"127.0.0.1:9096", nil, "localhost.", true},
		{"[::1]:9096", nil, "[::1]:9096", true},
		{"127.0.0.1:9096", nil, "evil.example:9096", false},
		{"127.0.0.1:9096", nil, "192.168.1.5:9096", false},
		{"127.0.0.1:9096", nil, "", false},
		{"127.0.0.1:9096", []string{"throne.lan"}, "throne.lan:9096", true},
		{":9096", nil, "anything.example", true},
		{":9096", []string{"Throne.LAN"}, "throne.lan", true},
		{":9096", []string{"throne.lan"}, "evil.example", false},
		{":9096", []string{"throne.lan"}, "192.168.1.5:9096", true},
		{":9096", []string{"throne.lan"}, "[fd00::1]:9096", true},
		{":9096", []string{"throne.lan"}, "localhost:9096", true},
	}
	for _, c := range cases {
		loopback, _ := loopbackListen(c.listen)
		allow, _ := allowList(c.allow)
		s := &Server{loopback: loopback, allow: allow}
		if got := s.hostAllowed(c.host); got != c.ok {
			t.Errorf("listen %s, allow %v, Host %q: %v", c.listen, c.allow, c.host, got)
		}
	}
	g := newGateway(t, nil)
	req, _ := http.NewRequest("GET", g.ts.URL+"/", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 421 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") ||
		resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Errorf("foreign host: %d %v %s", resp.StatusCode, resp.Header, body)
	}
}

func TestParseListen(t *testing.T) {
	cases := []struct {
		in, addr string
		loopback bool
	}{
		{"9096", "127.0.0.1:9096", true},
		{"127.0.0.1:9096", "127.0.0.1:9096", true},
		{"192.168.1.5:80", "192.168.1.5:80", false},
		{"[::1]:9096", "[::1]:9096", true},
		{"[fd00::5]:9096", "[fd00::5]:9096", false},
		{"*:9096", ":9096", false},
		{":9096", ":9096", false},
		{" 0.0.0.0:9096 ", "0.0.0.0:9096", false},
	}
	for _, c := range cases {
		addr, loopback, err := ParseListen(c.in)
		if err != nil || addr != c.addr || loopback != c.loopback {
			t.Errorf("ParseListen(%q) = %q, %v, %v", c.in, addr, loopback, err)
		}
	}
	for _, in := range []string{"", "x", "70000", "host:9096", "::1:9096", "[127.0.0.1]:1", "1.2.3:4", "[fe80::1%eth0]:1", "+80", "localhost:9096"} {
		if _, _, err := ParseListen(in); err == nil {
			t.Errorf("ParseListen(%q) passed", in)
		}
	}
}

func TestProxy(t *testing.T) {
	g := newGateway(t, nil)
	f := g.fake
	f.on("GET /v1/status", respond(`{"state":"idle","version":"1.4.0"}`))
	f.on("GET /v1/profiles", respond(`{"profiles":[]}`))
	f.on("PUT /v1/connection", func(*testserver.Request) *testserver.Response {
		return testserver.Accepted(json.RawMessage(`{"state":"connecting"}`))
	})
	f.on("DELETE /v1/results", func(*testserver.Request) *testserver.Response { return testserver.OK(nil) })
	f.on("PUT /v1/routes/active", func(*testserver.Request) *testserver.Response {
		return testserver.Error(409, "busy", "Another profile is starting")
	})
	b := g.login()

	resp, body := b.do("GET", "/api/v1/status", "", nil)
	if resp.StatusCode != 200 || string(body) != `{"state":"idle","version":"1.4.0"}` || resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("status: %d %s", resp.StatusCode, body)
	}
	b.do("GET", "/api/v1/profiles?group=2&group=3&name=de%20", "", nil)
	if r := f.last(); r.Path != "/v1/profiles" || !reflect.DeepEqual(r.Query, map[string]string{"group": "2", "name": "de "}) || r.Body != nil {
		t.Errorf("profiles request %+v", r)
	}
	resp, body = b.do("PUT", "/api/v1/connection", ` {"select":"last"} `, nil)
	if resp.StatusCode != 202 || string(body) != `{"state":"connecting"}` {
		t.Errorf("connect: %d %s", resp.StatusCode, body)
	}
	if r := f.last(); r.Method != "PUT" || string(r.Body) != `{"select":"last"}` {
		t.Errorf("connect request %+v", r)
	}
	resp, body = b.do("DELETE", "/api/v1/results", "", nil)
	if resp.StatusCode != 200 || string(body) != `{}` || f.last().Body != nil {
		t.Errorf("null body: %d %s, request body %s", resp.StatusCode, body, f.last().Body)
	}
	resp, body = b.do("PUT", "/api/v1/routes/active", `{"id":1}`, nil)
	if resp.StatusCode != 409 || string(body) != `{"error":{"code":"busy","message":"Another profile is starting"}}` {
		t.Errorf("Throne error: %d %s", resp.StatusCode, body)
	}
	resp, body = b.do("GET", "/api/v1/nothing/here", "", nil)
	if resp.StatusCode != 404 || errorCode(body) != "not_found" {
		t.Errorf("unknown Throne path: %d %s", resp.StatusCode, body)
	}

	for _, path := range []string{"/api/v1/../v1/status", "/api/v1/profiles%2F1", "/api/v1/caf%C3%A9", "/api/v1/a//b",
		"/api/v1/", "/api/v1/status%3Fx=1", "/api/v1/./status"} {
		if resp, body := b.do("GET", path, "", nil); resp.StatusCode != 400 || errorCode(body) != "bad_request" {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	if resp, _ := b.do("PATCH", "/api/v1/status", "{}", nil); resp.StatusCode != 405 || resp.Header.Get("Allow") == "" {
		t.Errorf("PATCH: %d", resp.StatusCode)
	}
	if resp, body := b.do("POST", "/api/v1/import", `{"text":`, nil); resp.StatusCode != 400 || errorCode(body) != "bad_request" {
		t.Errorf("invalid JSON: %d %s", resp.StatusCode, body)
	}
	big := `{"text":"` + strings.Repeat("a", 1<<20) + `"}`
	if resp, body := b.do("POST", "/api/v1/import", big, nil); resp.StatusCode != 413 || errorCode(body) != "too_large" {
		t.Errorf("large body: %d %s", resp.StatusCode, body)
	}

	g.throne.Close()
	resp, body = b.do("GET", "/api/v1/status", "", nil)
	if resp.StatusCode != 502 || errorCode(body) != "throne_unreachable" || !strings.Contains(string(body), g.throne.Addr) {
		t.Errorf("Throne down: %d %s", resp.StatusCode, body)
	}
}

func TestProxyRejected(t *testing.T) {
	g := newGateway(t, func(c *Config) {
		key, _ := throne.ParseKey(otherKey)
		c.Throne = throne.New(c.ThroneAddr, key)
	})
	b := g.login()
	if resp, body := b.do("GET", "/api/v1/status", "", nil); resp.StatusCode != 502 || errorCode(body) != "throne_rejected" {
		t.Errorf("wrong key: %d %s", resp.StatusCode, body)
	}
}

func TestClassifyTimeout(t *testing.T) {
	s := &Server{cfg: Config{ThroneAddr: "127.0.0.1:9095"}}
	status, code, _ := s.classify(context.DeadlineExceeded)
	if status != 504 || code != "throne_timeout" {
		t.Errorf("timeout: %d %s", status, code)
	}
	status, code, _ = s.classify(errorString("throne: connect to 127.0.0.1:9095: no answer within 5s: " + context.DeadlineExceeded.Error()))
	if status != 502 || code != "throne_unreachable" {
		t.Errorf("connect timeout: %d %s", status, code)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestAssets(t *testing.T) {
	g := newGateway(t, nil)
	b := g.browser()
	cases := []struct {
		path, ctype string
		status      int
	}{
		{"/", "text/html; charset=utf-8", 200},
		{"/app.js", "text/javascript; charset=utf-8", 200},
		{"/style.css", "text/css; charset=utf-8", 200},
		{"/icon.svg", "image/svg+xml", 200},
		{"/vendor/preact.js", "text/javascript; charset=utf-8", 200},
		{"/vendor/", "", 404},
		{"/vendor", "", 404},
		{"/missing.js", "", 404},
		{"/../web.go", "", 404},
		{"/vendor/../app.js", "", 404},
	}
	for _, c := range cases {
		resp, body := b.do("GET", c.path, "", nil)
		if resp.StatusCode != c.status || (c.ctype != "" && resp.Header.Get("Content-Type") != c.ctype) {
			t.Errorf("%s: %d %q %q", c.path, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}
	if resp, _ := b.do("POST", "/", "", nil); resp.StatusCode != 405 {
		t.Errorf("POST /: %d", resp.StatusCode)
	}
	if _, err := fs.ReadFile(embedded(), "vendor/preact.js"); err != nil {
		t.Errorf("embedded vendor files: %v", err)
	}
}

func TestBackup(t *testing.T) {
	g := newGateway(t, nil)
	archive := bytes.Repeat([]byte("THRN archive "), 1000)
	sum := sha256.Sum256(archive)
	sha := hex.EncodeToString(sum[:])
	var parts any
	g.fake.on("POST /v1/backups", func(r *testserver.Request) *testserver.Response {
		parts = r.Fields()["parts"]
		return testserver.OK(map[string]any{"id": 1, "size": len(archive), "sha256": sha, "parts": []string{"profiles"}})
	})
	g.fake.on("GET /v1/backups/1", func(r *testserver.Request) *testserver.Response {
		offset, _ := strconv.Atoi(r.Query["offset"])
		end := min(offset+5000, len(archive))
		return testserver.OK(map[string]any{"size": len(archive), "sha256": sha, "parts": []string{"profiles"},
			"offset": offset, "data": archive[offset:end], "eof": end == len(archive)})
	})
	g.fake.on("DELETE /v1/backups/1", respond(`{"deleted":1}`))
	b := g.login()
	resp, body := b.do("POST", "/api/backup", `{"parts":["profiles"]}`, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, archive) {
		t.Fatalf("backup: %d, %d bytes", resp.StatusCode, len(body))
	}
	h := resp.Header
	if h.Get("Content-Type") != "application/octet-stream" || h.Get("Content-Length") != strconv.Itoa(len(archive)) ||
		h.Get("Content-Disposition") != `attachment; filename="throne-backup-`+g.clock.now().Format("20060102-150405")+`.thrbackup"` {
		t.Errorf("backup headers %v", h)
	}
	if !reflect.DeepEqual(parts, []any{"profiles"}) {
		t.Errorf("parts %v", parts)
	}
	if resp, _ := b.do("POST", "/api/backup", "", nil); resp.StatusCode != 200 || parts != nil {
		t.Errorf("backup without a body: %d, parts %v", resp.StatusCode, parts)
	}
	if resp, _ := b.do("POST", "/api/backup", "", map[string]string{"X-CSRF-Token": "x"}); resp.StatusCode != 401 {
		t.Errorf("backup without the token: %d", resp.StatusCode)
	}
	if resp, _ := b.do("GET", "/api/backup", "", nil); resp.StatusCode != 405 {
		t.Errorf("GET backup: %d", resp.StatusCode)
	}
	g.fake.on("POST /v1/backups", func(*testserver.Request) *testserver.Response {
		return testserver.Error(403, "forbidden", "Admin requests are refused")
	})
	if resp, body := b.do("POST", "/api/backup", "{}", nil); resp.StatusCode != 403 || errorCode(body) != "forbidden" {
		t.Errorf("forbidden backup: %d %s", resp.StatusCode, body)
	}
	assertNoTemp(t, g.s.cfg.StateDir)
}

func TestRestore(t *testing.T) {
	g := newGateway(t, nil)
	archive := bytes.Repeat([]byte("THRN restore "), 1000)
	var received []byte
	g.fake.on("POST /v1/restores", func(r *testserver.Request) *testserver.Response {
		received = nil
		return testserver.OK(map[string]any{"id": 2, "chunk_limit": 4096})
	})
	g.fake.on("PUT /v1/restores/2", func(r *testserver.Request) *testserver.Response {
		var chunk struct {
			Data []byte `json:"data"`
		}
		json.Unmarshal(r.Body, &chunk)
		received = append(received, chunk.Data...)
		st := map[string]any{"id": 2, "size": len(archive), "received": len(received)}
		if len(received) == len(archive) {
			st["state"] = "complete"
			st["archive"] = map[string]any{"created_at": "Thu Oct 16 15:04:05 2026", "platform": "linux", "parts": []string{"profiles"}}
		}
		return testserver.OK(st)
	})
	b := g.login()
	resp, body := b.do("POST", "/api/restore", string(archive), map[string]string{"Content-Type": "application/octet-stream"})
	want := `{"id":2,"size":13000,"received":13000,"state":"complete","archive":{"created_at":"Thu Oct 16 15:04:05 2026","platform":"linux","parts":["profiles"]}}`
	if resp.StatusCode != 200 || string(body) != want {
		t.Fatalf("restore: %d %s", resp.StatusCode, body)
	}
	if !bytes.Equal(received, archive) {
		t.Errorf("Throne received %d bytes", len(received))
	}
	if resp, body := b.do("POST", "/api/restore", "", nil); resp.StatusCode != 400 || errorCode(body) != "bad_request" {
		t.Errorf("empty archive: %d %s", resp.StatusCode, body)
	}
	assertNoTemp(t, g.s.cfg.StateDir)
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("left behind: %s", e.Name())
		}
	}
}

func TestPasswordChange(t *testing.T) {
	g := newGateway(t, nil)
	a, other := g.login(), g.login()
	if resp, body := a.do("POST", "/auth/password", `{"current":"`+g.password+`","new":"short"}`, nil); resp.StatusCode != 400 || errorCode(body) != "bad_request" {
		t.Errorf("short password: %d %s", resp.StatusCode, body)
	}
	if resp, body := a.do("POST", "/auth/password", `{"current":"wrong","new":"long enough"}`, nil); resp.StatusCode != 401 || errorCode(body) != "bad_password" {
		t.Errorf("wrong current password: %d %s", resp.StatusCode, body)
	}
	if resp, body := a.do("POST", "/auth/password", `{"current":"`+g.password+`","new":"long enough"}`, nil); resp.StatusCode != 204 {
		t.Fatalf("change: %d %s", resp.StatusCode, body)
	}
	if _, body := a.do("GET", "/auth/state", "", nil); !strings.Contains(string(body), `"authenticated":true`) {
		t.Errorf("the changing session ended: %s", body)
	}
	if _, body := other.do("GET", "/auth/state", "", nil); string(body) != `{"authenticated":false}` {
		t.Errorf("another session survived: %s", body)
	}
	g.s.checkPasswordFile()
	if _, body := a.do("GET", "/auth/state", "", nil); !strings.Contains(string(body), `"authenticated":true`) {
		t.Errorf("the gateway's own change counted as an outside one: %s", body)
	}
	g.password = "long enough"
	g.login()

	if err := StorePassword(g.s.cfg.StateDir, "changed elsewhere"); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	os.Chtimes(filepath.Join(g.s.cfg.StateDir, "password"), later, later)
	g.s.checkPasswordFile()
	if _, body := a.do("GET", "/auth/state", "", nil); string(body) != `{"authenticated":false}` {
		t.Errorf("a session survived an outside change: %s", body)
	}
	g.password = "changed elsewhere"
	g.login()
}

func TestPasswordFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(file, []byte("\xEF\xBB\xBFdocker secret\r\n"), 0o600)
	g := newGateway(t, func(c *Config) { c.PasswordFile = file })
	if g.password != "" {
		t.Errorf("created a password with a password file")
	}
	if _, err := os.Stat(filepath.Join(g.s.cfg.StateDir, "password")); err == nil {
		t.Errorf("stored a hash with a password file")
	}
	g.password = "docker secret"
	b := g.login()
	if _, body := b.do("GET", "/auth/state", "", nil); !strings.Contains(string(body), `"password_file":true`) {
		t.Errorf("state %s", body)
	}
	if resp, body := b.do("POST", "/auth/password", `{"current":"docker secret","new":"long enough"}`, nil); resp.StatusCode != 409 || errorCode(body) != "password_file" {
		t.Errorf("change with a password file: %d %s", resp.StatusCode, body)
	}
	os.WriteFile(file, []byte("new secret\n"), 0o600)
	os.Chtimes(file, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	g.s.checkPasswordFile()
	if _, body := b.do("GET", "/auth/state", "", nil); string(body) != `{"authenticated":false}` {
		t.Errorf("session survived a new password file: %s", body)
	}
	g.password = "new secret"
	g.login()

	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, []byte("\n"), 0o600)
	_, err := New(Config{Throne: g.s.throne, Listen: "127.0.0.1:0", StateDir: t.TempDir(), PasswordFile: empty})
	if _, ok := err.(*ConfigError); !ok || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty password file: %v", err)
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`).MatchString(hash) {
		t.Fatalf("hash %q", hash)
	}
	h, err := parsePHC(hash)
	if err != nil || !h.matches("correct horse") || h.matches("correct horsE") {
		t.Errorf("parse %v", err)
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		strings.Replace(hash, "m=19456", "m=4194304", 1), strings.Replace(hash, "t=2", "t=1000", 1)} {
		if _, err := parsePHC(bad); err == nil {
			t.Errorf("parsePHC(%q) passed", bad)
		}
	}
	dir := t.TempDir()
	if err := StorePassword(dir, "correct horse"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "password"))
	if lines := strings.Split(string(b), "\n"); len(lines) != 2 || !strings.HasPrefix(lines[0], "$argon2id$") {
		t.Errorf("password file %q", b)
	}
	assertNoTemp(t, dir)
	if err := CheckPassword("1234567"); err == nil {
		t.Errorf("7 characters pass")
	}
	if err := CheckPassword("ääääääää"); err != nil {
		t.Errorf("8 characters: %v", err)
	}
}

func TestSessions(t *testing.T) {
	c := &clock{t: time.Unix(1760000000, 0)}
	st := newSessionStore(c.now)
	first, token := st.create()
	get := func(token string) *session { return st.authorize(token, first.csrf) }
	for range maxSessions - 1 {
		c.add(time.Second)
		st.create()
	}
	c.add(time.Second)
	if get(token) != first {
		t.Fatal("lost a session")
	}
	if st.authorize(token, "") != nil || st.authorize(token, first.csrf[1:]) != nil || st.authorize("", first.csrf) != nil {
		t.Fatal("a session without its token")
	}
	c.add(time.Second)
	st.create()
	if st.count() != maxSessions || get(token) != first {
		t.Fatalf("evicted the session used last: %d", st.count())
	}
	c.add(sessionIdle - time.Second)
	if get(token) != first {
		t.Fatal("idle session ended early")
	}
	c.add(sessionIdle)
	if get(token) != nil {
		t.Fatal("idle session lives on")
	}
	select {
	case <-first.done:
	default:
		t.Error("an ended session's done is open")
	}
	s, _ := st.create()
	for range 15 {
		c.add(sessionIdle - time.Hour)
		if !st.touch(s) {
			t.Fatal("a stream lost its session early")
		}
	}
	c.add(sessionIdle - time.Hour)
	if st.touch(s) {
		t.Error("a session lives past 7 days")
	}
	select {
	case <-s.done:
	default:
		t.Error("touch kept an expired session")
	}
}
