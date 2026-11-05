package fakethrone

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
)

type connection struct {
	throne.Connection
	up, down int64 // the usual bytes per second while open
	left     int   // seconds until it closes
}

var processes = []struct{ name, path string }{
	{"firefox", "/usr/lib/firefox/firefox"}, {"chrome", "/opt/google/chrome/chrome"},
	{"Telegram", "/opt/telegram/Telegram"}, {"Discord", "/opt/discord/Discord"},
	{"steam", "/home/user/.local/share/Steam/ubuntu12_64/steam"}, {"spotify", "/usr/share/spotify/spotify"},
	{"code", "/usr/share/code/code"}, {"curl", "/usr/bin/curl"}, {"git-remote-https", "/usr/lib/git-core/git-remote-https"},
}

// track refreshes the connections as the core reports them once a second; s.mu is held.
func (s *Server) track(p *throne.Profile) {
	now := time.Now().UnixMilli()
	if len(s.tracked) == 0 {
		for range 24 {
			s.tracked = append(s.tracked, s.newConnection(p, now-s.rnd.Int64N(600_000)))
		}
	}
	open := 0
	for _, c := range s.tracked {
		if c.ClosedAt != 0 {
			continue
		}
		if c.left--; c.left <= 0 {
			closeConnection(c, now)
			continue
		}
		open++
		c.UploadSpeed = c.up/2 + s.rnd.Int64N(c.up+1)
		c.DownloadSpeed = c.down/2 + s.rnd.Int64N(c.down+1)
		c.Upload += c.UploadSpeed
		c.Download += c.DownloadSpeed
	}
	for i := s.rnd.IntN(4); i > 0 && open < 80; i-- {
		s.tracked = append(s.tracked, s.newConnection(p, now))
		open++
	}
	// Keep the newest 150 closed connections.
	closed := 0
	for i := len(s.tracked) - 1; i >= 0; i-- {
		if s.tracked[i].ClosedAt != 0 {
			if closed++; closed > 150 {
				s.tracked = slices.Delete(s.tracked, i, i+1)
			}
		}
	}
	s.trackedAt = now
}

func closeConnection(c *connection, at int64) {
	c.ClosedAt, c.UploadSpeed, c.DownloadSpeed = at, 0, 0
}

// newConnection makes up a connection that opened at created; s.mu is held.
func (s *Server) newConnection(p *throne.Profile, created int64) *connection {
	pr := processes[s.rnd.IntN(len(processes))]
	src, srcName := "127.0.0.1", "Local"
	if s.tun {
		src = "172.19.0.1"
	}
	if s.rnd.IntN(8) == 0 {
		src = fmt.Sprintf("192.168.1.%d", 20+s.rnd.IntN(30))
		srcName, pr.name, pr.path = src, "", ""
	}
	c := &connection{Connection: throne.Connection{
		ID: fmt.Sprintf("%016x", s.rnd.Uint64()), CreatedAt: created, Network: "tcp", Protocol: "tls",
		Source: fmt.Sprintf("%s:%d", src, 40000+s.rnd.IntN(25000)), SourceName: srcName,
		Process: pr.name, ProcessPath: pr.path, Outbound: p.Name, OutboundTag: "proxy",
	}}
	switch x := s.rnd.IntN(10); {
	case x < 7:
		c.Domain = sites[s.rnd.IntN(len(sites))]
		c.Destination = fmt.Sprintf("%d.%d.%d.%d:443", []int{142, 140, 104, 151}[s.rnd.IntN(4)], s.rnd.IntN(256), s.rnd.IntN(256), 1+s.rnd.IntN(254))
		if strings.Contains(c.Domain, "goog") || strings.Contains(c.Domain, "youtube") || strings.Contains(c.Domain, "ytimg") {
			c.Rule, c.RuleItem = "domain_suffix=[google.com googlevideo.com youtube.com ytimg.com] => route(proxy)", "domain_suffix=google.com"
			if s.rnd.IntN(3) == 0 {
				c.Network, c.Protocol = "udp", "quic"
			}
		}
	case x < 9:
		c.Domain = directSites[s.rnd.IntN(len(directSites))]
		c.Destination = fmt.Sprintf("%s:443", c.Domain)
		c.Outbound, c.OutboundTag = "direct", "direct"
		c.Rule, c.RuleItem = "rule_set=[geosite-cn geoip-cn] => route(direct)", "rule_set=geosite-cn"
		if c.Domain == "192.168.1.1" {
			c.Domain, c.Destination, c.Protocol = "", "192.168.1.1:80", "http"
			c.Rule, c.RuleItem = "ip_is_private=true => route(direct)", "ip_is_private=true"
		}
	default:
		c.Destination, c.Network, c.Protocol = fmt.Sprintf("162.159.%d.%d:3478", s.rnd.IntN(256), 1+s.rnd.IntN(254)), "udp", "stun"
	}
	c.up, c.down = s.rnd.Int64N(20_000), s.rnd.Int64N(400_000)
	if c.left = 1 + s.rnd.IntN(8); s.rnd.IntN(3) == 0 {
		c.left = 30 + s.rnd.IntN(300)
	}
	if age := (time.Now().UnixMilli() - created) / 1000; age > 0 {
		c.Upload, c.Download = c.up*age/4, c.down*age/4
		if s.rnd.IntN(2) == 0 {
			closeConnection(c, created+age*1000/2)
		}
	}
	return c
}

// connectionsView returns the connections as GET /v1/connections lists them; s.mu is held.
func (s *Server) connectionsView() throne.Connections {
	list := make([]throne.Connection, 0, len(s.tracked))
	for _, c := range s.tracked {
		list = append(list, c.Connection)
	}
	slices.SortStableFunc(list, func(a, b throne.Connection) int {
		if (a.ClosedAt == 0) != (b.ClosedAt == 0) {
			if a.ClosedAt == 0 {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.CreatedAt, a.CreatedAt)
	})
	return throne.Connections{Running: true, UpdatedAt: s.trackedAt, Connections: list[:min(len(list), 2000)]}
}

func (s *Server) getConnections(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "running" || s.profiles[s.running] == nil {
		return ok(throne.Connections{Connections: []throne.Connection{}})
	}
	// Like Throne, a request keeps the list fresh for 10 s; the first one after a pause sees the old one.
	s.trackLease = time.Now().Add(10 * time.Second)
	return ok(s.connectionsView())
}

func (s *Server) closeConnections(_ context.Context, r *request) *response {
	all, bad := boolField(r.fields, "all", false)
	if bad != nil {
		return bad
	}
	v, hasIDs := r.fields["ids"]
	hasIDs = hasIDs && v != nil
	if hasIDs == all {
		return badRequest(`Give ids or "all": true`)
	}
	var ids []string
	if hasIDs {
		list, isList := v.([]any)
		if !isList || len(list) == 0 {
			return badRequest("ids must be a list of connection IDs")
		}
		for _, x := range list {
			id, isString := x.(string)
			if !isString {
				return badRequest("ids must be a list of connection IDs")
			}
			ids = append(ids, id)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "running" {
		return fail(409, "not_running", "No profile runs")
	}
	closed, now := 0, time.Now().UnixMilli()
	for _, c := range s.tracked {
		if c.ClosedAt == 0 && (all || slices.Contains(ids, c.ID)) {
			closeConnection(c, now)
			closed++
		}
	}
	s.logf("[Connections] Closed %d connections", closed)
	return ok(map[string]any{"closed": closed})
}
