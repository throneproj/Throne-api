package web

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	ipFailures     = 5               // failures within ipWindow that lock an address
	ipWindow       = 5 * time.Minute //
	firstLock      = time.Minute     // doubled by each further failure
	maxLock        = 15 * time.Minute
	globalFailures = 30 // more failures than this within globalWindow lock every login
	globalWindow   = time.Minute
	maxTracked     = 4096 // addresses the limiter remembers
)

// loginLimiter counts failed logins per client address and in all.
type loginLimiter struct {
	mu     sync.Mutex
	ips    map[netip.Prefix]*ipRecord
	global []time.Time
}

type ipRecord struct {
	failures []time.Time
	until    time.Time
	lock     time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{ips: map[netip.Prefix]*ipRecord{}}
}

// clientKey groups the addresses of one client: an IPv4 address, or an IPv6 /64.
func clientKey(remoteAddr string) netip.Prefix {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Prefix{}
	}
	ip = ip.Unmap().WithZone("")
	bits := 32
	if ip.Is6() {
		bits = 64
	}
	p, _ := ip.Prefix(bits)
	return p
}

// blocked returns how long the client must wait before it may try again, or 0.
func (l *loginLimiter) blocked(client netip.Prefix, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneGlobal(now)
	var wait time.Duration
	if n := len(l.global); n > globalFailures {
		wait = l.global[n-globalFailures-1].Add(globalWindow).Sub(now)
	}
	if r := l.record(client, now); r != nil && now.Before(r.until) {
		wait = max(wait, r.until.Sub(now))
	}
	return wait
}

// fail counts a failed login of the client and returns how long it locks the client
// out, or 0.
func (l *loginLimiter) fail(client netip.Prefix, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.global = append(l.global, now)
	l.pruneGlobal(now)
	r := l.record(client, now)
	if r == nil {
		if len(l.ips) >= maxTracked {
			l.prune(now)
		}
		if len(l.ips) >= maxTracked {
			return 0
		}
		r = &ipRecord{}
		l.ips[client] = r
	}
	r.failures = append(r.failures, now)
	if len(r.failures) < ipFailures {
		return 0
	}
	r.lock = min(max(2*r.lock, firstLock), maxLock)
	r.until = now.Add(r.lock)
	return r.lock
}

// clientName prints a client key: the address, or the IPv6 /64.
func clientName(client netip.Prefix) string {
	if client.Addr().Is4() {
		return client.Addr().String()
	}
	return client.String()
}

// succeed forgets the client's failures.
func (l *loginLimiter) succeed(client netip.Prefix) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, client)
}

// record returns the client's record with its old failures dropped; a client without
// recent failures and no lock is forgotten, its lock length with it.
func (l *loginLimiter) record(client netip.Prefix, now time.Time) *ipRecord {
	r := l.ips[client]
	if r == nil {
		return nil
	}
	i := 0
	for i < len(r.failures) && now.Sub(r.failures[i]) >= ipWindow {
		i++
	}
	r.failures = r.failures[i:]
	if len(r.failures) == 0 && !now.Before(r.until) {
		delete(l.ips, client)
		return nil
	}
	return r
}

func (l *loginLimiter) prune(now time.Time) {
	for client := range l.ips {
		l.record(client, now)
	}
}

func (l *loginLimiter) pruneGlobal(now time.Time) {
	i := 0
	for i < len(l.global) && now.Sub(l.global[i]) >= globalWindow {
		i++
	}
	l.global = l.global[i:]
}
