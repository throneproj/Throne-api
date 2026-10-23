package web

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// Error codes of the gateway itself.
const (
	codeUnauthenticated   = "unauthenticated"
	codeCSRF              = "csrf"
	codeBadPassword       = "bad_password"
	codeRateLimited       = "rate_limited"
	codePasswordFile      = "password_file"
	codeThroneUnreachable = "throne_unreachable"
	codeThroneRejected    = "throne_rejected"
	codeThroneTimeout     = "throne_timeout"
)

// Body limits: /auth bodies are a password or two, /api ones JSON for Throne.
const (
	maxAuthBody  = 4 << 10
	maxSmallBody = 1 << 20
)

type gatewayInfo struct {
	Version      string  `json:"version"`
	Throne       string  `json:"throne"`
	TLS          bool    `json:"tls"`
	Fingerprint  *string `json:"fingerprint"`
	PasswordFile bool    `json:"password_file"`
}

type authState struct {
	Authenticated bool         `json:"authenticated"`
	CSRF          string       `json:"csrf,omitempty"`
	Gateway       *gatewayInfo `json:"gateway,omitempty"`
}

func (s *Server) state(sess *session) authState {
	if sess == nil {
		return authState{}
	}
	g := &gatewayInfo{Version: s.cfg.Version, Throne: s.cfg.ThroneAddr, TLS: s.tls != nil,
		PasswordFile: s.cfg.PasswordFile != ""}
	if s.fingerprint != "" {
		g.Fingerprint = &s.fingerprint
	}
	return authState{Authenticated: true, CSRF: sess.csrf, Gateway: g}
}

// handleState reports the session of the cookie, which counts only with its token in
// X-CSRF-Token: a cookie alone, which the browser also sends to other services on this
// host, reveals nothing.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET")
		writeError(w, http.StatusMethodNotAllowed, throne.CodeMethodNotAllowed, "Use GET")
		return
	}
	writeJSON(w, http.StatusOK, s.state(s.authorized(r, false)))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, throne.CodeMethodNotAllowed, "Use POST")
		return
	}
	if !s.originOK(r) {
		writeError(w, http.StatusForbidden, codeCSRF, "The request comes from another site")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !s.readJSON(w, r, maxAuthBody, &body) {
		return
	}
	ok, wait, err := s.checkPassword(r.Context(), clientKey(r.RemoteAddr), body.Password)
	switch {
	case err != nil:
		return
	case wait > 0:
		s.slowFailure(r.Context(), start)
		writeRateLimited(w, wait, "Too many failed logins")
		return
	case !ok:
		s.slowFailure(r.Context(), start)
		writeError(w, http.StatusUnauthorized, codeBadPassword, "Wrong password")
		return
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.removeCookie(c.Value)
	}
	sess, token := s.sessions.create()
	s.setCookie(w, token)
	writeJSON(w, http.StatusOK, s.state(sess))
}

// checkPassword verifies a password, at most two at a time, and counts a failure
// against the client. It returns how long the client must wait instead when the login
// limiter refuses it. The error is ctx's, when the client went away while it waited.
func (s *Server) checkPassword(ctx context.Context, client netip.Prefix, password string) (bool, time.Duration, error) {
	if wait := s.limiter.blocked(client, s.now()); wait > 0 {
		return false, wait, nil
	}
	select {
	case s.verify <- struct{}{}:
	case <-ctx.Done():
		return false, 0, ctx.Err()
	}
	defer func() { <-s.verify }()
	// Requests that queued behind a failure must not get past its lock.
	if wait := s.limiter.blocked(client, s.now()); wait > 0 {
		return false, wait, nil
	}
	ok := len(password) <= maxPasswordLength && s.password.current().matches(password)
	if ok {
		s.limiter.succeed(client)
	} else if lock := s.limiter.fail(client, s.now()); lock > 0 {
		s.logf("failed logins from %s: refusing its logins for %v", clientName(client), lock)
	}
	return ok, 0, nil
}

// slowFailure makes a failed login last at least loginFloor.
func (s *Server) slowFailure(ctx context.Context, start time.Time) {
	t := time.NewTimer(s.t.loginFloor - time.Since(start))
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

func writeRateLimited(w http.ResponseWriter, wait time.Duration, why string) {
	secs := max(int(math.Ceil(wait.Seconds())), 1)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]any{"code": codeRateLimited,
		"message": why + "; try again in " + strconv.Itoa(secs) + " s", "retry_after": secs}})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *session) {
	s.sessions.remove(sess)
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request, sess *session) {
	start := time.Now()
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !s.readJSON(w, r, maxAuthBody, &body) {
		return
	}
	if s.password.plain {
		writeError(w, http.StatusConflict, codePasswordFile,
			"The password comes from a password file; change that file instead")
		return
	}
	if err := CheckPassword(body.New); err != nil {
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, upperFirst(err.Error()))
		return
	}
	ok, wait, err := s.checkPassword(r.Context(), clientKey(r.RemoteAddr), body.Current)
	switch {
	case err != nil:
		return
	case wait > 0:
		s.slowFailure(r.Context(), start)
		writeRateLimited(w, wait, "Too many failed logins")
		return
	case !ok:
		s.slowFailure(r.Context(), start)
		writeError(w, http.StatusUnauthorized, codeBadPassword, "Wrong current password")
		return
	}
	if err := s.password.set(s.cfg.StateDir, body.New); err != nil {
		writeError(w, http.StatusInternalServerError, throne.CodeInternal, "Could not save the password: "+err.Error())
		return
	}
	n := s.sessions.dropExcept(sess)
	s.logf("the web UI password was changed in the web UI; signed out %d other sessions", n)
	w.WriteHeader(http.StatusNoContent)
}

// watchPassword drops every session when the password file changes, such as through
// thronectl web passwd.
func (s *Server) watchPassword() {
	t := time.NewTicker(s.t.passwordCheck)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		s.checkPasswordFile()
	}
}

func (s *Server) checkPasswordFile() {
	changed, err := s.password.changed()
	switch {
	case err != nil:
		if msg := err.Error(); msg != s.passwordErr {
			s.passwordErr = msg
			s.logf("password file: %v; the password stays as it was", err)
		}
		return
	case changed:
		n := s.sessions.dropExcept(nil)
		s.logf("the web UI password changed; signed out %d sessions", n)
	}
	s.passwordErr = ""
}

// originOK checks the Origin header of a request that changes something: absent, or the
// request's own scheme and host. An https origin passes on a plain listener too, which
// is how a TLS reverse proxy in front of it looks.
func (s *Server) originOK(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.Path != "" {
		return false
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && s.tls == nil:
	default:
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":{"code":"internal","message":"encode the response"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {code, message}})
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
