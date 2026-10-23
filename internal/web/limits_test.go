package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// serveReal runs a gateway on a real listener; edit adjusts it before it serves.
func serveReal(t *testing.T, th Throne, edit func(s *Server)) (*Server, string) {
	t.Helper()
	s, err := New(Config{Throne: th, ThroneAddr: "127.0.0.1:9095", Listen: "127.0.0.1:0", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	s.t = fastTiming
	if edit != nil {
		edit(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return s, ln.Addr().String()
}

// rawRequest sends text on a new connection and returns the status line of the answer.
func rawRequest(t *testing.T, addr, text string) (string, error) {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, text); err != nil {
		return "", err
	}
	return bufio.NewReader(c).ReadString('\n')
}

func TestBodyLimits(t *testing.T) {
	_, addr := serveReal(t, newScripted(), func(s *Server) { s.t.bodyRead = 200 * time.Millisecond })
	start := time.Now()
	status, err := rawRequest(t, addr, "POST /auth/login HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\n"+
		"Content-Length: 100\r\n\r\n{\"pass")
	if err != nil || !strings.HasPrefix(status, "HTTP/1.1 408") {
		t.Errorf("slow body: %q %v", status, err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 3*time.Second {
		t.Errorf("slow body answered after %v", d)
	}
	start = time.Now()
	status, err = rawRequest(t, addr, "POST /api/v1/import HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 100\r\n\r\n{")
	if err != nil || !strings.HasPrefix(status, "HTTP/1.1 401") || time.Since(start) > 3*time.Second {
		t.Errorf("slow body without a session: %q %v after %v", status, err, time.Since(start))
	}
	big := `{"password":"` + strings.Repeat("a", 5000) + `"}`
	resp, err := http.Post("http://"+addr+"/auth/login", "application/json", strings.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 413 || errorCode(body) != "too_large" {
		t.Errorf("large login body: %d %s", resp.StatusCode, body)
	}
}

// TestBodyDeadlineCleared checks that a call to Throne may outlast the body deadline: a
// deadline left on the connection would cancel the request.
func TestBodyDeadlineCleared(t *testing.T) {
	f := newScripted()
	s, addr := serveReal(t, f, func(s *Server) { s.t.bodyRead = 100 * time.Millisecond })
	sess, token := s.sessions.create()
	go func() {
		c := next(t, f.sends)
		time.Sleep(400 * time.Millisecond)
		c.answer(&throne.Response{Status: 200, Body: json.RawMessage(`{"state":"running"}`)}, nil)
	}()
	req, _ := http.NewRequest("PUT", "http://"+addr+"/api/v1/connection", strings.NewReader(`{"select":"last"}`))
	req.Header.Set("X-CSRF-Token", sess.csrf)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != `{"state":"running"}` {
		t.Errorf("slow Throne call: %d %s", resp.StatusCode, body)
	}
}

func TestConnectionCap(t *testing.T) {
	_, addr := serveReal(t, newScripted(), func(s *Server) { s.conns = connLimits{total: 3, perClient: 2, perSite: 3} })
	get := "GET /auth/state HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
	var held []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	if status, err := rawRequest(t, addr, get); err == nil {
		t.Fatalf("a third connection from one client got %q", status)
	}
	held[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := rawRequest(t, addr, get)
		if err == nil && strings.HasPrefix(status, "HTTP/1.1 200") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no connection after one closed: %q %v", status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	held[1].Close()
}

// TestConnectionEviction checks that a full server closes its longest idle connection
// for a new one.
func TestConnectionEviction(t *testing.T) {
	_, addr := serveReal(t, newScripted(), func(s *Server) { s.conns = connLimits{total: 2, perClient: 10, perSite: 10} })
	var held []net.Conn
	for range 2 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		held = append(held, c)
		time.Sleep(20 * time.Millisecond)
	}
	status, err := rawRequest(t, addr, "GET /auth/state HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")
	if err != nil || !strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Fatalf("a new connection on a full server: %q %v", status, err)
	}
	held[0].SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := held[0].Read(make([]byte, 1)); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("the oldest idle connection stays open: %v", err)
	}
}

// pipeListener hands out the server ends of pipes that claim a remote address.
type pipeListener struct {
	ch chan net.Conn
}

type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

func (l pipeListener) Accept() (net.Conn, error) {
	c, ok := <-l.ch
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (l pipeListener) Close() error   { return nil }
func (l pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

// testListener runs a limitListener on pipes; dial returns the accepted connection, or
// nil when the listener closed it, and the client end.
func testListener(t *testing.T, lim connLimits) (*limitListener, func(ip string) (net.Conn, net.Conn)) {
	pl := pipeListener{make(chan net.Conn)}
	ll := newLimitListener(pl, lim)
	accepted := make(chan net.Conn, 10)
	go func() {
		for {
			c, err := ll.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	t.Cleanup(func() { close(pl.ch) })
	return ll, func(ip string) (net.Conn, net.Conn) {
		server, client := net.Pipe()
		pl.ch <- addrConn{server, &net.TCPAddr{IP: net.ParseIP(ip), Port: 1}}
		select {
		case c := <-accepted:
			return c, client
		case <-time.After(50 * time.Millisecond):
		}
		if !closed(client) {
			t.Errorf("%s: a refused connection stays open", ip)
		}
		return nil, client
	}
}

// closed says whether the other end of a pipe was closed.
func closed(client net.Conn) bool {
	client.SetDeadline(time.Now().Add(time.Second))
	_, err := client.Write([]byte("x"))
	return err != nil && !errors.Is(err, os.ErrDeadlineExceeded)
}

func TestLimitListener(t *testing.T) {
	ll, dial := testListener(t, connLimits{total: 3, perClient: 2, perSite: 3})
	a1, _ := dial("192.0.2.1")
	a2, a2c := dial("192.0.2.1")
	a3, _ := dial("192.0.2.1")
	b1, _ := dial("2001:db8:0:1::1")
	if a1 == nil || a2 == nil || a3 != nil || b1 == nil {
		t.Fatalf("accepted %v %v %v %v", a1, a2, a3, b1)
	}
	for _, c := range []net.Conn{a1, a2, b1} {
		ll.connState(c, http.StateActive)
	}
	if c, _ := dial("198.51.100.1"); c != nil {
		t.Fatal("over the total with every connection busy")
	}
	ll.connState(a2, http.StateIdle)
	if c, _ := dial("198.51.100.1"); c == nil {
		t.Fatal("no room made from an idle connection")
	}
	if !closed(a2c) {
		t.Error("the idle connection was not closed")
	}
	a1.Close()
	a1.Close()
	if c, _ := dial("198.51.100.2"); c == nil {
		t.Error("no room after a close")
	}

	_, dial = testListener(t, connLimits{total: 10, perClient: 10, perSite: 2})
	s1, _ := dial("2001:db8:0:1::1")
	s2, _ := dial("2001:db8:0:2::1")
	s3, _ := dial("2001:db8:0:3::1")
	other, _ := dial("2001:db9::1")
	if s1 == nil || s2 == nil || s3 != nil || other == nil {
		t.Errorf("per /48: %v %v %v %v", s1, s2, s3, other)
	}
	s1.Close()
	if c, _ := dial("2001:db8:0:3::1"); c == nil {
		t.Error("no room in the /48 after a close")
	}
}

// async sends a request in the background.
func async(b *browser, method, path string) chan int {
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(method, b.g.ts.URL+path, nil)
		req.Header.Set("X-CSRF-Token", b.csrf)
		resp, err := b.c.Do(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	return done
}

func TestThroneCallLimit(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	b := g.login()
	var results []chan int
	for range 6 {
		results = append(results, async(b, "GET", "/api/v1/status"))
	}
	var calls []*call[string, *throne.Response]
	for range 4 {
		calls = append(calls, next(t, f.sends))
	}
	select {
	case c := <-f.sends:
		t.Fatalf("a fifth call to Throne: %s", c.q)
	case <-time.After(100 * time.Millisecond):
	}
	ok := &throne.Response{Status: 200, Body: json.RawMessage(`{}`)}
	calls[0].answer(ok, nil)
	calls = append(calls[1:], next(t, f.sends))
	calls[0].answer(ok, nil)
	calls = append(calls[1:], next(t, f.sends))
	for _, c := range calls {
		c.answer(ok, nil)
	}
	for _, r := range results {
		if code := <-r; code != 200 {
			t.Errorf("status %d", code)
		}
	}
	if n := len(g.s.calls); n != 0 {
		t.Errorf("%d slots still taken", n)
	}
}

func TestThroneChangeLimit(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	b := g.login()
	var results []chan int
	for range 4 {
		results = append(results, async(b, "PUT", "/api/v1/modes"))
	}
	var calls []*call[string, *throne.Response]
	for range 3 {
		calls = append(calls, next(t, f.sends))
	}
	select {
	case c := <-f.sends:
		t.Fatalf("a fourth change to Throne: %s", c.q)
	case <-time.After(100 * time.Millisecond):
	}
	results = append(results, async(b, "GET", "/api/v1/status"))
	c := next(t, f.sends)
	if c.q != "GET /v1/status" {
		t.Fatalf("the GET waits behind changes: %s", c.q)
	}
	calls = append(calls, c)
	ok := &throne.Response{Status: 200, Body: json.RawMessage(`{}`)}
	for _, c := range calls {
		c.answer(ok, nil)
	}
	next(t, f.sends).answer(ok, nil)
	for _, r := range results {
		if code := <-r; code != 200 {
			t.Errorf("status %d", code)
		}
	}
	if len(g.s.calls) != 0 || len(g.s.changes) != 0 {
		t.Errorf("slots still taken: %d, %d", len(g.s.calls), len(g.s.changes))
	}
}

func TestProxyDropsTimeout(t *testing.T) {
	g := newGateway(t, nil)
	g.fake.on("GET /v1/logs", respond(`{"instance":"a","cursor":0,"more":false,"dropped":0,"lines":[]}`))
	g.fake.on("POST /v1/import", respond(`{}`))
	b := g.login()
	if resp, body := b.do("GET", "/api/v1/logs?limit=1&timeout=300&instance=a&after=0", "", nil); resp.StatusCode != 200 {
		t.Fatalf("logs: %d %s", resp.StatusCode, body)
	}
	if q := g.fake.last().Query; !reflect.DeepEqual(q, map[string]string{"limit": "1", "instance": "a", "after": "0"}) {
		t.Errorf("GET query %v", q)
	}
	b.do("POST", "/api/v1/import?timeout=5", "{}", nil)
	if q := g.fake.last().Query; q["timeout"] != "5" {
		t.Errorf("POST query %v", q)
	}
}

func TestAnonymousLimit(t *testing.T) {
	_, addr := serveReal(t, newScripted(), func(s *Server) { s.t.bodyRead = time.Second })
	slow := "POST /auth/login HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Length: 100\r\n\r\n{"
	type outcome struct {
		status string
		after  time.Duration
	}
	out := make(chan outcome, 10)
	start := time.Now()
	for range 10 {
		go func() {
			c, err := net.Dial("tcp", addr)
			if err != nil {
				out <- outcome{err.Error(), 0}
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(5 * time.Second))
			io.WriteString(c, slow)
			status, _ := bufio.NewReader(c).ReadString('\n')
			out <- outcome{status, time.Since(start)}
		}()
	}
	n := map[string]int{}
	for range 10 {
		o := <-out
		code := strings.TrimPrefix(o.status, "HTTP/1.1 ")
		code = code[:min(3, len(code))]
		n[code]++
		if code == "429" && o.after > 700*time.Millisecond {
			t.Errorf("429 after %v", o.after)
		}
	}
	if n["429"] != 2 || n["408"] != 8 {
		t.Errorf("outcomes %v", n)
	}
	if status, err := rawRequest(t, addr, "GET /auth/state HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); err != nil ||
		!strings.HasPrefix(status, "HTTP/1.1 200") {
		t.Errorf("after the slow bodies: %q %v", status, err)
	}
}

func TestRejectedAfterAnswers(t *testing.T) {
	f := newScripted()
	g := newGateway(t, func(c *Config) { c.Throne = f })
	b := g.login()
	try := func(err error) (int, string) {
		req, _ := http.NewRequest("GET", g.ts.URL+"/api/v1/status", nil)
		req.Header.Set("X-CSRF-Token", b.csrf)
		done := make(chan struct{})
		var resp *http.Response
		var body []byte
		go func() {
			defer close(done)
			var rerr error
			if resp, rerr = b.c.Do(req); rerr == nil {
				body, _ = io.ReadAll(resp.Body)
				resp.Body.Close()
			}
		}()
		c := next(t, f.sends)
		if err != nil {
			c.answer(nil, err)
		} else {
			c.answer(&throne.Response{Status: 200, Body: json.RawMessage(`{}`)}, nil)
		}
		<-done
		if resp == nil {
			t.Fatal("no response")
		}
		return resp.StatusCode, string(body)
	}
	if code, body := try(throne.ErrRejected); code != 502 || errorCode([]byte(body)) != "throne_rejected" {
		t.Errorf("rejected first: %d %s", code, body)
	}
	try(nil)
	code, body := try(throne.ErrRejected)
	if code != 502 || errorCode([]byte(body)) != "throne_unreachable" || !strings.Contains(body, "may be restarting") {
		t.Errorf("rejected after an answer: %d %s", code, body)
	}
	try(&throne.APIError{Status: 409, Code: "busy"})
	g.clock.add(9 * time.Minute)
	if code, body := try(throne.ErrRejected); errorCode([]byte(body)) != "throne_unreachable" {
		t.Errorf("rejected 9 minutes after an API error: %d %s", code, body)
	}
	g.clock.add(11 * time.Minute)
	if code, body := try(throne.ErrRejected); errorCode([]byte(body)) != "throne_rejected" {
		t.Errorf("rejected long after the last answer: %d %s", code, body)
	}
}

func TestPasswordSetRace(t *testing.T) {
	ps, _, err := openPasswordStore(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(ps.file)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var seen int
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if changed, _ := ps.changed(); changed {
				seen++
			}
		}
	})
	for i := range 5 {
		if err := ps.set(dir, "password number "+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	if seen != 0 {
		t.Errorf("the watcher saw %d of the store's own changes", seen)
	}
	if !ps.current().matches("password number e") {
		t.Error("the last password does not match")
	}
}
