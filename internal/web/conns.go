package web

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// connLimits caps the open connections.
type connLimits struct {
	total     int // in all; a new connection then closes the longest idle one
	perClient int // per IPv4 address or IPv6 /64
	perSite   int // per IPv6 /48
}

var defaultConnLimits = connLimits{total: 256, perClient: 64, perSite: 128}

// limitListener applies connLimits. A connection over a per-client cap, or over the total
// cap with none idle to make room, is closed as soon as it is accepted.
type limitListener struct {
	net.Listener
	lim connLimits

	mu    sync.Mutex
	conns map[*limitConn]struct{}
	per   map[netip.Prefix]int
}

func newLimitListener(ln net.Listener, lim connLimits) *limitListener {
	return &limitListener{Listener: ln, lim: lim, conns: map[*limitConn]struct{}{}, per: map[netip.Prefix]int{}}
}

// siteKey is the IPv6 /48 of a client key, or the zero Prefix for IPv4.
func siteKey(client netip.Prefix) netip.Prefix {
	if !client.Addr().Is6() {
		return netip.Prefix{}
	}
	p, _ := client.Addr().Prefix(48)
	return p
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if lc := l.admit(c); lc != nil {
			return lc, nil
		}
		c.Close()
	}
}

// admit counts a new connection, making room by closing the longest idle one when the
// total cap is reached, or returns nil.
func (l *limitListener) admit(c net.Conn) *limitConn {
	client := clientKey(c.RemoteAddr().String())
	site := siteKey(client)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.per[client] >= l.lim.perClient || (site.IsValid() && l.per[site] >= l.lim.perSite) {
		return nil
	}
	if len(l.conns) >= l.lim.total {
		var victim *limitConn
		for lc := range l.conns {
			if lc.idle && (victim == nil || lc.since.Before(victim.since)) {
				victim = lc
			}
		}
		if victim == nil {
			return nil
		}
		l.forget(victim)
		victim.Conn.Close()
	}
	lc := &limitConn{Conn: c, l: l, client: client, site: site, idle: true, since: time.Now()}
	l.conns[lc] = struct{}{}
	l.per[client]++
	if site.IsValid() {
		l.per[site]++
	}
	return lc
}

// forget stops counting a connection; l.mu is held.
func (l *limitListener) forget(lc *limitConn) {
	if _, ok := l.conns[lc]; !ok {
		return
	}
	delete(l.conns, lc)
	for _, key := range []netip.Prefix{lc.client, lc.site} {
		if !key.IsValid() {
			continue
		}
		if l.per[key]--; l.per[key] <= 0 {
			delete(l.per, key)
		}
	}
}

// connState follows http.Server's view of a connection: new and idle ones may make room.
func (l *limitListener) connState(c net.Conn, st http.ConnState) {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	lc, ok := c.(*limitConn)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	switch st {
	case http.StateActive:
		lc.idle = false
	case http.StateIdle:
		lc.idle, lc.since = true, time.Now()
	case http.StateHijacked, http.StateClosed:
		l.forget(lc)
	}
}

type limitConn struct {
	net.Conn
	l            *limitListener
	client, site netip.Prefix
	// idle and since are guarded by l.mu.
	idle  bool
	since time.Time
}

func (c *limitConn) Close() error {
	c.l.mu.Lock()
	c.l.forget(c)
	c.l.mu.Unlock()
	return c.Conn.Close()
}

// CloseWrite lets net/http half-close the connection, so that the client reads an error
// response before the connection closes.
func (c *limitConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// flightLimiter caps the requests in progress per client that carry no valid session.
type flightLimiter struct {
	max int
	mu  sync.Mutex
	n   map[netip.Prefix]int
}

func newFlightLimiter(max int) *flightLimiter {
	return &flightLimiter{max: max, n: map[netip.Prefix]int{}}
}

func (f *flightLimiter) enter(client netip.Prefix) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[client] >= f.max {
		return false
	}
	f.n[client]++
	return true
}

func (f *flightLimiter) leave(client netip.Prefix) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[client]--; f.n[client] <= 0 {
		delete(f.n, client)
	}
}
