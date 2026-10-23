package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"sync"
	"time"
)

const (
	cookieName      = "throne_web"
	sessionIdle     = 12 * time.Hour
	sessionLifetime = 7 * 24 * time.Hour
	maxSessions     = 64
)

// session is a signed-in browser.
type session struct {
	key      [sha256.Size]byte
	csrf     string
	created  time.Time
	lastUsed time.Time
	// done is closed when the session ends, which ends its streams.
	done chan struct{}
}

// sessionStore keeps sessions in memory, by the SHA-256 of their cookie.
type sessionStore struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[[sha256.Size]byte]*session
}

func newSessionStore(now func() time.Time) *sessionStore {
	return &sessionStore{now: now, m: map[[sha256.Size]byte]*session{}}
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// create starts a session and returns it with its cookie value, evicting the least
// recently used session when the store is full.
func (st *sessionStore) create() (*session, string) {
	token := randomToken()
	now := st.now()
	sess := &session{key: sha256.Sum256([]byte(token)), csrf: randomToken(), created: now, lastUsed: now,
		done: make(chan struct{})}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expire(now)
	for len(st.m) >= maxSessions {
		var oldest *session
		for _, s := range st.m {
			if oldest == nil || s.lastUsed.Before(oldest.lastUsed) {
				oldest = s
			}
		}
		st.drop(oldest)
	}
	st.m[sess.key] = sess
	return sess, token
}

// authorize returns the live session of a cookie value whose CSRF token is csrf, and
// marks it used; else nil.
func (st *sessionStore) authorize(cookie, csrf string) *session {
	if cookie == "" {
		return nil
	}
	key := sha256.Sum256([]byte(cookie))
	now := st.now()
	st.mu.Lock()
	defer st.mu.Unlock()
	sess := st.m[key]
	if sess == nil || subtle.ConstantTimeCompare([]byte(csrf), []byte(sess.csrf)) != 1 {
		return nil
	}
	if !alive(sess, now) {
		st.drop(sess)
		return nil
	}
	sess.lastUsed = now
	return sess
}

// touch marks a session used, as an open stream does, and reports whether it lives on;
// a session past its time ends here.
func (st *sessionStore) touch(sess *session) bool {
	now := st.now()
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.m[sess.key] != sess {
		return false
	}
	if !alive(sess, now) {
		st.drop(sess)
		return false
	}
	sess.lastUsed = now
	return true
}

func alive(sess *session, now time.Time) bool {
	return now.Sub(sess.lastUsed) < sessionIdle && now.Sub(sess.created) < sessionLifetime
}

// remove ends a session.
func (st *sessionStore) remove(sess *session) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.m[sess.key] == sess {
		st.drop(sess)
	}
}

// removeCookie ends the session of a cookie value, if there is one.
func (st *sessionStore) removeCookie(cookie string) {
	key := sha256.Sum256([]byte(cookie))
	st.mu.Lock()
	defer st.mu.Unlock()
	if sess := st.m[key]; sess != nil {
		st.drop(sess)
	}
}

// dropExcept ends every session but keep, which may be nil.
func (st *sessionStore) dropExcept(keep *session) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, s := range st.m {
		if s != keep {
			st.drop(s)
			n++
		}
	}
	return n
}

func (st *sessionStore) count() int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.m)
}

func (st *sessionStore) expire(now time.Time) {
	for _, s := range st.m {
		if !alive(s, now) {
			st.drop(s)
		}
	}
}

func (st *sessionStore) drop(sess *session) {
	delete(st.m, sess.key)
	close(sess.done)
}

// authorized returns the request's live session when the request carries its CSRF token
// in X-CSRF-Token, or with query, in the token query parameter, which EventSource needs;
// else nil.
func (s *Server) authorized(r *http.Request, query bool) *session {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	token := r.Header.Get("X-CSRF-Token")
	if token == "" && query {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return nil
	}
	return s.sessions.authorize(c.Value, token)
}

func (s *Server) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", MaxAge: int(sessionLifetime / time.Second),
		HttpOnly: true, Secure: s.tls != nil, SameSite: http.SameSiteStrictMode})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.tls != nil, SameSite: http.SameSiteStrictMode})
}
