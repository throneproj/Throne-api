// Package throne is a client for the Throne API, which lets scripts on the same machine
// or the LAN control and manage the Throne proxy client.
//
// The requests are specified in spec/api.md and the wire protocol in spec/protocol.md
// of this repository.
//
// Admin requests, which import, delete, change settings, back up, restore, restart, shut
// down and read the log, are answered only from Throne's own device, unless Throne allows management
// from the local network; from elsewhere they fail with CodeForbidden. Their
// documentation says that they are admin requests.
package throne

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/throneproj/Throne-api/internal/rawbody"
	"github.com/throneproj/Throne-api/internal/wire"
)

// DefaultAddr is the address Throne listens on by default.
const DefaultAddr = "127.0.0.1:9095"

const (
	// dialTimeout covers the TCP connect and the hello exchange.
	dialTimeout = 5 * time.Second
	// waitMargin is left between the timeout Throne gets and the ctx deadline, for the
	// response to arrive.
	waitMargin = 2 * time.Second
	// readAllowance bounds a call whose ctx has no deadline, on top of the time Throne
	// may hold it.
	readAllowance = 60 * time.Second
	// defaultWait and maxWait are Throne's default and largest timeout field.
	defaultWait = 30 * time.Second
	maxWait     = 300 * time.Second
	// cancelWait is how long Throne waits for a cancelled test to stop.
	cancelWait = 10 * time.Second
)

var aLongTimeAgo = time.Unix(1, 0)

// Client calls the Throne API, one connection per call. Make one with New or FromEnv.
// It is safe for concurrent use.
//
// Every call honours the deadline and cancellation of its ctx. A call whose ctx has no
// deadline gives up 60 seconds after the longest time Throne may hold it.
type Client struct {
	api
	addr string
	key  Key
}

// New returns a Client for the Throne API at addr (host:port) that uses key.
func New(addr string, key Key) *Client {
	c := &Client{addr: addr, key: key}
	c.api = api{c.do}
	return c
}

// FromEnv returns a Client for the address in THRONE_ADDR (default DefaultAddr) with
// the key from THRONE_KEY, or else from the file that THRONE_KEY_FILE names.
func FromEnv() (*Client, error) {
	var key Key
	switch text, file := os.Getenv("THRONE_KEY"), os.Getenv("THRONE_KEY_FILE"); {
	case text != "":
		var ok bool
		if key, ok = parseKey(text); !ok {
			return nil, errors.New("throne: invalid key in THRONE_KEY: " + keyFormat)
		}
	case file != "":
		var err error
		if key, err = ReadKeyFile(file); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("throne: no key: set THRONE_KEY or THRONE_KEY_FILE")
	}
	addr := os.Getenv("THRONE_ADDR")
	if addr == "" {
		addr = DefaultAddr
	}
	return New(addr, key), nil
}

func (c *Client) do(ctx context.Context, r *call, out any) error {
	msg, err := r.encode()
	if err != nil {
		return err
	}
	ctx, cancel := bound(ctx, r.wait)
	defer cancel()
	cn, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer cn.nc.Close()
	resp, err := cn.roundTrip(ctx, msg)
	if err != nil {
		return err
	}
	return decode(ctx, resp, out)
}

func (c *Client) dial(ctx context.Context) (*conn, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var d net.Dialer
	nc, err := d.DialContext(dctx, "tcp", c.addr)
	if err != nil {
		return nil, c.connectError(ctx, dctx, fmt.Errorf("throne: %w", err))
	}
	cn := &conn{nc: nc}
	err = guard(dctx, nc, func() (err error) {
		cn.wc, err = wire.Client(nc, c.key.bytes())
		return err
	})
	if err != nil {
		nc.Close()
		return nil, c.connectError(ctx, dctx, transportError(err, false))
	}
	return cn, nil
}

// connectError reports the caller's ctx, then the dial timeout, ahead of err.
func (c *Client) connectError(ctx, dctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("throne: connect to %s: %w", c.addr, ctx.Err())
	case dctx.Err() != nil:
		return fmt.Errorf("throne: connect to %s: no answer within %v: %w", c.addr, dialTimeout, context.DeadlineExceeded)
	}
	return err
}

// Session is one connection for several calls in a row, which saves a connection and a
// handshake per call. Calls on a Session run one at a time.
//
// Throne closes the connection after 60 seconds without a request. After any error
// other than an *APIError the Session is closed, and later calls return an error that
// matches net.ErrClosed.
type Session struct {
	api
	mu  sync.Mutex
	cn  *conn
	err error
}

// Dial opens a Session. Close it when done.
func (c *Client) Dial(ctx context.Context) (*Session, error) {
	cn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	s := &Session{cn: cn}
	s.api = api{s.do}
	return s, nil
}

func (s *Session) do(ctx context.Context, r *call, out any) error {
	msg, err := r.encode()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	ctx, cancel := bound(ctx, r.wait)
	defer cancel()
	resp, err := s.cn.roundTrip(ctx, msg)
	if err != nil {
		s.cn.nc.Close()
		s.err = fmt.Errorf("throne: session closed after an earlier error: %w", net.ErrClosed)
		return err
	}
	return decode(ctx, resp, out)
}

// Close closes the Session's connection.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil
	}
	s.err = fmt.Errorf("throne: session closed: %w", net.ErrClosed)
	return s.cn.nc.Close()
}

// call is one request, and how long Throne may hold it before it responds.
type call struct {
	method string
	path   string
	query  map[string]string
	body   any
	wait   time.Duration
}

func (r *call) encode() ([]byte, error) {
	msg, err := json.Marshal(struct {
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Query  map[string]string `json:"query,omitempty"`
		Body   any               `json:"body,omitempty"`
	}{r.method, r.path, r.query, r.body})
	if err != nil {
		return nil, fmt.Errorf("throne: encode request: %w", err)
	}
	if len(msg) > wire.MaxClientLength-wire.MinLength {
		return nil, fmt.Errorf("throne: request of %d bytes: %w", len(msg), wire.ErrTooLarge)
	}
	return msg, nil
}

// bound gives a ctx without a deadline one: wait, the time Throne may hold the call,
// plus readAllowance.
func bound(ctx context.Context, wait time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, wait+readAllowance)
}

// conn is a connection after the handshake.
type conn struct {
	nc       net.Conn
	wc       *wire.Conn
	answered bool // a response arrived, which proves the key
}

// roundTrip sends one request and returns the response. After an error the connection
// is unusable.
func (cn *conn) roundTrip(ctx context.Context, msg []byte) ([]byte, error) {
	var resp []byte
	err := guard(ctx, cn.nc, func() (err error) {
		if err = cn.wc.WriteMessage(msg); err != nil {
			return err
		}
		resp, err = cn.wc.ReadMessage()
		return err
	})
	if err != nil {
		return nil, transportError(err, cn.answered)
	}
	cn.answered = true
	return resp, nil
}

// guard runs f with ctx's deadline and cancellation applied to nc, and returns ctx's
// error in place of the I/O error they cause.
func guard(ctx context.Context, nc net.Conn, f func() error) error {
	deadline, _ := ctx.Deadline()
	if err := nc.SetDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		nc.SetDeadline(aLongTimeAgo)
		close(interrupted)
	})
	err := f()
	if !stop() {
		<-interrupted
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

// transportError describes an error of an exchange; answered says whether a response
// arrived on the connection before.
func transportError(err error, answered bool) error {
	var opErr *net.OpError
	closed := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &opErr)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("throne: %w", err)
	case errors.Is(err, wire.ErrAuth), errors.Is(err, wire.ErrLength):
		return fmt.Errorf("throne: bad frame from Throne: %w", err)
	case closed && !answered:
		return ErrRejected
	case closed:
		return fmt.Errorf("throne: connection lost: %w", err)
	}
	return fmt.Errorf("throne: %w", err)
}

// decode turns a response message into out or an error.
func decode(ctx context.Context, msg []byte, out any) error {
	var r struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(msg, &r); err != nil || r.Status == 0 {
		return errors.New("throne: malformed response from Throne")
	}
	switch {
	case r.Status >= 400:
		e := &APIError{Status: r.Status}
		if r.Error != nil {
			e.Code, e.Message = r.Error.Code, r.Error.Message
		}
		return e
	case r.Status < 200 || r.Status > 299:
		return fmt.Errorf("throne: unexpected status %d from Throne", r.Status)
	}
	rawbody.Store(ctx, r.Body)
	if resp, ok := out.(*Response); ok {
		resp.Status = r.Status
		if len(r.Body) > 0 && string(r.Body) != "null" {
			resp.Body = r.Body
		}
		return nil
	}
	if out == nil || len(r.Body) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, out); err != nil {
		return fmt.Errorf("throne: decode response: %w", err)
	}
	return nil
}
