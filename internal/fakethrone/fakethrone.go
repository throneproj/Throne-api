// Package fakethrone is an in-memory Throne for developing clients of the Throne API, such
// as the web UI of thronectl web, without a real Throne. It speaks the wire protocol and
// answers every request of spec/api.md plausibly, from made-up data that requests and time
// change: profiles start and stop, tests fill in results, traffic flows and the log grows.
// It reports itself as headless Throne and answers admin requests from every client.
//
// Run it with
//
//	go run ./internal/fakethrone/cmd -listen 127.0.0.1:19095 -key-file fake.key
package fakethrone

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/wire"
)

// Version is the version the fake reports in its Status.
const Version = "1.4.0+fakethrone"

const (
	maxLogLines   = 5000
	maxLogBytes   = 4 << 20
	maxLineLength = 8192
	// idle is how long a connection may wait for a request, as in Throne.
	idle = 60 * time.Second
)

// topics are the topics of GET /v1/events, in their order.
var topics = []string{throne.TopicStatus, throne.TopicGroups, throne.TopicProfiles, throne.TopicTests, throne.TopicSettings}

// Config configures a Server.
type Config struct {
	// Key is the key in its text form, as thronectl keygen prints it.
	Key string
	// Seed picks the made-up data: the same seed makes the same Throne.
	Seed uint64
	// Log, when not nil, gets every line of the fake's log too.
	Log io.Writer
}

// Server is a running fake Throne.
type Server struct {
	cfg  Config
	key  [wire.KeySize]byte
	ln   net.Listener
	quit chan struct{}
	wg   sync.WaitGroup

	mu      sync.Mutex
	rnd     *rand.Rand
	conns   map[net.Conn]struct{}
	closed  bool
	life    context.Context // ends when this run of the fake restarts
	endLife context.CancelFunc
	gen     int    // counts the runs, so that timers of an earlier run do nothing
	down    string // "", or the error code every request gets while the fake restarts

	instance      string
	groups        []*group
	profiles      map[int]*throne.Profile
	homes         map[int]string // the country of each profile's exit
	nextProfile   int
	nextGroup     int
	shown         int // the group Throne shows
	routes        []throne.Route
	activeRoute   int
	state         string
	running       int // the running profile, or -1
	last          int
	change        *op // the start or stop under way
	tun           bool
	killSwitch    string
	restartNeeded bool
	settings      map[string]any
	jobs          []*job // newest first
	nextJob       int
	live          throne.LiveStats
	hour0         int64 // the start of the first hour of the traffic series
	traffic       map[string]*series
	geo           []*geoFile
	geoUpdate     *op
	ruleSetUpdate *op
	backups       map[int]*backup
	uploads       map[int]*upload
	nextBackup    int
	nextUpload    int

	lines     []throne.LogLine
	lineBytes int
	seq       int64
	logWake   chan struct{}
	events    int64
	topicSeq  map[string]int64
	eventWake chan struct{}
}

type group struct {
	id         int
	name       string
	url        string // the subscription URL, "" for a plain group
	archived   bool
	autoUpdate bool
	updatedAt  int64
	profiles   []int
	update     *op // the subscription update under way
}

// op is work that ends later. Once done is closed, resp is its outcome. Its methods run
// with s.mu held.
type op struct {
	done   chan struct{}
	resp   *response
	result throne.UpdateResult // of a subscription update
	thens  []func()
}

func newOp() *op { return &op{done: make(chan struct{})} }

// end ends o with resp, then runs what waits for it.
func (o *op) end(resp *response) {
	o.resp = resp
	close(o.done)
	for _, f := range o.thens {
		f()
	}
}

// then runs f once o has ended.
func (o *op) then(f func()) {
	select {
	case <-o.done:
		f()
	default:
		o.thens = append(o.thens, f)
	}
}

type job struct {
	throne.Job
	ended *op
}

type series struct {
	id       *int // the profile, or nil for an app
	name     string
	up, down []int64 // bytes per hour from Server.hour0
}

type geoFile struct {
	name, url string
	present   bool
	size      int64
	updatedAt int64
}

type backup struct {
	id    int
	data  []byte
	sha   string
	parts []string
	built *op
	seen  time.Time
}

type upload struct {
	id      int
	size    int64
	sha     string
	data    []byte
	state   string
	archive *throne.Archive
	err     string
	checked *op
	seen    time.Time
}

// Start listens on addr and serves until Close.
func Start(addr string, cfg Config) (*Server, error) {
	k, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cfg.Key))
	if err != nil || len(k) != wire.KeySize {
		return nil, errors.New("fakethrone: the key is not 43 characters of base64url that decode to 32 bytes")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, ln: ln, quit: make(chan struct{}), conns: map[net.Conn]struct{}{},
		rnd: rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15))}
	copy(s.key[:], k)
	s.mu.Lock()
	s.seed()
	s.begin()
	s.logf("Throne %s (fakethrone, seed %d) started headless", Version, cfg.Seed)
	s.logf("[API] Listening on %s; fakethrone answers admin requests from every client", ln.Addr())
	s.logf("[Core] ThroneCore started, pid %d", 4000+s.rnd.IntN(5000))
	if p := s.profiles[s.running]; p != nil {
		s.logf("[Throne] Resuming %s", p.Name)
		s.logf("%s sing-box started (%.2fs)", level("INFO"), 0.2+s.rnd.Float64()/2)
	}
	s.mu.Unlock()
	s.wg.Add(2)
	go s.accept()
	go s.run()
	return s, nil
}

// Addr returns the address the fake listens on.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the fake and closes its connections.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.endLife()
	for nc := range s.conns {
		nc.Close()
	}
	s.mu.Unlock()
	close(s.quit)
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

// begin starts a run of the fake: a new instance, an empty log and no events.
func (s *Server) begin() {
	s.gen++
	s.life, s.endLife = context.WithCancel(context.Background())
	s.instance = fmt.Sprintf("%016x", s.rnd.Uint64())
	s.lines, s.lineBytes, s.seq = nil, 0, 0
	s.events, s.topicSeq = 0, map[string]int64{}
	s.logWake, s.eventWake = make(chan struct{}), make(chan struct{})
	s.jobs = nil
	s.backups, s.uploads = map[int]*backup{}, map[int]*upload{}
	s.change, s.geoUpdate, s.ruleSetUpdate = nil, nil, nil
	for _, g := range s.groups {
		g.update = nil
	}
	s.restartNeeded = false
}

// restart restarts the fake as Throne restarts: every connection drops, requests get code
// for a while, and then a new run starts the profile that ran.
func (s *Server) restart(code string) {
	resume := s.state == "running" || s.state == "connecting"
	s.logf("[Throne] Restarting")
	s.down = code
	s.state, s.running, s.change = "idle", -1, nil
	s.live = throne.LiveStats{}
	s.endLife()
	for nc := range s.conns {
		nc.Close()
	}
	s.gen++
	s.after(1500*time.Millisecond, func() {
		s.down = "starting"
		s.after(time.Second, func() {
			s.down = ""
			s.begin()
			s.logf("Throne %s (fakethrone) started headless after a restart", Version)
			s.logf("[Core] ThroneCore started, pid %d", 4000+s.rnd.IntN(5000))
			if resume && s.profiles[s.last] != nil {
				s.logf("[Throne] Resuming %s", s.profiles[s.last].Name)
				s.startProfile(s.last, time.Second)
			}
		})
	})
}

// after runs f with s.mu held after d, unless the fake restarted or closed meanwhile; s.mu
// is held.
func (s *Server) after(d time.Duration, f func()) {
	gen := s.gen
	time.AfterFunc(d, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen == gen && !s.closed {
			f()
		}
	})
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			nc.Close()
			return
		}
		s.conns[nc] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serve(nc)
	}
}

func (s *Server) serve(nc net.Conn) {
	defer s.wg.Done()
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
	client, _, _ := net.SplitHostPort(nc.RemoteAddr().String())
	for {
		nc.SetDeadline(time.Now().Add(idle))
		msg, err := wc.ReadMessage()
		if err != nil {
			return
		}
		nc.SetDeadline(time.Time{})
		resp := s.respond(msg, client)
		if resp == nil {
			return
		}
		if err := wc.WriteMessage(resp.encode()); err != nil {
			return
		}
	}
}

// run moves time along: traffic every second, and a line in the log every 0.5 to 2 s.
func (s *Server) run() {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	chatter := time.NewTimer(time.Second)
	defer chatter.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-tick.C:
			s.mu.Lock()
			s.flow()
			s.mu.Unlock()
		case <-chatter.C:
			s.mu.Lock()
			if s.down == "" {
				s.chatter()
			}
			d := 500*time.Millisecond + time.Duration(s.rnd.Int64N(int64(1500*time.Millisecond)))
			s.mu.Unlock()
			chatter.Reset(d)
		}
	}
}

type request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	Body   json.RawMessage   `json:"body"`

	client string
	ids    []int          // the {id} parts of the path
	fields map[string]any // the body
}

type response struct {
	status        int
	body          any
	code, message string
}

func ok(body any) *response       { return &response{status: 200, body: body} }
func accepted(body any) *response { return &response{status: 202, body: body} }

func fail(status int, code, format string, args ...any) *response {
	return &response{status: status, code: code, message: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...any) *response {
	return fail(400, "bad_request", format, args...)
}

func notFound(format string, args ...any) *response {
	return fail(404, "not_found", format, args...)
}

func busy(format string, args ...any) *response {
	return fail(409, "busy", format, args...)
}

func (r *response) encode() []byte {
	var v any
	if r.status >= 400 {
		v = map[string]any{"status": r.status, "error": map[string]string{"code": r.code, "message": r.message}}
	} else {
		v = map[string]any{"status": r.status, "body": r.body}
	}
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"status": 500, "error": map[string]string{"code": "internal", "message": err.Error()}})
	}
	return b
}

// respond answers a request; nil closes the connection without an answer.
func (s *Server) respond(msg []byte, client string) *response {
	var r request
	if err := json.Unmarshal(msg, &r); err != nil || r.Method == "" || !strings.HasPrefix(r.Path, "/") {
		return badRequest("The request is malformed")
	}
	r.client = client
	if len(r.Body) > 0 && string(r.Body) != "null" {
		if err := json.Unmarshal(r.Body, &r.fields); err != nil {
			return badRequest("The body must be a JSON object")
		}
	}
	s.mu.Lock()
	ctx, down := s.life, s.down
	s.mu.Unlock()
	switch down {
	case "restarting":
		return fail(503, down, "Throne is restarting")
	case "starting":
		return fail(503, down, "Throne is starting; try again in a moment")
	}
	return s.dispatch(ctx, &r)
}

// logf adds text to the log as Throne does: a line per line of it, empty ones left out,
// long ones cut. s.mu is held.
func (s *Server) logf(format string, args ...any) {
	now := time.Now().Unix()
	for line := range strings.SplitSeq(fmt.Sprintf(format, args...), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > maxLineLength {
			line = string([]rune(line)[:maxLineLength-1]) + "…"
		}
		s.seq++
		s.lines = append(s.lines, throne.LogLine{Seq: s.seq, Time: now, Text: line})
		s.lineBytes += len(line)
		if s.cfg.Log != nil {
			fmt.Fprintln(s.cfg.Log, line)
		}
	}
	for len(s.lines) > maxLogLines || s.lineBytes > maxLogBytes {
		s.lineBytes -= len(s.lines[0].Text)
		s.lines = s.lines[1:]
	}
	wake(&s.logWake)
}

// bump records a change of the topics for GET /v1/events. s.mu is held.
func (s *Server) bump(names ...string) {
	s.events++
	for _, n := range names {
		s.topicSeq[n] = s.events
	}
	wake(&s.eventWake)
}

func wake(ch *chan struct{}) {
	close(*ch)
	*ch = make(chan struct{})
}

// hold waits until ch closes or deadline passes, with s.mu unlocked meanwhile. It returns
// false when ctx ended first, as when the fake restarts.
func (s *Server) hold(ctx context.Context, ch chan struct{}, deadline time.Time) bool {
	s.mu.Unlock()
	defer s.mu.Lock()
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-ctx.Done():
		return false
	}
	return true
}

// await waits up to wait for o to end, with s.mu unlocked meanwhile, and says whether it
// did.
func (s *Server) await(ctx context.Context, o *op, wait time.Duration) bool {
	s.mu.Unlock()
	defer s.mu.Lock()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-o.done:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	select {
	case <-o.done:
		return true
	default:
		return false
	}
}

func (s *Server) getLogs(ctx context.Context, r *request) *response {
	limit, bad := queryInt(r, "limit", 200, 1, 1000)
	if bad != nil {
		return bad
	}
	after, bad := queryInt(r, "after", 0, 0, -1)
	if bad != nil {
		return bad
	}
	timeout, bad := queryInt(r, "timeout", 0, 0, 300)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Query["instance"] != s.instance {
		page := s.lines[max(0, len(s.lines)-int(limit)):]
		var cursor int64
		if len(page) > 0 {
			cursor = page[len(page)-1].Seq
		}
		return ok(logsBody(s.instance, cursor, false, 0, page))
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		i := sort.Search(len(s.lines), func(i int) bool { return s.lines[i].Seq > after })
		page := s.lines[i:]
		if len(page) > 0 || !time.Now().Before(deadline) {
			more := len(page) > int(limit)
			page = page[:min(len(page), int(limit))]
			cursor := after
			if len(page) > 0 {
				cursor = page[len(page)-1].Seq
			}
			dropped := s.seq - after
			if len(s.lines) > 0 {
				dropped = s.lines[0].Seq - after - 1
			}
			return ok(logsBody(s.instance, cursor, more, max(dropped, 0), page))
		}
		if !s.hold(ctx, s.logWake, deadline) {
			return nil
		}
	}
}

func logsBody(instance string, cursor int64, more bool, dropped int64, lines []throne.LogLine) map[string]any {
	return map[string]any{"instance": instance, "cursor": cursor, "more": more, "dropped": dropped,
		"lines": append([]throne.LogLine{}, lines...)}
}

func (s *Server) getEvents(ctx context.Context, r *request) *response {
	after, bad := queryInt(r, "after", 0, 0, -1)
	if bad != nil {
		return bad
	}
	timeout, bad := queryInt(r, "timeout", 0, 0, 300)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Query["instance"] != s.instance {
		return ok(map[string]any{"instance": s.instance, "cursor": s.events, "changes": topics})
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	held := false
	for {
		changes := s.changesAfter(after)
		if len(changes) > 0 && held {
			// Gather the changes that come with the first one.
			if !s.hold(ctx, nil, time.Now().Add(200*time.Millisecond)) {
				return nil
			}
			changes = s.changesAfter(after)
		}
		if len(changes) > 0 || !time.Now().Before(deadline) {
			return ok(map[string]any{"instance": s.instance, "cursor": s.events, "changes": changes})
		}
		held = true
		if !s.hold(ctx, s.eventWake, deadline) {
			return nil
		}
	}
}

func (s *Server) changesAfter(after int64) []string {
	changes := []string{}
	for _, t := range topics {
		if s.topicSeq[t] > after {
			changes = append(changes, t)
		}
	}
	return changes
}

// queryInt reads an integer query parameter from lo to hi, or lo and more for a hi below
// lo; def when the parameter is missing.
func queryInt(r *request, name string, def, lo, hi int64) (int64, *response) {
	v, present := r.Query[name]
	if !present {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	switch {
	case hi < lo && (err != nil || n < lo):
		return 0, badRequest("%s must be an integer of %d or more", name, lo)
	case hi >= lo && (err != nil || n < lo || n > hi):
		return 0, badRequest("%s must be an integer from %d to %d", name, lo, hi)
	}
	return n, nil
}

var places = []struct{ cc, city string }{
	{"DE", "Frankfurt"}, {"NL", "Amsterdam"}, {"FI", "Helsinki"}, {"US", "New York"}, {"US", "Los Angeles"},
	{"GB", "London"}, {"FR", "Paris"}, {"SE", "Stockholm"}, {"JP", "Tokyo"}, {"SG", "Singapore"}, {"TR", "Istanbul"},
	{"CA", "Toronto"}, {"PL", "Warsaw"}, {"CH", "Zurich"}, {"AT", "Vienna"}, {"HK", "Hong Kong"}, {"AE", "Dubai"},
	{"KZ", "Almaty"},
}

var protocols = []string{"vless", "vless", "vless", "vmess", "trojan", "trojan", "shadowsocks", "shadowsocks",
	"hysteria2", "tuic", "anytls", "wireguard"}

var apps = []string{"firefox", "chrome", "Telegram", "Discord", "steam", "Spotify", "code", "curl", "git"}

var sites = []string{"www.google.com", "github.com", "api.github.com", "www.youtube.com", "i.ytimg.com",
	"rr3---sn-4g5e6nsz.googlevideo.com", "discord.com", "gateway.discord.gg", "web.telegram.org", "en.wikipedia.org",
	"cdn.jsdelivr.net", "update.googleapis.com", "www.reddit.com", "x.com", "open.spotify.com", "store.steampowered.com"}

var directSites = []string{"www.baidu.com", "qq.com", "aparat.com", "digikala.com", "time.windows.com", "192.168.1.1"}

// flag returns the flag emoji of a country code.
func flag(cc string) string {
	return string(rune(0x1F1E6+rune(cc[0]-'A'))) + string(rune(0x1F1E6+rune(cc[1]-'A')))
}

// level returns a log level as the core prints it, in color.
func level(name string) string {
	color := map[string]string{"DEBUG": "37", "INFO": "36", "WARN": "33", "ERROR": "31"}[name]
	return "\x1b[" + color + "m" + name + "\x1b[0m"
}

func (s *Server) addGroup(name, link string) *group {
	g := &group{id: s.nextGroup, name: name, url: link, autoUpdate: true}
	s.nextGroup++
	s.groups = append(s.groups, g)
	return g
}

func (s *Server) addProfile(g *group, e entry) *throne.Profile {
	p := e.profile
	p.ID, p.Group = s.nextProfile, g.id
	s.nextProfile++
	s.profiles[p.ID] = &p
	s.homes[p.ID] = e.home
	if e.home == "" {
		s.homes[p.ID] = places[s.rnd.IntN(len(places))].cc
	}
	g.profiles = append(g.profiles, p.ID)
	return &p
}

// madeUp makes up a profile of a provider.
func (s *Server) madeUp() entry {
	pl := places[s.rnd.IntN(len(places))]
	n := 1 + s.rnd.IntN(20)
	return entry{home: pl.cc, profile: throne.Profile{
		Name:    fmt.Sprintf("%s %s %02d", flag(pl.cc), pl.city, n),
		Type:    protocols[s.rnd.IntN(len(protocols))],
		Address: fmt.Sprintf("%s-%02d.provider.example.net:%d", strings.ToLower(pl.cc), n, []int{443, 443, 8443, 2053}[s.rnd.IntN(4)]),
	}}
}

// seed makes up the data the fake starts with. s.mu is held.
func (s *Server) seed() {
	now := time.Now().Unix()
	s.profiles, s.homes, s.traffic = map[int]*throne.Profile{}, map[int]string{}, map[string]*series{}
	s.running, s.last, s.state, s.killSwitch = -1, -1, "idle", "off"

	def := s.addGroup("Default", "")
	for i, pl := range places[:12] {
		s.addProfile(def, entry{home: pl.cc, profile: throne.Profile{Name: pl.cc + " " + pl.city,
			Type: protocols[s.rnd.IntN(len(protocols))], Address: fmt.Sprintf("%s%d.example.com:443", strings.ToLower(pl.cc), i+1)}})
	}
	provider := s.addGroup("Provider", "https://sub.example.com/api/v1/client/subscribe?token=fake")
	provider.updatedAt = now - 3*3600 - s.rnd.Int64N(3600)
	for range 30 {
		s.addProfile(provider, s.madeUp())
	}
	work := s.addGroup("Work", "")
	for _, w := range []struct{ name, typ, addr, cc string }{
		{"Office VPN", "openvpn", "vpn.corp.example.com:1194", "DE"},
		{"Office WireGuard", "wireguard", "203.0.113.10:51820", "DE"},
		{"Office via DE", "chain", "de1.example.com:443", "DE"},
		{"Corporate SOCKS", "socks", "10.0.0.5:1080", "NL"},
		{"Corporate HTTP proxy", "http", "proxy.corp.example.com:3128", "NL"},
		{"Auto (Provider)", "autoselector", "Provider", ""},
		{"Lab (broken config)", "vless", "lab.example.org:8443", "US"},
		{"Naive US", "naive", "naive.example.org:443", "US"},
		{"Hysteria JP", "hysteria2", "jp.example.org:443", "JP"},
		{"AnyTLS SG", "anytls", "sg.example.org:443", "SG"},
	} {
		s.addProfile(work, entry{home: w.cc, profile: throne.Profile{Name: w.name, Type: w.typ, Address: w.addr}})
	}
	old := s.addGroup("Old provider", "https://old.example.net/sub/fake")
	old.archived, old.updatedAt = true, now-40*86400
	for range 8 {
		s.addProfile(old, s.madeUp())
	}
	for _, g := range s.groups {
		for _, id := range g.profiles {
			p := s.profiles[id]
			if p.Type == "autoselector" {
				continue
			}
			x := s.rnd.Float64()
			if x < 0.6 {
				s.measure(throne.TestLatency, p)
				p.LatencyAt -= s.rnd.Int64N(7200)
			}
			if x < 0.35 {
				s.measure(throne.TestIP, p)
			}
			if x < 0.2 {
				s.measure(throne.TestSpeed, p)
			}
		}
	}
	s.shown = def.id

	s.routes = []throne.Route{{ID: 0, Name: "Default"}, {ID: 1, Name: "Bypass LAN and China"}, {ID: 2, Name: "Global"},
		{ID: 3, Name: "Iran direct"}}
	s.settings = map[string]any{
		"remember_enable": true, "log_level": "info", "xray_log_level": "warning", "inbound_enabled": true,
		"inbound_address": "127.0.0.1", "inbound_port": 2080, "test_url": "https://www.gstatic.com/generate_204",
		"test_concurrency": 10, "test_timeout_ms": 0, "sub_auto_update": 360, "sub_respect_server_interval": true,
		"route_auto_update": 0, "kill_switch": false, "tun_strict_route": true, "tun_auto_redirect": false,
		"tun_mtu": 9000, "tun_ipv6": false, "tun_ipv4_cidr": "172.19.0.1/24", "tun_ipv6_cidr": "fdfe:dcba:9876::1/96",
		"dns_in_port": 5533, "clash_api_port": 0, "clash_listen_addr": "", "singbox_api_port": 0,
		"remote_dns": "https://8.8.8.8/dns-query", "direct_dns": "localhost", "ntp_enabled": false, "ntp_server": "",
		"ntp_interval": "30m",
	}
	s.geo = []*geoFile{
		{name: "geoip.dat", url: "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geoip.dat",
			present: true, size: 19_000_000 + s.rnd.Int64N(1_000_000), updatedAt: now - 12*86400},
		{name: "geosite.dat", url: "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat",
			present: true, size: 8_000_000 + s.rnd.Int64N(1_000_000), updatedAt: now - 12*86400},
	}

	s.hour0 = (now/3600 - 30*24) * 3600
	used := append(append(slices.Clone(provider.profiles[:8]), def.profiles[:4]...), work.profiles[0])
	for _, id := range used {
		p := s.profiles[id]
		sr := s.history(&p.ID, p.Name, 20_000_000+s.rnd.Int64N(400_000_000))
		s.traffic[fmt.Sprintf("profile:%d", id)] = sr
		for h := range sr.up {
			p.TrafficUp += sr.up[h]
			p.TrafficDown += sr.down[h]
		}
	}
	direct := -101
	s.traffic["profile:-101"] = s.history(&direct, "Direct", 30_000_000)
	for _, app := range append(slices.Clone(apps), "Speedtest") {
		s.traffic["app:"+app] = s.history(nil, app, 5_000_000+s.rnd.Int64N(300_000_000))
	}

	// Like headless Throne, the fake resumes the profile that ran.
	s.state, s.running, s.last = "running", provider.profiles[2], provider.profiles[2]
}

// history makes up the hourly traffic of the last 30 days, busier in the evening.
func (s *Server) history(id *int, name string, perHour int64) *series {
	hours := int((time.Now().Unix()-s.hour0)/3600) + 1
	sr := &series{id: id, name: name, up: make([]int64, hours), down: make([]int64, hours)}
	for h := range hours {
		if s.rnd.IntN(3) == 0 {
			continue
		}
		hourOfDay := (s.hour0/3600 + int64(h)) % 24
		busy := 0.2 + 0.8*float64(min(hourOfDay, 24-hourOfDay))/12
		sr.down[h] = int64(float64(perHour) * busy * (0.2 + s.rnd.Float64()))
		sr.up[h] = sr.down[h] / int64(5+s.rnd.IntN(15))
	}
	return sr
}

// addTraffic counts traffic in the current hour of a series. s.mu is held.
func (s *Server) addTraffic(key string, id *int, name string, up, down int64) {
	h := int((time.Now().Unix() - s.hour0) / 3600)
	sr := s.traffic[key]
	if sr == nil {
		sr = &series{id: id, name: name}
		s.traffic[key] = sr
	}
	for len(sr.up) <= h {
		sr.up, sr.down = append(sr.up, 0), append(sr.down, 0)
	}
	sr.up[h] += up
	sr.down[h] += down
}

// flow moves a second's traffic through the running profile. s.mu is held.
func (s *Server) flow() {
	p := s.profiles[s.running]
	if s.state != "running" || s.down != "" || p == nil {
		s.live = throne.LiveStats{}
		return
	}
	walk := func(v, hi int64) int64 {
		if s.rnd.IntN(12) == 0 {
			return s.rnd.Int64N(hi)
		}
		return min(max(v+s.rnd.Int64N(hi/5+1)-hi/10, hi/200), hi)
	}
	s.live.Proxy.Down = walk(s.live.Proxy.Down, 6_000_000)
	s.live.Proxy.Up = walk(s.live.Proxy.Up, 600_000)
	s.live.Direct.Down = walk(s.live.Direct.Down, 250_000)
	s.live.Direct.Up = walk(s.live.Direct.Up, 30_000)
	p.TrafficUp += s.live.Proxy.Up
	p.TrafficDown += s.live.Proxy.Down
	id, direct := p.ID, -101
	s.addTraffic(fmt.Sprintf("profile:%d", id), &id, p.Name, s.live.Proxy.Up, s.live.Proxy.Down)
	s.addTraffic("profile:-101", &direct, "Direct", s.live.Direct.Up, s.live.Direct.Down)
	app := apps[s.rnd.IntN(len(apps))]
	s.addTraffic("app:"+app, nil, app, s.live.Proxy.Up+s.live.Direct.Up, s.live.Proxy.Down+s.live.Direct.Down)
}

// chatter adds a line to the log, as the core and Throne write them. s.mu is held.
func (s *Server) chatter() {
	p := s.profiles[s.running]
	if s.state != "running" || p == nil {
		lines := []string{
			"[Throne] No profile runs",
			fmt.Sprintf("%s dns: cache holds %d entries", level("DEBUG"), s.rnd.IntN(300)),
			"[Updater] No update available",
			fmt.Sprintf("[Subscription] Provider: next automatic update in %dh %dm", s.rnd.IntN(6), s.rnd.IntN(60)),
			"[Throne] Saved the traffic statistics",
		}
		s.logf("%s", lines[s.rnd.IntN(len(lines))])
		return
	}
	id, ms, port := 100000000+s.rnd.IntN(900000000), s.rnd.IntN(400), 49152+s.rnd.IntN(16000)
	site, out := sites[s.rnd.IntN(len(sites))], p.Type
	switch x := s.rnd.IntN(100); {
	case x < 40:
		s.logf("%s [%d 0ms] inbound/mixed[mixed-in]: inbound connection from 127.0.0.1:%d\n"+
			"%s [%d %dms] outbound/%s[proxy]: outbound connection to %s:443", level("INFO"), id, port, level("INFO"), id, ms, out, site)
	case x < 60:
		s.logf("%s [%d %dms] dns: exchanged A %s %ds", level("DEBUG"), id, ms, site, 30+s.rnd.IntN(600))
	case x < 75:
		d := directSites[s.rnd.IntN(len(directSites))]
		s.logf("%s [%d %dms] router: found rule[%d] rule_set=[geosite-cn geoip-cn] => route(direct)\n"+
			"%s [%d %dms] outbound/direct[direct]: outbound connection to %s:443", level("DEBUG"), id, ms, 2+s.rnd.IntN(6),
			level("INFO"), id, ms, d)
	case x < 85 && s.tun:
		s.logf("%s [%d 0ms] inbound/tun[tun-in]: inbound packet connection from 172.19.0.1:%d\n"+
			"%s [%d %dms] outbound/%s[proxy]: outbound packet connection to 8.8.8.8:53", level("INFO"), id, port, level("INFO"), id, ms, out)
	case x < 93:
		s.logf("%s [%d %dms] outbound/%s[proxy]: dial tcp %s:443: i/o timeout", level("WARN"), id, 5000+ms, out, site)
	case x < 99:
		s.logf("%s [%d %dms] connection: open connection to %s:443 using outbound/%s[proxy]: EOF", level("ERROR"), id, ms, site, out)
	default:
		var b strings.Builder
		for i := 0; b.Len() < 9000; i++ {
			fmt.Fprintf(&b, "domain_suffix=%s,", sites[i%len(sites)])
		}
		s.logf("%s router: rule-set geosite-ads loaded: %s", level("DEBUG"), b.String())
	}
}
