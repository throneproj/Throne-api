// Package testserver runs a Throne API server on a loopback port for tests, built on the
// wire package.
package testserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/throneproj/Throne-api/internal/wire"
)

// Request is a request as the server received it.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`
}

// Fields decodes a JSON object body; it returns nil for a missing or null body.
func (r *Request) Fields() map[string]any {
	var m map[string]any
	json.Unmarshal(r.Body, &m)
	return m
}

// Response is what a Handler answers.
type Response struct {
	Status  int
	Body    any
	Code    string
	Message string
	// Raw, when set, goes onto the connection as it is, in place of a frame.
	Raw []byte
	// Plaintext, when set, is sent as the message of a frame in place of the encoded
	// response.
	Plaintext []byte
}

// OK returns a 200 response.
func OK(body any) *Response { return &Response{Status: 200, Body: body} }

// Accepted returns a 202 response.
func Accepted(body any) *Response { return &Response{Status: 202, Body: body} }

// Error returns an error response.
func Error(status int, code, message string) *Response {
	return &Response{Status: status, Code: code, Message: message}
}

// Handler answers a request. A nil Response closes the connection without an answer.
// ctx ends when the server closes.
type Handler func(ctx context.Context, r *Request) *Response

// Config configures a Server.
type Config struct {
	// Key is the key in its text form.
	Key     string
	Handler Handler
	// Idle is how long the server waits for a request; 0 means 60 seconds.
	Idle time.Duration
}

// Server is a running test server.
type Server struct {
	// Addr is the server's address, host:port.
	Addr string

	cfg      Config
	key      [wire.KeySize]byte
	ln       net.Listener
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	accepted atomic.Int64
}

// Start starts a server that closes when the test ends.
func Start(t testing.TB, cfg Config) *Server {
	t.Helper()
	k, err := base64.RawURLEncoding.DecodeString(cfg.Key)
	if err != nil || len(k) != wire.KeySize {
		t.Fatal("testserver: the key does not decode to 32 bytes")
	}
	if cfg.Idle == 0 {
		cfg.Idle = 60 * time.Second
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), cfg: cfg, ln: ln, conns: map[net.Conn]struct{}{}}
	copy(s.key[:], k)
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.wg.Go(s.accept)
	t.Cleanup(s.Close)
	return s
}

// Accepted returns how many connections the server has accepted.
func (s *Server) Accepted() int { return int(s.accepted.Load()) }

// Close stops the server and closes its connections.
func (s *Server) Close() {
	s.cancel()
	s.ln.Close()
	s.mu.Lock()
	for nc := range s.conns {
		nc.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) accept() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.accepted.Add(1)
		s.mu.Lock()
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			nc.Close()
			return
		}
		s.conns[nc] = struct{}{}
		s.mu.Unlock()
		s.wg.Go(func() { s.serve(nc) })
	}
}

func (s *Server) serve(nc net.Conn) {
	defer func() {
		nc.Close()
		s.mu.Lock()
		delete(s.conns, nc)
		s.mu.Unlock()
	}()
	nc.SetDeadline(time.Now().Add(10 * time.Second))
	wc, err := wire.Server(nc, &s.key)
	if err != nil {
		return
	}
	for {
		nc.SetDeadline(time.Now().Add(s.cfg.Idle))
		msg, err := wc.ReadMessage()
		if err != nil {
			return
		}
		nc.SetDeadline(time.Time{})
		resp := s.respond(msg)
		if resp == nil {
			return
		}
		if resp.Raw != nil {
			if _, err := nc.Write(resp.Raw); err != nil {
				return
			}
			continue
		}
		if err := wc.WriteMessage(resp.encode()); err != nil {
			return
		}
	}
}

func (s *Server) respond(msg []byte) *Response {
	var r Request
	if err := json.Unmarshal(msg, &r); err != nil || r.Method == "" || !strings.HasPrefix(r.Path, "/") {
		return Error(400, "bad_request", "The request is malformed")
	}
	return s.cfg.Handler(s.ctx, &r)
}

func (r *Response) encode() []byte {
	if r.Plaintext != nil {
		return r.Plaintext
	}
	var v any
	if r.Status >= 400 {
		v = map[string]any{"status": r.Status, "error": map[string]string{"code": r.Code, "message": r.Message}}
	} else {
		v = map[string]any{"status": r.Status, "body": r.Body}
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
