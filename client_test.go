package throne_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/testserver"
	"github.com/throneproj/Throne-api/internal/wire"
)

// Keys from spec/vectors/v1.json.
const (
	testKey  = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	otherKey = "bljyVf7GZBkFc4ehd35siy7LduS9QD-st76wDYoRFsE"
)

const statusJSON = `{"state":"running","profile":{"id":0,"group":0,"name":"DE Frankfurt","type":"vless",` +
	`"address":"de1.example.com:443","latency_ms":85,"latency_at":1760000000,"download_bps":48300000,` +
	`"upload_bps":9100000,"country":"DE","ip_out":"203.0.113.7","traffic_up":1048576,` +
	`"traffic_down":73400320,"running":true},"last_profile_id":0,"tun":true,"system_proxy":false,` +
	`"route":{"id":1,"name":"Default","active":true},"kill_switch":"off","core":true,` +
	`"inbound":{"enabled":true,"address":"127.0.0.1","port":2080},"version":"1.4.0",` +
	`"capabilities":{"tun":true,"kill_switch":false,"headless":true},"restart_needed":true,"degraded":null}`

func mustKey(t *testing.T, text string) throne.Key {
	t.Helper()
	k, err := throne.ParseKey(text)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// recorder keeps the requests it sees and answers them with answer.
type recorder struct {
	mu     sync.Mutex
	reqs   []*testserver.Request
	answer func(r *testserver.Request) *testserver.Response
}

func (rec *recorder) handle(_ context.Context, r *testserver.Request) *testserver.Response {
	rec.mu.Lock()
	rec.reqs = append(rec.reqs, r)
	rec.mu.Unlock()
	return rec.answer(r)
}

func (rec *recorder) requests() []*testserver.Request {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]*testserver.Request(nil), rec.reqs...)
}

func (rec *recorder) last(t *testing.T) *testserver.Request {
	t.Helper()
	reqs := rec.requests()
	if len(reqs) == 0 {
		t.Fatal("the server saw no request")
	}
	return reqs[len(reqs)-1]
}

func always(resp *testserver.Response) func(*testserver.Request) *testserver.Response {
	return func(*testserver.Request) *testserver.Response { return resp }
}

func serve(t *testing.T, h testserver.Handler) (*throne.Client, *testserver.Server) {
	t.Helper()
	srv := testserver.Start(t, testserver.Config{Key: testKey, Handler: h})
	return throne.New(srv.Addr, mustKey(t, testKey)), srv
}

// block holds a request until the server closes.
func block(ctx context.Context, _ *testserver.Request) *testserver.Response {
	<-ctx.Done()
	return nil
}

func jsonEqual(t *testing.T, what string, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil && len(got) > 0 {
		t.Fatalf("%s: %v", what, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil && want != "" {
		t.Fatalf("%s: bad want: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %s, want %s", what, got, want)
	}
}

func TestParseKey(t *testing.T) {
	for i, text := range []string{testKey, " " + testKey + "\n", "\t" + testKey + "\r\n"} {
		if _, err := throne.ParseKey(text); err != nil {
			t.Errorf("valid key %d: %v", i, err)
		}
	}
	invalid := []string{
		"",
		testKey[:42],
		testKey + "A",
		testKey + "=",
		testKey[:42] + "9", // non-zero padding bits
		"+" + testKey[1:],  // standard, not URL, alphabet
		testKey[:20] + " " + testKey[21:],
		base64.RawURLEncoding.EncodeToString(make([]byte, 31)),
		base64.RawURLEncoding.EncodeToString(make([]byte, 33)),
	}
	for i, text := range invalid {
		_, err := throne.ParseKey(text)
		if err == nil {
			t.Errorf("invalid key %d accepted", i)
			continue
		}
		if len(text) > 8 && strings.Contains(err.Error(), strings.TrimSpace(text)) {
			t.Errorf("invalid key %d: the error repeats the key", i)
		}
	}
}

func TestKeyIsNeverPrinted(t *testing.T) {
	k := mustKey(t, testKey)
	raw, _ := base64.RawURLEncoding.DecodeString(testKey)
	secrets := []string{testKey, hex.EncodeToString(raw), strings.ToUpper(hex.EncodeToString(raw)),
		string(raw), fmt.Sprint(raw[:8])[1:15]}
	client := throne.New("127.0.0.1:1", k)
	values := []any{k, &k, []throne.Key{k}, struct{ k throne.Key }{k}, struct{ K throne.Key }{k}, client, *client}
	for _, v := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d"} {
			out := fmt.Sprintf(verb, v)
			for _, s := range secrets {
				if strings.Contains(out, s) {
					t.Fatalf("%s of %T reveals the key", verb, v)
				}
			}
		}
	}
	b, err := json.Marshal(k)
	if err != nil || string(b) != "{}" {
		t.Fatalf("json.Marshal(key) = %s, %v", b, err)
	}
}

func TestReadKeyFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "key")
	if err := os.WriteFile(good, []byte("\xEF\xBB\xBF"+testKey+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := throne.ReadKeyFile(good); err != nil {
		t.Fatalf("key file with a BOM and CRLF: %v", err)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("not-a-key-but-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := throne.ReadKeyFile(bad)
	if err == nil || !strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid key file: err = %v", err)
	}
	if _, err := throne.ReadKeyFile(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key file: err = %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	rec := &recorder{answer: always(testserver.OK(json.RawMessage(statusJSON)))}
	srv := testserver.Start(t, testserver.Config{Key: testKey, Handler: rec.handle})
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("THRONE_ADDR", srv.Addr)
	t.Setenv("THRONE_KEY", testKey)
	t.Setenv("THRONE_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	for _, step := range []string{"THRONE_KEY", "THRONE_KEY_FILE"} {
		if step == "THRONE_KEY_FILE" {
			t.Setenv("THRONE_KEY", "")
			t.Setenv("THRONE_KEY_FILE", keyFile)
		}
		c, err := throne.FromEnv()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if _, err := c.Status(t.Context()); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}

	t.Setenv("THRONE_KEY", "")
	t.Setenv("THRONE_KEY_FILE", "")
	if _, err := throne.FromEnv(); err == nil || !strings.Contains(err.Error(), "THRONE_KEY") {
		t.Fatalf("no key: err = %v", err)
	}
	t.Setenv("THRONE_KEY", "secret-but-invalid")
	if _, err := throne.FromEnv(); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("invalid THRONE_KEY: err = %v", err)
	}
}

func TestStatus(t *testing.T) {
	rec := &recorder{answer: always(testserver.OK(json.RawMessage(statusJSON)))}
	c, _ := serve(t, rec.handle)
	st, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := &throne.Status{
		State: throne.StateRunning,
		Profile: &throne.Profile{ID: 0, Group: 0, Name: "DE Frankfurt", Type: "vless",
			Address: "de1.example.com:443", LatencyMS: 85, LatencyAt: 1760000000, DownloadBPS: 48300000,
			UploadBPS: 9100000, Country: "DE", IPOut: "203.0.113.7", TrafficUp: 1048576,
			TrafficDown: 73400320, Running: true},
		Tun:           true,
		Route:         throne.Route{ID: 1, Name: "Default", Active: true},
		KillSwitch:    "off",
		Core:          true,
		Inbound:       throne.Inbound{Enabled: true, Address: "127.0.0.1", Port: 2080},
		Version:       "1.4.0",
		Capabilities:  throne.Capabilities{Tun: true, Headless: true},
		RestartNeeded: true,
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("Status = %+v, want %+v", st, want)
	}
	r := rec.last(t)
	if r.Method != "GET" || r.Path != "/v1/status" || r.Query != nil || r.Body != nil {
		t.Fatalf("request = %+v", r)
	}
}

func TestAPIError(t *testing.T) {
	c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response {
		return testserver.Error(409, "busy", "Another profile is starting...")
	})
	_, err := c.Connect(t.Context(), 12)
	var apiErr *throne.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an *APIError", err)
	}
	if apiErr.Status != 409 || apiErr.Code != throne.CodeBusy || apiErr.Message != "Another profile is starting..." {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if code := throne.ErrorCode(fmt.Errorf("wrapped: %w", err)); code != throne.CodeBusy {
		t.Fatalf("ErrorCode = %q", code)
	}
	if code := throne.ErrorCode(errors.New("other")); code != "" {
		t.Fatalf("ErrorCode of another error = %q", code)
	}
}

func TestAccepted(t *testing.T) {
	c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response {
		return testserver.Accepted(map[string]any{"state": "connecting", "profile": nil, "last_profile_id": 12})
	})
	st, err := c.Connect(t.Context(), 12)
	if err != nil {
		t.Fatalf("202: %v", err)
	}
	if st.State != throne.StateConnecting || st.LastProfileID != 12 || st.Profile != nil {
		t.Fatalf("Status = %+v", st)
	}
}

func TestRejected(t *testing.T) {
	t.Run("wrong key", func(t *testing.T) {
		rec := &recorder{answer: always(testserver.OK(json.RawMessage(statusJSON)))}
		srv := testserver.Start(t, testserver.Config{Key: testKey, Handler: rec.handle})
		_, err := throne.New(srv.Addr, mustKey(t, otherKey)).Status(t.Context())
		if !errors.Is(err, throne.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
		if len(rec.requests()) != 0 {
			t.Fatal("the server answered a request with the wrong key")
		}
	})
	t.Run("closed after the request", func(t *testing.T) {
		c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response { return nil })
		if _, err := c.Status(t.Context()); !errors.Is(err, throne.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	})
	t.Run("closed at once", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			for {
				nc, err := ln.Accept()
				if err != nil {
					return
				}
				nc.Close()
			}
		}()
		_, err = throne.New(ln.Addr().String(), mustKey(t, testKey)).Status(t.Context())
		if !errors.Is(err, throne.ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	})
}

func TestBadResponseFrames(t *testing.T) {
	header := func(n uint32) []byte { return binary.BigEndian.AppendUint32(nil, n) }
	tests := []struct {
		name string
		resp *testserver.Response
		want error
	}{
		{"length above the limit", &testserver.Response{Raw: header(wire.MaxServerLength + 1)}, wire.ErrLength},
		{"length below 16", &testserver.Response{Raw: append(header(15), make([]byte, 15)...)}, wire.ErrLength},
		{"not sealed", &testserver.Response{Raw: append(header(32), make([]byte, 32)...)}, wire.ErrAuth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response { return tt.resp })
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			start := time.Now()
			_, err := c.Status(ctx)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if errors.Is(err, throne.ErrRejected) {
				t.Fatal("a bad frame reads as a rejected key")
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Fatalf("took %v: the client waited for the body of a bad frame", d)
			}
		})
	}
}

func TestMalformedResponse(t *testing.T) {
	for _, plaintext := range []string{"not json", `{"body":{}}`, `{"status":302,"body":null}`} {
		c, _ := serve(t, func(context.Context, *testserver.Request) *testserver.Response {
			return &testserver.Response{Plaintext: []byte(plaintext)}
		})
		_, err := c.Status(t.Context())
		var apiErr *throne.APIError
		if err == nil || errors.As(err, &apiErr) || errors.Is(err, throne.ErrRejected) {
			t.Errorf("response %q: err = %v", plaintext, err)
		}
	}
}

func TestRequestTooLarge(t *testing.T) {
	c, srv := serve(t, func(context.Context, *testserver.Request) *testserver.Response {
		return testserver.OK(nil)
	})
	body := strings.Repeat("x", wire.MaxClientLength)
	err := c.Call(t.Context(), "PUT", "/v1/anything", nil, body, nil)
	if !errors.Is(err, wire.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if srv.Accepted() != 0 {
		t.Fatal("the client connected for a request it cannot send")
	}
	if err := c.Call(t.Context(), "PUT", "/v1/anything", nil, strings.Repeat("x", 1000), nil); err != nil {
		t.Fatalf("small body: %v", err)
	}
}

func TestSession(t *testing.T) {
	rec := &recorder{answer: func(r *testserver.Request) *testserver.Response {
		if r.Path == "/v1/profiles/99" {
			return testserver.Error(404, "not_found", "No such profile")
		}
		return testserver.OK(json.RawMessage(statusJSON))
	}}
	c, srv := serve(t, rec.handle)
	s, err := c.Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := s.Status(t.Context()); err != nil {
			t.Fatalf("exchange %d: %v", i+1, err)
		}
	}
	if _, err := s.Profile(t.Context(), 99); throne.ErrorCode(err) != throne.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if _, err := s.Status(t.Context()); err != nil {
		t.Fatalf("after an API error: %v", err)
	}
	if n := srv.Accepted(); n != 1 {
		t.Fatalf("%d connections, want 1", n)
	}
	if n := len(rec.requests()); n != 4 {
		t.Fatalf("%d requests, want 4", n)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Status(t.Context()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("after Close: err = %v, want net.ErrClosed", err)
	}
}

func TestSessionClosedByServer(t *testing.T) {
	srv := testserver.Start(t, testserver.Config{
		Key:  testKey,
		Idle: 200 * time.Millisecond,
		Handler: func(context.Context, *testserver.Request) *testserver.Response {
			return testserver.OK(json.RawMessage(statusJSON))
		},
	})
	s, err := throne.New(srv.Addr, mustKey(t, testKey)).Dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	_, err = s.Status(t.Context())
	if err == nil || errors.Is(err, throne.ErrRejected) {
		t.Fatalf("after the server closed an answered connection: err = %v", err)
	}
	if _, err := s.Status(t.Context()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("after a failed call: err = %v, want net.ErrClosed", err)
	}
}

func TestContext(t *testing.T) {
	c, _ := serve(t, block)
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		time.AfterFunc(100*time.Millisecond, cancel)
		start := time.Now()
		_, err := c.Status(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("returned after %v", d)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := c.Connect(ctx, 12)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("returned after %v", d)
		}
	})
	t.Run("cancelled before the call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := c.Status(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("session", func(t *testing.T) {
		s, err := c.Dial(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		if _, err := s.Status(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if _, err := s.Status(t.Context()); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("after a timeout: err = %v, want net.ErrClosed", err)
		}
	})
}

func TestDialError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = throne.New(addr, mustKey(t, testKey)).Status(t.Context())
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" || errors.Is(err, throne.ErrRejected) {
		t.Fatalf("err = %v, want a dial error", err)
	}
}

func TestWaitFields(t *testing.T) {
	rec := &recorder{answer: always(testserver.OK(json.RawMessage(statusJSON)))}
	c, _ := serve(t, rec.handle)
	tests := []struct {
		name    string
		timeout time.Duration
		opts    []throne.Option
		check   func(m map[string]any) bool
	}{
		{"no deadline", 0, nil, func(m map[string]any) bool {
			_, hasTimeout := m["timeout"]
			_, hasWait := m["wait"]
			return !hasTimeout && !hasWait
		}},
		{"20s deadline", 20 * time.Second, nil, func(m map[string]any) bool {
			t, _ := m["timeout"].(float64)
			return t >= 16 && t <= 18
		}},
		{"hour deadline", time.Hour, nil, func(m map[string]any) bool { return m["timeout"] == 300.0 }},
		{"short deadline", time.Second, nil, func(m map[string]any) bool { return m["timeout"] == 1.0 }},
		{"no wait", 20 * time.Second, []throne.Option{throne.NoWait()}, func(m map[string]any) bool {
			_, hasTimeout := m["timeout"]
			return m["wait"] == false && !hasTimeout
		}},
	}
	for _, tt := range tests {
		ctx := t.Context()
		if tt.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, tt.timeout)
			defer cancel()
		}
		if _, err := c.Connect(ctx, 0, tt.opts...); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if m := rec.last(t).Fields(); !tt.check(m) || m["profile"] != 0.0 {
			t.Errorf("%s: body %s", tt.name, rec.last(t).Body)
		}
	}
}

func TestRequests(t *testing.T) {
	rec := &recorder{answer: func(r *testserver.Request) *testserver.Response {
		switch r.Path {
		case "/v1/results":
			return testserver.OK(map[string]int{"cleared": 2})
		case "/v1/profiles":
			return testserver.OK(json.RawMessage(`{"profiles":[{"id":0,"name":"zero"}]}`))
		}
		return testserver.OK(json.RawMessage(`{}`))
	}}
	c, _ := serve(t, rec.handle)
	on, off := true, false
	from, to := time.Unix(1759913600, 0), time.Unix(1760000000, 0)
	tests := []struct {
		name   string
		call   func(ctx context.Context) error
		method string
		path   string
		query  map[string]string
		body   string
	}{
		{"status", func(ctx context.Context) error { _, err := c.Status(ctx); return err },
			"GET", "/v1/status", nil, ""},
		{"groups", func(ctx context.Context) error { _, err := c.Groups(ctx); return err },
			"GET", "/v1/groups", nil, ""},
		{"all profiles", func(ctx context.Context) error { _, err := c.Profiles(ctx, throne.Selection{}); return err },
			"GET", "/v1/profiles", nil, ""},
		{"profiles of group 0", func(ctx context.Context) error {
			ps, err := c.Profiles(ctx, throne.SelectGroup(0, "DE|NL"))
			if err == nil && (len(ps) != 1 || ps[0].Name != "zero") {
				err = fmt.Errorf("profiles = %+v", ps)
			}
			return err
		}, "GET", "/v1/profiles", map[string]string{"group": "0", "name": "DE|NL"}, ""},
		{"profiles by id", func(ctx context.Context) error { _, err := c.Profiles(ctx, throne.SelectProfiles(0, 15)); return err },
			"GET", "/v1/profiles", map[string]string{"ids": "0,15"}, ""},
		{"profile 0", func(ctx context.Context) error { _, err := c.Profile(ctx, 0); return err },
			"GET", "/v1/profiles/0", nil, ""},
		{"connect", func(ctx context.Context) error { _, err := c.Connect(ctx, 0, throne.Restart()); return err },
			"PUT", "/v1/connection", nil, `{"profile":0,"restart":true}`},
		{"connect last", func(ctx context.Context) error { _, err := c.ConnectLast(ctx, throne.NoWait()); return err },
			"PUT", "/v1/connection", nil, `{"select":"last","wait":false}`},
		{"connect lowest latency", func(ctx context.Context) error {
			_, err := c.ConnectLowestLatency(ctx, throne.SelectGroup(0, "DE"), throne.MaxAge(0))
			return err
		}, "PUT", "/v1/connection", nil, `{"select":"lowest_latency","group":0,"name":"DE","max_age":0}`},
		{"connect lowest latency in the shown group", func(ctx context.Context) error {
			_, err := c.ConnectLowestLatency(ctx, throne.Selection{}, throne.MaxAge(90*time.Minute))
			return err
		}, "PUT", "/v1/connection", nil, `{"select":"lowest_latency","max_age":5400}`},
		{"disconnect", func(ctx context.Context) error { _, err := c.Disconnect(ctx); return err },
			"DELETE", "/v1/connection", nil, ""},
		{"tun on", func(ctx context.Context) error { _, err := c.SetModes(ctx, throne.Modes{Tun: &on}); return err },
			"PUT", "/v1/modes", nil, `{"tun":true}`},
		{"both modes", func(ctx context.Context) error {
			_, err := c.SetModes(ctx, throne.Modes{Tun: &off, SystemProxy: &on})
			return err
		}, "PUT", "/v1/modes", nil, `{"tun":false,"system_proxy":true}`},
		{"routes", func(ctx context.Context) error { _, err := c.Routes(ctx); return err },
			"GET", "/v1/routes", nil, ""},
		{"route 0", func(ctx context.Context) error { _, err := c.SetRoute(ctx, 0); return err },
			"PUT", "/v1/routes/active", nil, `{"id":0}`},
		{"start test", func(ctx context.Context) error {
			_, err := c.StartTest(ctx, throne.TestSpeed, throne.SelectGroup(0, ""))
			return err
		}, "POST", "/v1/tests", nil, `{"kind":"speed","group":0}`},
		{"jobs", func(ctx context.Context) error { _, err := c.Jobs(ctx); return err },
			"GET", "/v1/tests", nil, ""},
		{"job", func(ctx context.Context) error { _, err := c.Job(ctx, 0); return err },
			"GET", "/v1/tests/0", nil, ""},
		{"cancel job", func(ctx context.Context) error { _, err := c.CancelJob(ctx, 7); return err },
			"DELETE", "/v1/tests/7", nil, ""},
		{"clear results", func(ctx context.Context) error {
			n, err := c.ClearResults(ctx, throne.SelectProfiles(0, 3))
			if err == nil && n != 2 {
				err = fmt.Errorf("cleared %d, want 2", n)
			}
			return err
		}, "DELETE", "/v1/results", nil, `{"profiles":[0,3]}`},
		{"live", func(ctx context.Context) error { _, err := c.LiveStats(ctx); return err },
			"GET", "/v1/stats/live", nil, ""},
		{"traffic", func(ctx context.Context) error {
			_, err := c.Traffic(ctx, throne.TrafficQuery{By: throne.TrafficByApp, From: from, To: to})
			return err
		}, "GET", "/v1/stats/traffic", map[string]string{"by": "app", "from": "1759913600", "to": "1760000000"}, ""},
		{"call", func(ctx context.Context) error {
			var out json.RawMessage
			err := c.Call(ctx, "POST", "/v1/custom", map[string]string{"a": "1"}, json.RawMessage(`{"x":[1,2]}`), &out)
			if err == nil && string(out) != "{}" {
				err = fmt.Errorf("body = %s", out)
			}
			return err
		}, "POST", "/v1/custom", map[string]string{"a": "1"}, `{"x":[1,2]}`},
	}
	for _, tt := range tests {
		if err := tt.call(context.Background()); err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		r := rec.last(t)
		if r.Method != tt.method || r.Path != tt.path || !reflect.DeepEqual(r.Query, tt.query) {
			t.Errorf("%s: request %s %s %v, want %s %s %v", tt.name, r.Method, r.Path, r.Query, tt.method, tt.path, tt.query)
		}
		jsonEqual(t, tt.name+": body", r.Body, tt.body)
	}

	n := len(rec.requests())
	if ps, err := c.Profiles(t.Context(), throne.SelectProfiles()); err != nil || len(ps) != 0 {
		t.Errorf("no IDs: %v, %v", ps, err)
	}
	if _, err := c.ConnectLowestLatency(t.Context(), throne.SelectProfiles(1)); err == nil {
		t.Error("ConnectLowestLatency accepted IDs")
	}
	for _, path := range []string{"v1/status", "/v1/profiles?group=1", ""} {
		if err := c.Call(t.Context(), "GET", path, nil, nil, nil); err == nil {
			t.Errorf("Call accepted the path %q", path)
		}
	}
	if len(rec.requests()) != n {
		t.Error("a request that should fail locally reached the server")
	}
}

func TestRunTest(t *testing.T) {
	defer throne.SetPollInterval(10 * time.Millisecond)()
	var mu sync.Mutex
	polls := 0
	rec := &recorder{answer: func(r *testserver.Request) *testserver.Response {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			return testserver.Accepted(map[string]any{"id": 7, "kind": "latency", "state": "running", "profile_ids": []int{12, 15}})
		}
		polls++
		if polls < 3 {
			return testserver.OK(map[string]any{"id": 7, "kind": "latency", "state": "running"})
		}
		return testserver.OK(map[string]any{"id": 7, "kind": "latency", "state": "done",
			"results": []map[string]any{{"id": 12, "latency_ms": 85}, {"id": 15, "latency_ms": -1}}})
	}}
	c, _ := serve(t, rec.handle)
	job, err := c.RunTest(t.Context(), throne.TestLatency, throne.SelectProfiles(12, 15))
	if err != nil {
		t.Fatal(err)
	}
	if job.State != throne.JobDone || len(job.Results) != 2 || job.Results[1].LatencyMS != -1 {
		t.Fatalf("job = %+v", job)
	}
	reqs := rec.requests()
	jsonEqual(t, "POST body", reqs[0].Body, `{"kind":"latency","profiles":[12,15],"wait":true}`)
	for _, r := range reqs[1:] {
		if r.Method != "GET" || r.Path != "/v1/tests/7" {
			t.Fatalf("poll %s %s", r.Method, r.Path)
		}
	}
	if len(reqs) != 4 {
		t.Fatalf("%d requests, want 4", len(reqs))
	}
}

func TestRunTestDeadline(t *testing.T) {
	defer throne.SetPollInterval(10 * time.Millisecond)()
	rec := &recorder{answer: always(testserver.Accepted(map[string]any{"id": 9, "kind": "speed", "state": "running"}))}
	c, _ := serve(t, rec.handle)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	job, err := c.RunTest(ctx, throne.TestSpeed, throne.SelectGroup(3, ""))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if job == nil || job.ID != 9 || job.State != throne.JobRunning {
		t.Fatalf("job = %+v, want the running job", job)
	}
	if m := rec.requests()[0].Fields(); m["timeout"] != 1.0 || m["wait"] != true {
		t.Fatalf("POST body %s", rec.requests()[0].Body)
	}
}
