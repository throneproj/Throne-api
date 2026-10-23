package web

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// scripted is a Throne whose answers the test gives one call at a time.
type scripted struct {
	Throne
	events chan *call[throne.EventsQuery, *throne.Events]
	logs   chan *call[throne.LogQuery, *throne.Logs]
	sends  chan *call[string, *throne.Response]
}

type call[Q, R any] struct {
	q   Q
	ctx context.Context
	res chan result[R]
}

type result[R any] struct {
	v   R
	err error
}

func newScripted() *scripted {
	return &scripted{
		events: make(chan *call[throne.EventsQuery, *throne.Events]),
		logs:   make(chan *call[throne.LogQuery, *throne.Logs]),
		sends:  make(chan *call[string, *throne.Response]),
	}
}

func ask[Q, R any](ctx context.Context, ch chan *call[Q, R], q Q) (R, error) {
	c := &call[Q, R]{q: q, ctx: ctx, res: make(chan result[R], 1)}
	var zero R
	select {
	case ch <- c:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	select {
	case r := <-c.res:
		return r.v, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (f *scripted) Events(ctx context.Context, q throne.EventsQuery) (*throne.Events, error) {
	return ask(ctx, f.events, q)
}

func (f *scripted) Logs(ctx context.Context, q throne.LogQuery) (*throne.Logs, error) {
	return ask(ctx, f.logs, q)
}

func (f *scripted) Send(ctx context.Context, method, path string, _ map[string]string, _ any) (*throne.Response, error) {
	return ask(ctx, f.sends, method+" "+path)
}

// next returns the next call on ch.
func next[Q, R any](t *testing.T, ch chan *call[Q, R]) *call[Q, R] {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no call to Throne")
		return nil
	}
}

func (c *call[Q, R]) answer(v R, err error) { c.res <- result[R]{v, err} }

// sse is an open event stream.
type sse struct {
	t      *testing.T
	events chan [2]string
	cancel context.CancelFunc
}

func (b *browser) stream(path string) *sse {
	b.g.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", b.g.ts.URL+path+"?token="+url.QueryEscape(b.csrf), nil)
	resp, err := b.c.Do(req)
	if err != nil {
		b.g.t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("Cache-Control") != "no-store" {
		b.g.t.Fatalf("stream %s: %d %v", path, resp.StatusCode, resp.Header)
	}
	s := &sse{t: b.g.t, events: make(chan [2]string, 100), cancel: cancel}
	b.g.t.Cleanup(cancel)
	go func() {
		defer close(s.events)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var name, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if name != "" {
					s.events <- [2]string{name, data}
				}
				name, data = "", ""
			case strings.HasPrefix(line, "event: "):
				name = line[7:]
			case strings.HasPrefix(line, "data: "):
				data = line[6:]
			}
		}
	}()
	return s
}

func (s *sse) expect(name, data string) {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			s.t.Fatalf("stream ended, want %s %s", name, data)
		}
		if ev[0] != name || (data != "" && ev[1] != data) {
			s.t.Fatalf("event %s %s, want %s %s", ev[0], ev[1], name, data)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatalf("no event, want %s %s", name, data)
	}
}

func (s *sse) expectEnd() {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if ok {
			s.t.Fatalf("event %s %s, want the end", ev[0], ev[1])
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("the stream goes on")
	}
}

func (s *sse) expectQuiet() {
	s.t.Helper()
	select {
	case ev := <-s.events:
		s.t.Fatalf("event %s %s, want none", ev[0], ev[1])
	case <-time.After(50 * time.Millisecond):
	}
}

const everyTopic = `{"changes":["status","groups","profiles","tests","settings"]}`

func TestEventsHub(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	b := g.login()
	if resp, _ := g.browser().do("GET", "/api/events", "", nil); resp.StatusCode != 401 {
		t.Fatalf("events without a session: %d", resp.StatusCode)
	}
	st := b.stream("/api/events")
	st.expect("changes", everyTopic)
	c := next(t, f.events)
	if c.q != (throne.EventsQuery{Wait: time.Second}) {
		t.Errorf("first query %+v", c.q)
	}
	c.answer(&throne.Events{Instance: "a", Cursor: 5, Changes: allTopics}, nil)
	c = next(t, f.events)
	if c.q != (throne.EventsQuery{Instance: "a", After: 5, Wait: time.Second}) {
		t.Errorf("second query %+v", c.q)
	}
	st.expectQuiet()
	c.answer(&throne.Events{Instance: "a", Cursor: 6, Changes: []string{"profiles", "tests"}}, nil)
	st.expect("changes", `{"changes":["profiles","tests"]}`)

	st2 := b.stream("/api/events")
	st2.expect("changes", everyTopic)
	refused := errorString("throne: dial tcp 127.0.0.1:9: connect: connection refused")
	c = next(t, f.events)
	c.answer(nil, refused)
	down := `{"reachable":false,"code":"throne_unreachable","message":"Throne at ` + g.s.cfg.ThroneAddr +
		` is not reachable: dial tcp 127.0.0.1:9: connect: connection refused"}`
	st.expect("throne", down)
	st2.expect("throne", down)
	st3 := b.stream("/api/events")
	st3.expect("changes", everyTopic)
	st3.expect("throne", down)
	c = next(t, f.events)
	if c.q.Instance != "" {
		t.Errorf("query after a failure %+v", c.q)
	}
	c.answer(nil, refused)
	c = next(t, f.events)
	st.expectQuiet()
	c.answer(&throne.Events{Instance: "b", Cursor: 1, Changes: allTopics}, nil)
	for _, s := range []*sse{st, st2, st3} {
		s.expect("throne", `{"reachable":true}`)
		s.expect("changes", everyTopic)
	}
	st3.cancel()

	c = next(t, f.events)
	c.answer(nil, &throne.APIError{Status: 404, Code: "not_found", Message: "Unknown path"})
	s := next(t, f.sends)
	if s.q != "GET /v1/status" {
		t.Fatalf("legacy poll %s", s.q)
	}
	s.answer(&throne.Response{Status: 200, Body: json.RawMessage(`{"state":"idle"}`)}, nil)
	next(t, f.sends).answer(&throne.Response{Status: 200, Body: json.RawMessage(`{"state":"idle"}`)}, nil)
	st.expectQuiet()
	next(t, f.sends).answer(&throne.Response{Status: 200, Body: json.RawMessage(`{"state":"running"}`)}, nil)
	st.expect("changes", `{"changes":["status"]}`)
	st2.expect("changes", `{"changes":["status"]}`)
	next(t, f.sends).answer(nil, &throne.APIError{Status: 503, Code: "restarting", Message: "Throne restarts"})
	st.expect("throne", `{"reachable":false,"code":"restarting","message":"Throne restarts"}`)
	c = next(t, f.events)
	c.answer(&throne.Events{Instance: "c", Cursor: 0, Changes: allTopics}, nil)
	st.expect("throne", `{"reachable":true}`)
	st.expect("changes", everyTopic)

	c = next(t, f.events)
	st.cancel()
	st2.cancel()
	select {
	case <-c.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the hub follows Throne without streams")
	}

	st4 := b.stream("/api/events")
	st4.expect("changes", everyTopic)
	if c := next(t, f.events); c.q.Instance != "" {
		t.Errorf("a new hub starts at %+v", c.q)
	}
	b.do("POST", "/auth/logout", "", nil)
	st4.expectEnd()
}

func TestEventsLegacyRefresh(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	g.s.t.fullRefresh = 50 * time.Millisecond
	st := g.login().stream("/api/events")
	st.expect("changes", everyTopic)
	next(t, f.events).answer(nil, &throne.APIError{Status: 404, Code: "not_found"})
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case c := <-f.sends:
				c.answer(&throne.Response{Status: 200, Body: json.RawMessage(`{}`)}, nil)
			case <-done:
				return
			}
		}
	}()
	st.expect("changes", everyTopic)
}

func line(seq int64, text string) throne.LogLine {
	return throne.LogLine{Seq: seq, Time: 1760000000 + seq, Text: text}
}

func TestLogsStream(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	b := g.login()
	st := b.stream("/api/logs")
	c := next(t, f.logs)
	if c.q != (throne.LogQuery{Limit: 500}) {
		t.Errorf("backlog query %+v", c.q)
	}
	c.answer(&throne.Logs{Instance: "a", Cursor: 2, Lines: []throne.LogLine{line(1, "one"), line(2, "two")}}, nil)
	backlog := `{"lines":[{"seq":1,"time":1760000001,"text":"one"},{"seq":2,"time":1760000002,"text":"two"}],"dropped":0}`
	st.expect("lines", backlog)
	c = next(t, f.logs)
	if c.q != (throne.LogQuery{Instance: "a", After: 2, Limit: 500, Wait: time.Second}) {
		t.Errorf("follow query %+v", c.q)
	}
	st2 := b.stream("/api/logs")
	st2.expect("lines", backlog)
	select {
	case extra := <-f.logs:
		t.Fatalf("a second stream made another call: %+v", extra.q)
	case <-time.After(50 * time.Millisecond):
	}
	c.answer(&throne.Logs{Instance: "a", Cursor: 7, More: true, Dropped: 4, Lines: []throne.LogLine{line(7, "seven")}}, nil)
	for _, s := range []*sse{st, st2} {
		s.expect("lines", `{"lines":[{"seq":7,"time":1760000007,"text":"seven"}],"dropped":4}`)
	}
	c = next(t, f.logs)
	if c.q.Wait != 0 || c.q.After != 7 {
		t.Errorf("query after a cut page %+v", c.q)
	}
	c.answer(&throne.Logs{Instance: "a", Cursor: 7}, nil)
	c = next(t, f.logs)
	st.expectQuiet()
	c.answer(&throne.Logs{Instance: "b", Cursor: 1, Lines: []throne.LogLine{line(1, "fresh")}}, nil)
	for _, s := range []*sse{st, st2} {
		s.expect("reset", `{}`)
		s.expect("lines", `{"lines":[{"seq":1,"time":1760000001,"text":"fresh"}],"dropped":0}`)
	}
	next(t, f.logs).answer(nil, errorString("throne: connection lost: EOF"))
	down := `{"reachable":false,"code":"throne_unreachable","message":"Throne at ` + g.s.cfg.ThroneAddr +
		` is not reachable: connection lost: EOF"}`
	st.expect("throne", down)
	st2.expect("throne", down)
	st3 := b.stream("/api/logs")
	st3.expect("lines", `{"lines":[{"seq":1,"time":1760000001,"text":"fresh"}],"dropped":0}`)
	st3.expect("throne", down)
	st3.cancel()
	c = next(t, f.logs)
	if c.q.Instance != "b" || c.q.After != 1 {
		t.Errorf("query after a failure %+v", c.q)
	}
	c.answer(&throne.Logs{Instance: "b", Cursor: 1}, nil)
	st.expect("throne", `{"reachable":true}`)
	next(t, f.logs).answer(nil, &throne.APIError{Status: 503, Code: "restarting", Message: "Throne restarts"})
	st.expect("throne", `{"reachable":false,"code":"restarting","message":"Throne restarts"}`)
	next(t, f.logs).answer(nil, &throne.APIError{Status: 403, Code: "forbidden", Message: "Admin requests are refused"})
	for _, s := range []*sse{st, st2} {
		if s == st2 {
			s.expect("throne", `{"reachable":true}`)
			s.expect("throne", "")
		}
		s.expect("error", `{"code":"forbidden","message":"Admin requests are refused"}`)
		s.expectEnd()
	}

	st4 := b.stream("/api/logs")
	if c := next(t, f.logs); c.q != (throne.LogQuery{Limit: 500}) {
		t.Errorf("a new follower starts at %+v", c.q)
	} else {
		c.answer(nil, &throne.APIError{Status: 404, Code: "not_found", Message: "Unknown path"})
	}
	st4.expect("error", `{"code":"not_found","message":"Unknown path"}`)
	st4.expectEnd()
}

func TestLogQueueMerges(t *testing.T) {
	sub := &logSub{wake: make(chan struct{}, 1)}
	many := make([]throne.LogLine, 400)
	for i := range many {
		many[i] = line(int64(i+1), "x")
	}
	sub.push("lines", logLines{many, 3})
	sub.push("lines", logLines{many[:200], 1})
	sub.push("reset", struct{}{})
	sub.push("lines", logLines{many[:1], 0})
	q, end := sub.take()
	if end || len(q) != 3 || q[1].name != "reset" {
		t.Fatalf("queue %d events, end %v", len(q), end)
	}
	if got := q[0].data.(logLines); len(got.Lines) != 500 || got.Dropped != 104 || got.Lines[0].Seq != 101 || got.Lines[499].Seq != 200 {
		t.Errorf("merged %d lines from %d, dropped %d", len(got.Lines), got.Lines[0].Seq, got.Dropped)
	}
	if len(many) != 400 || many[0].Seq != 1 {
		t.Error("merging changed the pushed lines")
	}
}

func TestStreamEndsWithSession(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	g.s.t.ping = 20 * time.Millisecond
	st := g.login().stream("/api/events")
	st.expect("changes", everyTopic)
	next(t, f.events)
	g.clock.add(sessionIdle - time.Hour)
	st.expectQuiet()
	g.clock.add(8 * 24 * time.Hour)
	st.expectEnd()
}

func TestServeTLS(t *testing.T) {
	f := newScripted()
	dir := t.TempDir()
	cfg := Config{Throne: f, ThroneAddr: "127.0.0.1:9095", Listen: "127.0.0.1:0", StateDir: dir, TLS: true}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.t = fastTiming
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()
	var once sync.Once
	var serveErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			serveErr = <-served
		})
		return serveErr
	}
	t.Cleanup(func() { stop() })

	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}
	client := &http.Client{Transport: tr}
	url := "https://localhost:" + strings.Split(ln.Addr().String(), ":")[1]
	resp, err := client.Get(url + "/auth/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	leaf := resp.TLS.PeerCertificates[0]
	if fingerprint(leaf.Raw) != s.Fingerprint() || leaf.Subject.CommonName != "Throne web UI" || resp.ProtoMajor != 2 {
		t.Errorf("certificate %s for %v, %s", leaf.Subject, leaf.DNSNames, resp.Proto)
	}
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Error(err)
	}
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Error(err)
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) < 9*365*24*time.Hour || leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Errorf("validity %v, key %v", leaf.NotAfter.Sub(leaf.NotBefore), leaf.PublicKeyAlgorithm)
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{"cert.pem", "key.pem"} {
			if fi, err := os.Stat(filepath.Join(dir, "tls", name)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v %v", name, fi.Mode(), err)
			}
		}
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("state directory: %v %v", fi.Mode(), err)
		}
	}
	again, err := New(cfg)
	if err != nil || again.Fingerprint() != s.Fingerprint() {
		t.Errorf("the certificate was not reused: %v", err)
	}

	pw, _ := json.Marshal(map[string]string{"password": s.created})
	resp, err = client.Post(url+"/auth/login", "application/json", strings.NewReader(string(pw)))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", resp, err)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	req, _ := http.NewRequest("GET", url+"/api/events", nil)
	req.Header.Set("X-CSRF-Token", login.CSRF)
	for _, c := range resp.Cookies() {
		req.AddCookie(c)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	next(t, f.events)
	start := time.Now()
	if err := stop(); err != nil {
		t.Errorf("Serve: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("shutdown took %v", d)
	}
}

func TestServeURLs(t *testing.T) {
	if got := serveURLs(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9096}, false); len(got) != 1 || got[0] != "http://127.0.0.1:9096/" {
		t.Errorf("loopback %v", got)
	}
	if got := serveURLs(&net.TCPAddr{IP: net.IPv6loopback, Port: 9096}, true); len(got) != 1 || got[0] != "https://[::1]:9096/" {
		t.Errorf("IPv6 loopback %v", got)
	}
	got := serveURLs(&net.TCPAddr{IP: net.IPv6unspecified, Port: 9096}, true)
	if got[0] != "https://localhost:9096/" {
		t.Errorf("every address %v", got)
	}
	for _, u := range got[1:] {
		if strings.Contains(u, "127.0.0.1") || strings.Contains(u, "[::1]") {
			t.Errorf("listed a loopback address: %v", got)
		}
	}
}
