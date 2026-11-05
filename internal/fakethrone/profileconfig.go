package fakethrone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/url"
	"strconv"
	"strings"

	throne "github.com/throneproj/Throne-api"
)

// foreignFields are what only Throne's window may set in a configuration.
var foreignFields = []string{"otp_profile_id"}

// outbound returns the configuration of a profile, made up when first asked for; s.mu is
// held.
func (s *Server) outbound(p *throne.Profile) map[string]any {
	if o := s.outbounds[p.ID]; o != nil {
		return o
	}
	host, port := splitAddress(p.Address)
	o := map[string]any{"type": p.Type, "tag": p.Name, "server": host, "server_port": port}
	secret := fmt.Sprintf("%x", 0x5eed0000c0ffee+p.ID*7919)
	uuid := fmt.Sprintf("%08x-%04x-4%03x-8%03x-%012x", 0x1f2e3d4c+p.ID, p.ID*31%0x10000, p.ID%0x1000, p.ID*7%0x1000, 0xabc000000000+p.ID)
	tls := map[string]any{"enabled": true, "server_name": host}
	switch p.Type {
	case "vless":
		o["uuid"], o["flow"], o["packet_encoding"] = uuid, "xtls-rprx-vision", "xudp"
		o["tls"] = map[string]any{"enabled": true, "server_name": host, "utls": map[string]any{"enabled": true, "fingerprint": "chrome"}}
	case "vmess":
		o["uuid"], o["security"], o["alter_id"] = uuid, "auto", 0
		o["transport"], o["tls"] = map[string]any{"type": "ws", "path": "/ws"}, tls
	case "trojan", "hysteria2", "anytls":
		o["password"], o["tls"] = secret, tls
	case "tuic":
		o["uuid"], o["password"], o["congestion_control"], o["tls"] = uuid, secret, "bbr", tls
	case "shadowsocks":
		o["method"], o["password"] = "2022-blake3-aes-128-gcm", base64.StdEncoding.EncodeToString([]byte(secret + "0123456789")[:16])
	case "naive":
		o["username"], o["password"], o["tls"] = "user", secret, tls
	case "socks", "http":
		o["username"], o["password"] = "user", secret
	case "wireguard":
		o["private_key"] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%032s", secret)))
		o["peer_public_key"] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%032d", p.ID)))
		o["local_address"], o["mtu"] = []any{"10.7.0.2/32"}, 1420
	case "openvpn":
		o["proto"], o["username"], o["password"], o["otp_profile_id"] = "udp", "j.doe", secret, 2
	case "chain":
		hops := []any{}
		for _, g := range s.groups {
			for _, id := range g.profiles {
				if len(hops) < 2 && id != p.ID && s.profiles[id].Type == "vless" {
					hops = append(hops, id)
				}
			}
		}
		o = map[string]any{"type": p.Type, "tag": p.Name, "list": hops}
	case "autoselector":
		tracked := -1
		for _, g := range s.groups {
			if g.name == p.Address {
				tracked = g.id
			}
		}
		o = map[string]any{"type": p.Type, "tag": p.Name, "group": tracked, "test_interval": 300}
	case "extracore":
		o = map[string]any{"type": p.Type, "tag": p.Name, "core_path": "/opt/tool/tool", "args": "-c config.json"}
	}
	s.outbounds[p.ID] = o
	return o
}

func splitAddress(address string) (string, int) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address, 0
	}
	n, _ := strconv.Atoi(port)
	return host, n
}

// editable says whether the API may change a profile's configuration, and if not, why.
func editable(p *throne.Profile, o map[string]any) (bool, string) {
	if p.Type == "extracore" {
		return false, "Extra Core profiles run programs, so only Throne's window can edit them"
	}
	for _, f := range foreignFields {
		if _, set := o[f]; set {
			return false, fmt.Sprintf("It sets %s, which only Throne's window can set", f)
		}
	}
	return true, ""
}

// shareLink returns the share link of a configuration, "" for a type without one.
func shareLink(o map[string]any) string {
	str := func(k string) string {
		v, _ := o[k].(string)
		return v
	}
	port, _ := o["server_port"].(int)
	u := url.URL{Host: net.JoinHostPort(str("server"), strconv.Itoa(port)), Fragment: str("tag")}
	q := url.Values{}
	if tls, _ := o["tls"].(map[string]any); tls != nil {
		q.Set("security", "tls")
		if sni, _ := tls["server_name"].(string); sni != "" {
			q.Set("sni", sni)
		}
	}
	switch typ := str("type"); typ {
	case "vless", "vmess":
		u.Scheme, u.User = typ, url.User(str("uuid"))
		if flow := str("flow"); flow != "" {
			q.Set("flow", flow)
		}
	case "trojan", "hysteria2", "anytls":
		u.Scheme, u.User = typ, url.User(str("password"))
	case "tuic":
		u.Scheme, u.User = typ, url.UserPassword(str("uuid"), str("password"))
	case "shadowsocks":
		u.Scheme, u.User = "ss", url.User(base64.RawURLEncoding.EncodeToString([]byte(str("method")+":"+str("password"))))
	case "naive":
		u.Scheme, u.User = "naive+https", url.UserPassword(str("username"), str("password"))
	case "socks":
		u.Scheme, u.User = "socks5", url.UserPassword(str("username"), str("password"))
	case "http":
		u.Scheme, u.User = "http", url.UserPassword(str("username"), str("password"))
	default:
		return ""
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// fromLink reads a share link as a configuration of type typ, or returns nil.
func fromLink(typ, text string) map[string]any {
	e := parseLink(strings.TrimSpace(text))
	u, err := url.Parse(strings.TrimSpace(text))
	if e.severity == throne.SeverityError || e.profile.Type != typ || err != nil {
		return nil
	}
	host, port := splitAddress(e.profile.Address)
	o := map[string]any{"type": typ, "tag": e.profile.Name, "server": host, "server_port": port}
	if sni := u.Query().Get("sni"); sni != "" || u.Query().Get("security") != "" {
		o["tls"] = map[string]any{"enabled": true, "server_name": sni}
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	switch typ {
	case "vless", "vmess":
		o["uuid"] = user
	case "trojan", "hysteria2", "anytls":
		o["password"] = user
	case "tuic":
		o["uuid"], o["password"] = user, pass
	case "shadowsocks":
		b, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(user, "="))
		o["method"], o["password"], _ = strings.Cut(string(b), ":")
	default:
		o["username"], o["password"] = user, pass
	}
	return o
}

// checkOutbound returns what is wrong with a configuration, and the warnings, each
// "field: message".
func checkOutbound(o map[string]any) (problems, warnings []string) {
	for _, f := range foreignFields {
		if _, set := o[f]; set {
			problems = append(problems, f+": only Throne's window can set this")
		}
	}
	typ, _ := o["type"].(string)
	if typ != "chain" && typ != "autoselector" {
		if server, _ := o["server"].(string); strings.TrimSpace(server) == "" {
			problems = append(problems, "server: Enter the server's address")
		}
		if port, isInt := o["server_port"].(int); !isInt || port < 1 || port > 65535 {
			problems = append(problems, "server_port: The port must be 1 to 65535")
		}
	}
	need := map[string]string{"vless": "uuid", "vmess": "uuid", "tuic": "uuid", "trojan": "password", "hysteria2": "password",
		"anytls": "password", "shadowsocks": "password"}[typ]
	if v, _ := o[need].(string); need != "" && v == "" {
		problems = append(problems, need+": Enter the "+need)
	}
	if tag, _ := o["tag"].(string); strings.TrimSpace(tag) == "" {
		warnings = append(warnings, "tag: The profile has no name, so Throne names it after its server")
	}
	return problems, warnings
}

func (s *Server) getProfileConfig(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profiles[r.ids[0]]
	if p == nil {
		return notFound("No profile %d", r.ids[0])
	}
	o := s.outbound(p)
	raw, err := json.Marshal(o)
	if err != nil {
		return fail(500, "internal", "%v", err)
	}
	canEdit, reason := editable(p, o)
	return ok(throne.ProfileConfig{ID: p.ID, Type: p.Type, Name: p.Name, Outbound: raw, Link: shareLink(o), Editable: canEdit, Reason: reason})
}

func (s *Server) putProfileConfig(_ context.Context, r *request) *response {
	in, hasOutbound := r.fields["outbound"]
	hasOutbound = hasOutbound && in != nil
	text, hasLink, bad := stringField(r.fields, "link")
	switch {
	case bad != nil:
		return bad
	case hasOutbound && hasLink:
		return badRequest("Give outbound or link, not both")
	case !hasOutbound && !hasLink:
		return badRequest("Give outbound or link")
	}
	obj, isObject := in.(map[string]any)
	if hasOutbound && !isObject {
		return badRequest("outbound must be a JSON object")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profiles[r.ids[0]]
	if p == nil {
		return notFound("No profile %d", r.ids[0])
	}
	if canEdit, reason := editable(p, s.outbound(p)); !canEdit {
		return fail(403, "not_editable", "%s", reason)
	}
	if s.change != nil || s.testRunning() {
		return busy("A profile is starting or stopping, or a test is running")
	}
	next := maps.Clone(obj)
	if hasLink {
		if next = fromLink(p.Type, text); next == nil {
			return fail(422, "invalid_profile", "The link is not a %s link", p.Type)
		}
	}
	next["type"] = p.Type
	if port, isInt := integer(next["server_port"]); isInt {
		next["server_port"] = port
	}
	problems, warnings := checkOutbound(next)
	if len(problems) > 0 {
		return fail(422, "invalid_profile", "%s\n%s", summary("The configuration", len(problems)), strings.Join(problems, "\n"))
	}
	s.outbounds[p.ID] = next
	if host, port := next["server"], next["server_port"]; host != nil {
		p.Address = net.JoinHostPort(fmt.Sprint(host), fmt.Sprint(port))
	}
	p.Name, _ = next["tag"].(string)
	if strings.TrimSpace(p.Name) == "" {
		p.Name = p.Address
	}
	s.logf("[Throne] Changed the configuration of %s", p.Name)
	s.bump(throne.TopicProfiles)
	if s.state == "running" && s.running == p.ID && !s.restartNeeded {
		s.restartNeeded = true
		s.bump(throne.TopicStatus)
	}
	if warnings == nil {
		warnings = []string{}
	}
	return ok(map[string]any{"profile": s.view(p), "warnings": warnings, "restart_needed": s.restartNeeded})
}

// summary is the first line of a message that lists n problems of what.
func summary(what string, n int) string {
	if n == 1 {
		return what + " has a problem:"
	}
	return fmt.Sprintf("%s has %d problems:", what, n)
}
