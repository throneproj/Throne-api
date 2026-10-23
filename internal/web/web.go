// Package web is the gateway behind thronectl web: it serves the browser app embedded
// in ui/ and passes its requests to Throne over the Throne API.
package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// Throne is the part of *throne.Client that the gateway uses.
type Throne interface {
	Send(ctx context.Context, method, path string, query map[string]string, body any) (*throne.Response, error)
	Events(ctx context.Context, q throne.EventsQuery) (*throne.Events, error)
	Logs(ctx context.Context, q throne.LogQuery) (*throne.Logs, error)
	Backup(ctx context.Context, w io.Writer, parts []string, opts ...throne.Option) (*throne.BackupInfo, error)
	UploadRestore(ctx context.Context, r io.ReadSeeker) (*throne.RestoreUpload, error)
}

// Config configures a Server.
type Config struct {
	// Throne is the client for Throne, and ThroneAddr its address, for messages.
	Throne     Throne
	ThroneAddr string
	// Version is the gateway's version, which the app shows.
	Version string
	// Listen is the address to listen on, as ParseListen returns it.
	Listen string
	// StateDir holds the password hash, the self-signed certificate and temporary files.
	StateDir string
	// TLS turns HTTPS on. Without TLSCert and TLSKey the Server uses a self-signed
	// certificate in StateDir/tls, which it creates when there is none.
	TLS             bool
	TLSCert, TLSKey string
	// AllowHosts are host names accepted in the Host header besides the loopback ones.
	AllowHosts []string
	// PasswordFile, when set, holds the password in plain text and overrides the stored
	// hash.
	PasswordFile string
	// Logf prints a message for the operator; nil discards them.
	Logf func(format string, args ...any)
}

// ConfigError is a mistake in the Config or in the files it names, such as an empty
// password file.
type ConfigError struct {
	Msg string
}

func (e *ConfigError) Error() string { return e.Msg }

// timing holds the intervals of the Server; tests shorten them.
type timing struct {
	eventsWait    time.Duration // how long Throne may hold /v1/events and /v1/logs
	retry         time.Duration // between attempts to reach Throne
	minPoll       time.Duration // least time between two long polls that brought nothing
	statusPoll    time.Duration // /v1/status polling for a Throne without /v1/events
	fullRefresh   time.Duration // every topic while polling /v1/status
	ping          time.Duration // SSE keep-alive comments
	loginFloor    time.Duration // least duration of a failed login
	passwordCheck time.Duration // how often the password file is checked for changes
	shutdown      time.Duration // graceful shutdown limit
	streamWrite   time.Duration // write deadline of one SSE event
	bodyRead      time.Duration // read deadline of a request body, but an archive's
	recentOK      time.Duration // since a call to Throne succeeded, ErrRejected means a closed connection
}

var defaultTiming = timing{
	eventsWait:    25 * time.Second,
	retry:         2 * time.Second,
	minPoll:       500 * time.Millisecond,
	statusPoll:    2 * time.Second,
	fullRefresh:   30 * time.Second,
	ping:          15 * time.Second,
	loginFloor:    500 * time.Millisecond,
	passwordCheck: 5 * time.Second,
	shutdown:      5 * time.Second,
	streamWrite:   30 * time.Second,
	bodyRead:      15 * time.Second,
	recentOK:      10 * time.Minute,
}

// Limits on the calls to Throne, which takes 32 connections in all and 8 from an address
// other than loopback, and on the requests without a session.
const (
	maxThroneRequests = 4 // besides the events hub's and the log follower's
	maxThroneChanges  = 3 // of them, what other requests than GET may hold
	maxAnonymous      = 8 // requests in progress per client without a valid session
	h2Streams         = 32
)

// Server is the gateway. Make one with New.
type Server struct {
	cfg         Config
	throne      Throne
	logf        func(format string, args ...any)
	loopback    bool
	allow       map[string]bool
	tls         *tls.Config
	fingerprint string
	created     string

	password    *passwordStore
	passwordErr string // the last error of the password file, logged once
	sessions    *sessionStore
	limiter     *loginLimiter
	verify      chan struct{}
	calls       chan struct{}
	changes     chan struct{}
	anonymous   *flightLimiter
	lastOK      atomic.Int64 // when a call to Throne last got an answer, in Unix nanoseconds
	hub         *hub
	logs        *logFollower
	restore     chan struct{}
	assets      fs.FS
	t           timing
	now         func() time.Time
	conns       connLimits

	ctx       context.Context
	cancel    context.CancelFunc
	closing   chan struct{}
	closeOnce sync.Once
}

// New prepares a Server: it creates the state directory, loads or creates the password
// and, with TLS, the certificate. It does not contact Throne.
func New(cfg Config) (*Server, error) {
	if cfg.Throne == nil {
		return nil, errors.New("web: no Throne client")
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	loopback, err := loopbackListen(cfg.Listen)
	if err != nil {
		return nil, &ConfigError{err.Error()}
	}
	allow, err := allowList(cfg.AllowHosts)
	if err != nil {
		return nil, &ConfigError{err.Error()}
	}
	if err := prepareStateDir(cfg.StateDir, logf); err != nil {
		return nil, err
	}
	removeLeftovers(cfg.StateDir)
	s := &Server{
		cfg:       cfg,
		logf:      logf,
		loopback:  loopback,
		allow:     allow,
		verify:    make(chan struct{}, 2),
		calls:     make(chan struct{}, maxThroneRequests),
		changes:   make(chan struct{}, maxThroneChanges),
		anonymous: newFlightLimiter(maxAnonymous),
		restore:   make(chan struct{}, 1),
		assets:    embedded(),
		t:         defaultTiming,
		now:       time.Now,
		conns:     defaultConnLimits,
		closing:   make(chan struct{}),
	}
	s.throne = tracked{cfg.Throne, s}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.sessions = newSessionStore(func() time.Time { return s.now() })
	s.limiter = newLoginLimiter()
	s.hub = newHub(s)
	s.logs = newLogFollower(s)
	if s.password, s.created, err = openPasswordStore(cfg.StateDir, cfg.PasswordFile); err != nil {
		return nil, err
	}
	if s.created != "" {
		logf("created the web UI password: %s", s.created)
		logf("change it with 'thronectl web passwd'")
	}
	if cfg.TLS {
		cert, made, err := loadCertificate(cfg.StateDir, cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, err
		}
		if made {
			logf("created a self-signed TLS certificate in %s", certDir(cfg.StateDir))
		}
		s.fingerprint = fingerprint(cert.Certificate[0])
		s.tls = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}
		logf("TLS certificate SHA-256 fingerprint: %s", s.fingerprint)
	} else if !loopback {
		logf("warning: TLS is off on a network address: passwords and sessions cross the network unencrypted; " +
			"use this only behind a TLS reverse proxy")
	}
	return s, nil
}

// Fingerprint returns the SHA-256 fingerprint of the TLS certificate, or "" without TLS.
func (s *Server) Fingerprint() string { return s.fingerprint }

// Handler returns the gateway's HTTP handler.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

// ListenAndServe listens on the Config's address and serves until ctx ends, then shuts
// down within 5 seconds.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	for _, u := range serveURLs(ln.Addr(), s.tls != nil) {
		s.logf("serving on %s for Throne at %s", u, s.cfg.ThroneAddr)
	}
	return s.Serve(ctx, ln)
}

// Serve serves on ln until ctx ends, then shuts down within 5 seconds. It closes ln.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(serverLog{s.logf}, "", 0),
		BaseContext:       func(net.Listener) context.Context { return s.ctx },
	}
	ll := newLimitListener(ln, s.conns)
	ln, srv.ConnState = ll, ll.connState
	// WriteByteTimeout runs only while data waits to be written, so idle streams keep.
	srv.HTTP2 = &http.HTTP2Config{MaxConcurrentStreams: h2Streams, WriteByteTimeout: s.t.streamWrite}
	go s.watchPassword()
	served := make(chan error, 1)
	go func() {
		if s.tls != nil {
			srv.TLSConfig = s.tls
			served <- srv.ServeTLS(ln, "", "")
		} else {
			served <- srv.Serve(ln)
		}
	}()
	select {
	case err := <-served:
		s.Close()
		return err
	case <-ctx.Done():
	}
	s.endStreams()
	sctx, cancel := context.WithTimeout(context.Background(), s.t.shutdown)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		srv.Close()
	}
	s.Close()
	<-served
	return nil
}

// Close ends the streams and the calls to Throne that run for the Server's handlers.
func (s *Server) Close() {
	s.endStreams()
	s.cancel()
}

func (s *Server) endStreams() {
	s.closeOnce.Do(func() { close(s.closing) })
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	h.Set("Cache-Control", "no-store")
	if !s.hostAllowed(r.Host) {
		closeUnread(w, r)
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMisdirectedRequest)
		io.WriteString(w, "421 Misdirected Request: this host name is not accepted; see thronectl web --allow-host\n")
		return
	}
	if r.ContentLength != 0 {
		// Bounds bodies that no handler reads too: net/http reads them before it answers.
		http.NewResponseController(w).SetReadDeadline(time.Now().Add(s.t.bodyRead))
	}
	if s.limitsAnonymous(r) {
		closeUnread(w, r)
		client := clientKey(r.RemoteAddr)
		if !s.anonymous.enter(client) {
			writeRateLimited(w, time.Second, "Too many requests at once from this address")
			return
		}
		defer s.anonymous.leave(client)
	}
	switch p := r.URL.Path; {
	case p == "/auth/state":
		s.handleState(w, r)
	case p == "/auth/login":
		s.handleLogin(w, r)
	case p == "/auth/logout":
		s.withSession(w, r, http.MethodPost, false, s.handleLogout)
	case p == "/auth/password":
		s.withSession(w, r, http.MethodPost, false, s.handlePassword)
	case strings.HasPrefix(p, "/api/v1/"):
		s.withSession(w, r, "", false, s.handleProxy)
	case p == "/api/events":
		s.withSession(w, r, http.MethodGet, true, s.handleEvents)
	case p == "/api/logs":
		s.withSession(w, r, http.MethodGet, true, s.handleLogs)
	case p == "/api/backup":
		s.withSession(w, r, http.MethodPost, false, s.handleBackup)
	case p == "/api/restore":
		s.withSession(w, r, http.MethodPost, false, s.handleRestore)
	case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/auth/"):
		if s.authorized(r, false) == nil {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Sign in first")
			return
		}
		writeError(w, http.StatusNotFound, throne.CodeNotFound, "Unknown path")
	default:
		s.serveAsset(w, r)
	}
}

// limitsAnonymous says whether a request counts against maxAnonymous: one without a
// valid session for a path that does work, or with a body. Static files do not count,
// as the browser loads many at once before it signs in.
func (s *Server) limitsAnonymous(r *http.Request) bool {
	p := r.URL.Path
	if r.ContentLength == 0 && !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/auth/") {
		return false
	}
	return s.authorized(r, true) == nil
}

// withSession runs h for a request with a valid session and its CSRF token, whatever
// the method; with query, the token may come in the token query parameter. method, unless
// "", is the only method h takes. A request that changes something must not come from
// another origin.
func (s *Server) withSession(w http.ResponseWriter, r *http.Request, method string, query bool,
	h func(http.ResponseWriter, *http.Request, *session)) {
	sess := s.authorized(r, query)
	if sess == nil {
		writeError(w, http.StatusUnauthorized, codeUnauthenticated, "Sign in first")
		return
	}
	if method != "" && r.Method != method {
		w.Header().Set("Allow", method)
		writeError(w, http.StatusMethodNotAllowed, throne.CodeMethodNotAllowed, "Use "+method)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !s.originOK(r) {
		writeError(w, http.StatusForbidden, codeCSRF, "The request comes from another site")
		return
	}
	h(w, r, sess)
}

// serverLog passes net/http's messages on, without the TLS handshake failures that
// every browser causes while it does not trust a self-signed certificate yet.
type serverLog struct {
	logf func(format string, args ...any)
}

func (l serverLog) Write(p []byte) (int, error) {
	if !bytes.Contains(p, []byte("TLS handshake error")) {
		l.logf("%s", bytes.TrimRight(p, "\n"))
	}
	return len(p), nil
}

// removeLeftovers deletes the temporary files of an earlier run that ended abruptly.
func removeLeftovers(dir string) {
	for _, pattern := range []string{tempBackup + "*", tempRestore + "*", tempPassword + "*"} {
		names, _ := globIn(dir, pattern)
		for _, name := range names {
			os.Remove(name)
		}
	}
}
