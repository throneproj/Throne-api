package fakethrone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
)

type route struct {
	method, pattern string
	handle          func(s *Server, ctx context.Context, r *request) *response
}

var routes = []route{
	{"GET", "/v1/status", (*Server).getStatus},
	{"GET", "/v1/groups", (*Server).getGroups},
	{"POST", "/v1/groups", (*Server).addGroupRequest},
	{"POST", "/v1/groups/update", (*Server).updateGroups},
	{"POST", "/v1/groups/{id}/update", (*Server).updateGroup},
	{"DELETE", "/v1/groups/{id}", (*Server).deleteGroup},
	{"GET", "/v1/profiles", (*Server).getProfiles},
	{"DELETE", "/v1/profiles", (*Server).deleteProfiles},
	{"GET", "/v1/profiles/{id}", (*Server).getProfile},
	{"POST", "/v1/import", (*Server).importProfiles},
	{"PUT", "/v1/connection", (*Server).connect},
	{"DELETE", "/v1/connection", (*Server).disconnect},
	{"PUT", "/v1/modes", (*Server).setModes},
	{"GET", "/v1/routes", (*Server).getRoutes},
	{"PUT", "/v1/routes/active", (*Server).setRoute},
	{"POST", "/v1/tests", (*Server).startTest},
	{"GET", "/v1/tests", (*Server).getJobs},
	{"GET", "/v1/tests/{id}", (*Server).getJob},
	{"DELETE", "/v1/tests/{id}", (*Server).cancelJob},
	{"DELETE", "/v1/results", (*Server).clearResults},
	{"GET", "/v1/stats/live", (*Server).getLive},
	{"GET", "/v1/stats/traffic", (*Server).getTraffic},
	{"GET", "/v1/settings", (*Server).getSettings},
	{"PUT", "/v1/settings", (*Server).putSettings},
	{"POST", "/v1/rule-sets/update", (*Server).updateRuleSets},
	{"GET", "/v1/geo-assets", (*Server).getGeo},
	{"POST", "/v1/geo-assets/update", (*Server).updateGeo},
	{"POST", "/v1/backups", (*Server).createBackup},
	{"GET", "/v1/backups/{id}", (*Server).readBackup},
	{"DELETE", "/v1/backups/{id}", (*Server).deleteBackup},
	{"POST", "/v1/restores", (*Server).openUpload},
	{"PUT", "/v1/restores/{id}", (*Server).putChunk},
	{"GET", "/v1/restores/{id}", (*Server).getUpload},
	{"DELETE", "/v1/restores/{id}", (*Server).deleteUpload},
	{"POST", "/v1/restores/{id}/apply", (*Server).applyRestore},
	{"POST", "/v1/restart", (*Server).restartRequest},
	{"POST", "/v1/shutdown", (*Server).shutdownRequest},
	{"GET", "/v1/logs", (*Server).getLogs},
	{"GET", "/v1/events", (*Server).getEvents},
}

func (s *Server) dispatch(ctx context.Context, r *request) *response {
	known := false
	for _, rt := range routes {
		ids, ok := match(rt.pattern, r.Path)
		if !ok {
			continue
		}
		known = true
		if rt.method != r.Method {
			continue
		}
		r.ids = ids
		if r.Method != "GET" {
			s.mu.Lock()
			s.logf("[API] %s %s from %s", r.Method, r.Path, r.client)
			s.mu.Unlock()
		}
		return rt.handle(s, ctx, r)
	}
	if known {
		return fail(405, "method_not_allowed", "%s is not allowed on %s", r.Method, r.Path)
	}
	return notFound("Unknown path %s", r.Path)
}

// match matches a path against a pattern whose {id} parts take integers.
func match(pattern, path string) ([]int, bool) {
	ps, xs := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(ps) != len(xs) {
		return nil, false
	}
	var ids []int
	for i, p := range ps {
		if p == "{id}" {
			id, err := strconv.Atoi(xs[i])
			if err != nil {
				return nil, false
			}
			ids = append(ids, id)
		} else if p != xs[i] {
			return nil, false
		}
	}
	return ids, true
}

func integer(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || math.Abs(f) > 1<<31 {
		return 0, false
	}
	return int(f), true
}

func intField(f map[string]any, name string) (int, bool, *response) {
	v, present := f[name]
	if !present || v == nil {
		return 0, false, nil
	}
	n, ok := integer(v)
	if !ok {
		return 0, true, badRequest("%s must be an integer", name)
	}
	return n, true, nil
}

func boolField(f map[string]any, name string, def bool) (bool, *response) {
	v, present := f[name]
	if !present || v == nil {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, badRequest("%s must be true or false", name)
	}
	return b, nil
}

func stringField(f map[string]any, name string) (string, bool, *response) {
	v, present := f[name]
	if !present || v == nil {
		return "", false, nil
	}
	str, ok := v.(string)
	if !ok {
		return "", true, badRequest("%s must be a string", name)
	}
	return str, true, nil
}

// waitTime reads the waiting fields: how long the request waits, def unless the body says
// otherwise, and 0 for "wait": false.
func waitTime(f map[string]any, def time.Duration) (time.Duration, *response) {
	wait, bad := boolField(f, "wait", true)
	if bad != nil || !wait {
		return 0, bad
	}
	t, present, bad := intField(f, "timeout")
	switch {
	case bad != nil:
		return 0, bad
	case present && (t < 1 || t > 300):
		return 0, badRequest("timeout must be 1 to 300 seconds")
	case present:
		return time.Duration(t) * time.Second, nil
	}
	return def, nil
}

// waited answers a request that waited for o: o's response, or when the wait ended first,
// a 202 with early. A nil response drops the connection, after the fake restarted.
func (s *Server) waited(ctx context.Context, o *op, wait time.Duration, early func() any) *response {
	if s.await(ctx, o, wait) {
		return o.resp
	}
	if ctx.Err() != nil {
		return nil
	}
	return accepted(early())
}

func nameFilter(name string) (*regexp.Regexp, *response) {
	if name == "" {
		return nil, nil
	}
	re, err := regexp.Compile("(?i)" + name)
	if err != nil {
		return nil, badRequest("name is not a valid regular expression: %v", err)
	}
	return re, nil
}

// selection reads the profiles a body selects. s.mu is held.
func (s *Server) selection(f map[string]any) ([]int, *response) {
	list, hasIDs := f["profiles"]
	gid, hasGroup, bad := intField(f, "group")
	if bad != nil {
		return nil, bad
	}
	name, _, bad := stringField(f, "name")
	if bad != nil {
		return nil, bad
	}
	var ids []int
	switch {
	case hasIDs && list != nil:
		if hasGroup || name != "" {
			return nil, badRequest("Select profiles with profiles, or with group and name, not both")
		}
		items, ok := list.([]any)
		if !ok {
			return nil, badRequest("profiles must be a list of profile IDs")
		}
		for _, v := range items {
			id, ok := integer(v)
			switch {
			case !ok:
				return nil, badRequest("profiles must be a list of profile IDs")
			case s.profiles[id] == nil:
				return nil, notFound("No profile %d", id)
			case !slices.Contains(ids, id):
				ids = append(ids, id)
			}
		}
	case hasGroup:
		g := s.group(gid)
		if g == nil {
			return nil, notFound("No group %d", gid)
		}
		re, bad := nameFilter(name)
		if bad != nil {
			return nil, bad
		}
		for _, id := range g.profiles {
			if re == nil || re.MatchString(s.profiles[id].Name) {
				ids = append(ids, id)
			}
		}
	default:
		return nil, badRequest("Select profiles with profiles, or with group and name")
	}
	if len(ids) == 0 {
		return nil, fail(400, "no_profiles", "The selection matched no profile")
	}
	return ids, nil
}

func (s *Server) group(id int) *group {
	for _, g := range s.groups {
		if g.id == id {
			return g
		}
	}
	return nil
}

func (s *Server) view(p *throne.Profile) throne.Profile {
	v := *p
	v.Running = s.state == "running" && s.running == p.ID
	return v
}

func (s *Server) groupView(g *group) throne.Group {
	return throne.Group{ID: g.id, Name: g.name, Subscription: g.url != "", Archived: g.archived,
		ProfileCount: len(g.profiles), UpdatedAt: g.updatedAt}
}

func (s *Server) status() map[string]any {
	var profile, route any
	if p := s.profiles[s.running]; p != nil && s.state == "running" {
		profile = s.view(p)
	}
	for _, rt := range s.routes {
		if rt.ID == s.activeRoute {
			route = throne.Route{ID: rt.ID, Name: rt.Name, Active: true}
		}
	}
	return map[string]any{
		"state": s.state, "profile": profile, "last_profile_id": s.last, "tun": s.tun, "tun_conflict": nil,
		"system_proxy": false, "route": route, "kill_switch": s.killSwitch, "kill_switch_error": nil, "core": true,
		"inbound": map[string]any{"enabled": s.settings["inbound_enabled"], "address": s.settings["inbound_address"],
			"port": s.settings["inbound_port"]},
		"version":        Version,
		"capabilities":   map[string]any{"tun": true, "kill_switch": true, "headless": true},
		"restart_needed": s.restartNeeded, "degraded": nil,
	}
}

func (s *Server) getStatus(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(s.status())
}

func (s *Server) getGroups(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	groups := []throne.Group{}
	for _, g := range s.groups {
		groups = append(groups, s.groupView(g))
	}
	return ok(map[string]any{"groups": groups})
}

func (s *Server) getProfiles(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	gid := -1
	if v, present := r.Query["group"]; present {
		id, err := strconv.Atoi(v)
		if err != nil {
			return badRequest("group must be an integer")
		}
		if s.group(id) == nil {
			return notFound("No group %d", id)
		}
		gid = id
	}
	re, bad := nameFilter(r.Query["name"])
	if bad != nil {
		return bad
	}
	var only []int
	if v, present := r.Query["ids"]; present {
		for part := range strings.SplitSeq(v, ",") {
			id, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return badRequest("ids must be comma-separated profile IDs")
			}
			only = append(only, id)
		}
	}
	profiles := []throne.Profile{}
	for _, g := range s.groups {
		for _, id := range g.profiles {
			p := s.profiles[id]
			if (gid < 0 || g.id == gid) && (only == nil || slices.Contains(only, id)) && (re == nil || re.MatchString(p.Name)) {
				profiles = append(profiles, s.view(p))
			}
		}
	}
	return ok(map[string]any{"profiles": profiles})
}

func (s *Server) getProfile(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.profiles[r.ids[0]]
	if p == nil {
		return notFound("No profile %d", r.ids[0])
	}
	return ok(s.view(p))
}

// startProfile starts a profile, which runs after d; s.mu is held.
func (s *Server) startProfile(id int, d time.Duration) *op {
	o := newOp()
	s.state, s.running, s.change, s.last = "connecting", -1, o, id
	s.live = throne.LiveStats{}
	s.logf("[Throne] Starting %s", s.profiles[id].Name)
	s.bump(throne.TopicStatus, throne.TopicProfiles)
	s.after(d, func() {
		s.change = nil
		p := s.profiles[id]
		if p == nil || strings.Contains(strings.ToLower(p.Name), "broken") {
			s.state = "idle"
			s.logf("%s [Throne] The core rejected the configuration: outbound/vless[proxy]: missing uuid", level("ERROR"))
			s.bump(throne.TopicStatus)
			o.end(fail(422, "start_failed", "The core rejected the configuration: outbound/vless[proxy]: missing uuid"))
			return
		}
		s.state, s.running, s.restartNeeded = "running", id, false
		s.logf("%s sing-box started (%.2fs)", level("INFO"), d.Seconds())
		s.logf("[Throne] Running %s (%s, %s)", p.Name, p.Type, p.Address)
		s.bump(throne.TopicStatus, throne.TopicProfiles)
		o.end(ok(s.status()))
	})
	return o
}

// stopProfile stops the running profile; s.mu is held.
func (s *Server) stopProfile() *op {
	o := newOp()
	name := ""
	if p := s.profiles[s.running]; p != nil {
		name = p.Name
	}
	s.state, s.change = "stopping", o
	s.bump(throne.TopicStatus)
	s.after(600*time.Millisecond, func() {
		s.state, s.running, s.change = "idle", -1, nil
		s.live = throne.LiveStats{}
		s.logf("[Throne] Stopped %s", name)
		s.bump(throne.TopicStatus, throne.TopicProfiles)
		o.end(ok(s.status()))
	})
	return o
}

// restartRunning restarts the running profile to apply a change, or returns nil when none
// runs; s.mu is held.
func (s *Server) restartRunning() *op {
	if s.state != "running" {
		return nil
	}
	return s.startProfile(s.running, 900*time.Millisecond)
}

func (s *Server) connect(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 30*time.Second)
	if bad != nil {
		return bad
	}
	restart, bad := boolField(r.fields, "restart", false)
	if bad != nil {
		return bad
	}
	id, hasID, bad := intField(r.fields, "profile")
	if bad != nil {
		return bad
	}
	sel, hasSel, bad := stringField(r.fields, "select")
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.change != nil {
		return busy("Another profile is starting or stopping")
	}
	extra := time.Duration(0)
	switch {
	case hasID == hasSel:
		return badRequest("Give profile or select")
	case hasID && s.profiles[id] == nil:
		return notFound("No profile %d", id)
	case hasID:
	case sel == "last":
		if id = s.last; s.profiles[id] == nil {
			return notFound("There is no profile to resume")
		}
	case sel == "lowest_latency":
		var resp *response
		if id, extra, resp = s.lowestLatency(r.fields); resp != nil {
			return resp
		}
	default:
		return badRequest("select must be last or lowest_latency")
	}
	if g := s.group(s.profiles[id].Group); g != nil && g.archived {
		return fail(409, "group_archived", "The group %s is archived", g.name)
	}
	if s.state == "running" && s.running == id && !restart {
		return ok(s.status())
	}
	o := s.startProfile(id, 700*time.Millisecond+time.Duration(s.rnd.IntN(700))*time.Millisecond+extra)
	return s.waited(ctx, o, wait, func() any { return s.status() })
}

// lowestLatency picks the profile to start for "select": "lowest_latency", testing the
// candidates without a recent result first; it returns how long that test takes.
func (s *Server) lowestLatency(f map[string]any) (int, time.Duration, *response) {
	gid, hasGroup, bad := intField(f, "group")
	if bad != nil {
		return 0, 0, bad
	}
	if !hasGroup {
		gid = s.shown
	}
	name, _, bad := stringField(f, "name")
	if bad != nil {
		return 0, 0, bad
	}
	maxAge, hasAge, bad := intField(f, "max_age")
	if bad != nil {
		return 0, 0, bad
	}
	if !hasAge {
		maxAge = 3600
	}
	g := s.group(gid)
	if g == nil {
		return 0, 0, notFound("No group %d", gid)
	}
	if g.archived {
		return 0, 0, fail(409, "group_archived", "The group %s is archived", g.name)
	}
	re, bad := nameFilter(name)
	if bad != nil {
		return 0, 0, bad
	}
	best, tested, candidates := -1, 0, 0
	now := time.Now().Unix()
	for _, id := range g.profiles {
		p := s.profiles[id]
		if p.Type == "autoselector" || re != nil && !re.MatchString(p.Name) {
			continue
		}
		candidates++
		if p.LatencyAt == 0 || now-p.LatencyAt >= int64(maxAge) {
			s.measure(throne.TestLatency, p)
			tested++
		}
		if p.LatencyMS > 0 && (best < 0 || p.LatencyMS < s.profiles[best].LatencyMS) {
			best = id
		}
	}
	if tested > 0 {
		s.logf("[Test] Tested the latency of %d profiles of %s", tested, g.name)
		s.bump(throne.TopicProfiles)
	}
	switch {
	case best >= 0:
		return best, time.Duration(min(tested, 20)) * 100 * time.Millisecond, nil
	case candidates == 0:
		return 0, 0, fail(400, "no_profiles", "The selection matched no profile")
	}
	return 0, 0, fail(409, "no_working_profile", "No candidate passed the latency test")
}

func (s *Server) disconnect(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 30*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.change != nil {
		return busy("A profile is starting or stopping")
	}
	if s.state == "idle" {
		return ok(s.status())
	}
	return s.waited(ctx, s.stopProfile(), wait, func() any { return s.status() })
}

func (s *Server) setModes(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 30*time.Second)
	if bad != nil {
		return bad
	}
	hasTun := r.fields["tun"] != nil
	tun, bad := boolField(r.fields, "tun", false)
	if bad != nil {
		return bad
	}
	proxy, bad := boolField(r.fields, "system_proxy", false)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if proxy {
		return fail(409, "system_proxy_unsupported", "Headless Throne never sets the system proxy")
	}
	if !hasTun || tun == s.tun {
		return ok(s.status())
	}
	if s.change != nil {
		return busy("A profile is starting or stopping")
	}
	s.tun = tun
	s.logf("[Throne] Tun mode %s", map[bool]string{true: "on", false: "off"}[tun])
	s.bump(throne.TopicStatus)
	if o := s.restartRunning(); o != nil {
		return s.waited(ctx, o, wait, func() any { return s.status() })
	}
	return ok(s.status())
}

func (s *Server) getRoutes(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	routes := []throne.Route{}
	for _, rt := range s.routes {
		routes = append(routes, throne.Route{ID: rt.ID, Name: rt.Name, Active: rt.ID == s.activeRoute})
	}
	return ok(map[string]any{"routes": routes})
}

func (s *Server) setRoute(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 30*time.Second)
	if bad != nil {
		return bad
	}
	id, present, bad := intField(r.fields, "id")
	switch {
	case bad != nil:
		return bad
	case !present:
		return badRequest("id is missing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.routes, func(rt throne.Route) bool { return rt.ID == id })
	switch {
	case i < 0:
		return notFound("No routing profile %d", id)
	case id == s.activeRoute:
		return ok(s.status())
	case s.change != nil:
		return busy("A profile is starting or stopping")
	}
	s.activeRoute = id
	s.logf("[Throne] Routing profile %s is active", s.routes[i].Name)
	s.bump(throne.TopicStatus)
	if o := s.restartRunning(); o != nil {
		return s.waited(ctx, o, wait, func() any { return s.status() })
	}
	return ok(s.status())
}

// testRunning says whether a test runs; s.mu is held.
func (s *Server) testRunning() bool {
	return len(s.jobs) > 0 && s.jobs[0].State == throne.JobRunning
}

func (s *Server) startTest(ctx context.Context, r *request) *response {
	kind, _, bad := stringField(r.fields, "kind")
	if bad != nil {
		return bad
	}
	if !slices.Contains([]string{"latency", "ip", "speed"}, kind) {
		return badRequest("kind must be latency, ip or speed")
	}
	wait, bad := boolField(r.fields, "wait", false)
	if bad != nil {
		return bad
	}
	timeout, present, bad := intField(r.fields, "timeout")
	switch {
	case bad != nil:
		return bad
	case present && (timeout < 1 || timeout > 300):
		return badRequest("timeout must be 1 to 300 seconds")
	case !present:
		timeout = 300
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, resp := s.selection(r.fields)
	if resp != nil {
		return resp
	}
	ids = slices.DeleteFunc(ids, func(id int) bool { return s.profiles[id].Type == "autoselector" })
	switch {
	case len(ids) == 0:
		return fail(400, "no_profiles", "The selection holds only auto selectors, which are never tested")
	case s.testRunning():
		return busy("A test is already running")
	}
	j := &job{Job: throne.Job{ID: s.nextJob, Kind: throne.TestKind(kind), State: throne.JobRunning, ProfileIDs: ids,
		StartedAt: time.Now().Unix()}, ended: newOp()}
	s.nextJob++
	s.jobs = append([]*job{j}, s.jobs...)[:min(len(s.jobs)+1, 32)]
	s.logf("[Test] Started a %s test of %d profiles", kind, len(ids))
	s.bump(throne.TopicTests)
	s.testNext(j, 0)
	if !wait {
		return accepted(j.Job)
	}
	if !s.await(ctx, j.ended, time.Duration(timeout)*time.Second) {
		if ctx.Err() != nil {
			return nil
		}
		return accepted(j.Job)
	}
	return ok(j.Job)
}

// testNext tests the profile at i of j, and so on to the end. s.mu is held.
func (s *Server) testNext(j *job, i int) {
	if i == len(j.ProfileIDs) {
		s.endJob(j, throne.JobDone)
		return
	}
	step := map[throne.TestKind]time.Duration{throne.TestLatency: 80, throne.TestIP: 150, throne.TestSpeed: 700}[j.Kind]
	d := min(step*time.Millisecond, 8*time.Second/time.Duration(len(j.ProfileIDs)))
	s.after(d, func() {
		if j.State != throne.JobRunning {
			return
		}
		if p := s.profiles[j.ProfileIDs[i]]; p != nil {
			s.measure(j.Kind, p)
		}
		s.testNext(j, i+1)
	})
}

// endJob ends a test; s.mu is held.
func (s *Server) endJob(j *job, state string) {
	j.State, j.FinishedAt = state, time.Now().Unix()
	j.Results = []throne.Profile{}
	for _, id := range j.ProfileIDs {
		if p := s.profiles[id]; p != nil {
			j.Results = append(j.Results, s.view(p))
		}
	}
	s.logf("[Test] The %s test of %d profiles is %s", j.Kind, len(j.ProfileIDs), state)
	s.bump(throne.TopicTests, throne.TopicProfiles)
	j.ended.end(nil)
}

// measure makes up the result of a test of p; s.mu is held.
func (s *Server) measure(kind throne.TestKind, p *throne.Profile) {
	broken := strings.Contains(strings.ToLower(p.Name), "broken")
	x := s.rnd.Float64()
	switch kind {
	case throne.TestLatency:
		p.LatencyAt = time.Now().Unix()
		switch {
		case broken || x < 0.08:
			p.LatencyMS = -1
		case (p.Type == "openvpn" || p.Type == "wireguard") && x < 0.15:
			p.LatencyMS = -2
		default:
			p.LatencyMS = 20 + s.rnd.IntN(380)
		}
	case throne.TestIP:
		if broken || x < 0.08 {
			return
		}
		p.Country = s.homes[p.ID]
		p.IPOut = fmt.Sprintf("%s.%d", []string{"203.0.113", "198.51.100", "192.0.2"}[s.rnd.IntN(3)], 1+s.rnd.IntN(254))
	case throne.TestSpeed:
		if broken || x < 0.1 {
			p.DownloadBPS, p.UploadBPS = -1, -1
			return
		}
		p.DownloadBPS = 2_000_000 + s.rnd.Int64N(400_000_000)
		p.UploadBPS = p.DownloadBPS / int64(2+s.rnd.IntN(8))
		p.Country = s.homes[p.ID]
	}
}

func (s *Server) findJob(id int) *job {
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (s *Server) getJobs(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := []throne.Job{}
	for _, j := range s.jobs {
		v := j.Job
		v.Results = nil
		jobs = append(jobs, v)
	}
	return ok(map[string]any{"jobs": jobs})
}

func (s *Server) getJob(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.findJob(r.ids[0])
	if j == nil {
		return notFound("No test %d", r.ids[0])
	}
	return ok(j.Job)
}

func (s *Server) cancelJob(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.findJob(r.ids[0])
	if j == nil {
		return notFound("No test %d", r.ids[0])
	}
	if j.State == throne.JobRunning {
		s.endJob(j, throne.JobCancelled)
	}
	return ok(j.Job)
}

func (s *Server) clearResults(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, resp := s.selection(r.fields)
	if resp != nil {
		return resp
	}
	for _, id := range ids {
		p := s.profiles[id]
		p.LatencyMS, p.LatencyAt, p.DownloadBPS, p.UploadBPS, p.Country, p.IPOut = 0, 0, 0, 0, "", ""
	}
	s.bump(throne.TopicProfiles)
	return ok(map[string]any{"cleared": len(ids)})
}

func (s *Server) getLive(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(s.live)
}

func (s *Server) getTraffic(_ context.Context, r *request) *response {
	by := r.Query["by"]
	switch by {
	case "":
		by = throne.TrafficByProfile
	case throne.TrafficByProfile, throne.TrafficByApp:
	default:
		return badRequest("by must be profile or app")
	}
	now := time.Now().Unix()
	to, bad := queryInt(r, "to", now, math.MinInt64, math.MaxInt64)
	if bad != nil {
		return badRequest("to must be an integer")
	}
	from, bad := queryInt(r, "from", to-86400, math.MinInt64, math.MaxInt64)
	if bad != nil {
		return badRequest("from must be an integer")
	}
	if from > to {
		return badRequest("from is after to")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []throne.TrafficItem{}
	for _, sr := range s.traffic {
		if (sr.id == nil) != (by == throne.TrafficByApp) {
			continue
		}
		item := throne.TrafficItem{ID: sr.id, Name: sr.name}
		if sr.id != nil {
			if p := s.profiles[*sr.id]; p != nil {
				item.Name = p.Name
			}
		}
		for h := range sr.up {
			if start := s.hour0 + int64(h)*3600; start+3600 > from && start < to {
				item.Up += sr.up[h]
				item.Down += sr.down[h]
			}
		}
		if item.Up+item.Down > 0 {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i].Up+items[i].Down, items[j].Up+items[j].Down
		return a > b || a == b && items[i].Name < items[j].Name
	})
	return ok(map[string]any{"by": by, "from": from, "to": to, "items": items})
}

func (s *Server) getSettings(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok(map[string]any{"settings": s.settings})
}

type settingSpec struct {
	kind    string // bool, number or string
	restart string // profile, tun or ""
	lo, hi  int
	zero    bool                // a number may also be 0, which turns it off
	check   func(string) string // what is wrong with a string, or ""
}

var settingSpecs = map[string]settingSpec{
	"remember_enable":             {kind: "bool"},
	"log_level":                   {kind: "string", restart: "profile", check: oneOf("trace", "debug", "info", "warn", "error", "fatal", "panic")},
	"xray_log_level":              {kind: "string", restart: "profile", check: oneOf("debug", "info", "warning", "error", "none")},
	"inbound_enabled":             {kind: "bool", restart: "profile"},
	"inbound_address":             {kind: "string", restart: "profile", check: ipAddress},
	"inbound_port":                {kind: "number", restart: "profile", lo: 1, hi: 65535},
	"test_url":                    {kind: "string", check: httpURL},
	"test_concurrency":            {kind: "number", lo: 1, hi: 499},
	"test_timeout_ms":             {kind: "number", lo: 0, hi: 60000},
	"sub_auto_update":             {kind: "number", lo: 30, hi: 525600, zero: true},
	"sub_respect_server_interval": {kind: "bool"},
	"route_auto_update":           {kind: "number", lo: 30, hi: 525600, zero: true},
	"kill_switch":                 {kind: "bool"},
	"tun_strict_route":            {kind: "bool", restart: "tun"},
	"tun_auto_redirect":           {kind: "bool", restart: "tun"},
	"tun_mtu":                     {kind: "number", restart: "tun", lo: 1000, hi: 10000},
	"tun_ipv6":                    {kind: "bool", restart: "tun"},
	"tun_ipv4_cidr":               {kind: "string", restart: "tun", check: cidr(false)},
	"tun_ipv6_cidr":               {kind: "string", restart: "tun", check: cidr(true)},
	"dns_in_port":                 {kind: "number", restart: "profile", lo: 1, hi: 65535},
	"clash_api_port":              {kind: "number", restart: "profile", lo: 1, hi: 65535, zero: true},
	"clash_listen_addr":           {kind: "string", restart: "profile", check: clashListen},
	"singbox_api_port":            {kind: "number", restart: "profile", lo: 1, hi: 65535, zero: true},
	"remote_dns":                  {kind: "string", restart: "profile", check: notEmpty},
	"direct_dns":                  {kind: "string", restart: "profile", check: notEmpty},
	"ntp_enabled":                 {kind: "bool", restart: "profile"},
	"ntp_server":                  {kind: "string", restart: "profile", check: func(string) string { return "" }},
	"ntp_interval":                {kind: "string", restart: "profile", check: duration},
}

func oneOf(values ...string) func(string) string {
	return func(v string) string {
		if slices.Contains(values, v) {
			return ""
		}
		return "want one of " + strings.Join(values, ", ")
	}
}

func ipAddress(v string) string {
	if net.ParseIP(v) == nil {
		return "not an IP address"
	}
	return ""
}

func httpURL(v string) string {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "not an http or https URL"
	}
	return ""
}

func cidr(v6 bool) func(string) string {
	return func(v string) string {
		ip, _, err := net.ParseCIDR(v)
		if err != nil || (ip.To4() == nil) != v6 {
			return "not an address with its prefix length"
		}
		return ""
	}
}

func clashListen(v string) string {
	switch v {
	case "", "127.0.0.1", "localhost", "::1":
		return ""
	}
	return "the Clash API has no secret, so it listens on loopback only; set a secret in Throne's settings window"
}

func notEmpty(v string) string {
	if v == "" {
		return "empty"
	}
	return ""
}

func duration(v string) string {
	if _, err := time.ParseDuration(v); err != nil && v != "" {
		return "not a duration such as 30m or 1h"
	}
	return ""
}

// settingValue checks a new value of a setting and returns it as the fake keeps it.
func settingValue(spec settingSpec, v any) (any, string) {
	switch spec.kind {
	case "bool":
		if b, ok := v.(bool); ok {
			return b, ""
		}
		return nil, "want true or false"
	case "number":
		n, ok := integer(v)
		if !ok || (n < spec.lo || n > spec.hi) && !(spec.zero && n == 0) {
			if spec.zero {
				return nil, fmt.Sprintf("want %d to %d, or 0 for off", spec.lo, spec.hi)
			}
			return nil, fmt.Sprintf("want %d to %d", spec.lo, spec.hi)
		}
		return n, ""
	}
	str, ok := v.(string)
	if !ok {
		return nil, "want a string"
	}
	return str, spec.check(str)
}

func (s *Server) putSettings(_ context.Context, r *request) *response {
	values, isObject := r.fields["settings"].(map[string]any)
	if !isObject {
		return badRequest("settings must be an object of settings")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.change != nil || s.testRunning() {
		return busy("A profile is starting or stopping, or a test is running")
	}
	next := map[string]any{}
	for k, v := range s.settings {
		next[k] = v
	}
	var changed []string
	for _, k := range slices.Sorted(maps.Keys(values)) {
		v := values[k]
		spec, known := settingSpecs[k]
		switch {
		case !known:
			return badRequest("Unknown setting %s", k)
		case v == nil:
			continue
		}
		nv, problem := settingValue(spec, v)
		if nv == s.settings[k] {
			continue
		}
		if problem != "" {
			return badRequest("%s: %s", k, problem)
		}
		next[k], changed = nv, append(changed, k)
	}
	ports := map[int]string{}
	for _, k := range []string{"inbound_port", "dns_in_port", "clash_api_port", "singbox_api_port"} {
		if p := next[k].(int); p != 0 {
			if other, taken := ports[p]; taken && slices.Contains(changed, k) {
				return badRequest("%s: port %d is %s already", k, p, other)
			}
			ports[p] = k
		}
	}
	if slices.Contains(changed, "ntp_enabled") && next["ntp_enabled"] == true && next["ntp_interval"] == "" {
		next["ntp_interval"] = "30m"
		changed = append(changed, "ntp_interval")
		slices.Sort(changed)
	}
	s.settings = next
	for _, k := range changed {
		switch settingSpecs[k].restart {
		case "profile":
			s.restartNeeded = s.restartNeeded || s.state == "running"
		case "tun":
			s.restartNeeded = s.restartNeeded || s.state == "running" && s.tun
		}
		if k == "kill_switch" {
			s.killSwitch = map[bool]string{true: "armed", false: "off"}[next[k].(bool)]
		}
	}
	if len(changed) > 0 {
		s.logf("[Settings] Changed %s", strings.Join(changed, ", "))
		s.bump(throne.TopicSettings, throne.TopicStatus)
	}
	if changed == nil {
		changed = []string{}
	}
	return ok(map[string]any{"settings": next, "changed": changed, "restart_needed": s.restartNeeded})
}

func (s *Server) updateRuleSets(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 90*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.state != "running":
		return fail(409, "not_running", "No profile runs, so no rule-set is loaded")
	case s.ruleSetUpdate != nil:
		return busy("The rule-sets are updating already")
	}
	o := newOp()
	s.ruleSetUpdate = o
	s.after(1500*time.Millisecond, func() {
		s.ruleSetUpdate = nil
		failures := []throne.RuleSetFailure{}
		if s.rnd.IntN(3) == 0 {
			failures = append(failures, throne.RuleSetFailure{Tag: "geosite-category-ads-all", Error: "HTTP 404 Not Found"})
			s.logf("%s [Rule-sets] geosite-category-ads-all: HTTP 404 Not Found", level("WARN"))
		}
		s.logf("[Rule-sets] Updated %d rule-sets", 4-len(failures))
		o.end(ok(map[string]any{"updated": 4 - len(failures), "failures": failures}))
	})
	return s.waited(ctx, o, wait, func() any { return map[string]any{"state": "updating"} })
}

func (s *Server) getGeo(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := []throne.GeoAsset{}
	for _, f := range s.geo {
		files = append(files, throne.GeoAsset{Name: f.name, URL: f.url, Present: f.present, Size: f.size, UpdatedAt: f.updatedAt})
	}
	return ok(map[string]any{"files": files})
}

func (s *Server) updateGeo(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 300*time.Second)
	if bad != nil {
		return bad
	}
	onlyMissing, bad := boolField(r.fields, "only_missing", false)
	if bad != nil {
		return bad
	}
	names := []string{"geoip", "geosite"}
	if v, present := r.fields["files"]; present && v != nil {
		list, isList := v.([]any)
		if !isList {
			return badRequest("files must be a list of geoip and geosite")
		}
		names = nil
		for _, x := range list {
			if x != "geoip" && x != "geosite" {
				return badRequest("files must be a list of geoip and geosite")
			}
			if !slices.Contains(names, x.(string)) {
				names = append(names, x.(string))
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.geoUpdate != nil {
		return busy("The geo files are downloading already")
	}
	o := newOp()
	s.geoUpdate = o
	results := []throne.GeoFileResult{}
	var next func(i int)
	next = func(i int) {
		if i == len(names) {
			s.geoUpdate = nil
			o.end(ok(map[string]any{"files": results}))
			return
		}
		f := s.geo[slices.IndexFunc(s.geo, func(f *geoFile) bool { return strings.HasPrefix(f.name, names[i]+".") })]
		if onlyMissing && f.present {
			results = append(results, throne.GeoFileResult{Name: f.name, OK: true, Skipped: true})
			next(i + 1)
			return
		}
		s.logf("[Geo] Downloading %s", f.url)
		s.after(1200*time.Millisecond, func() {
			f.present, f.size, f.updatedAt = true, 4_000_000+s.rnd.Int64N(6_000_000), time.Now().Unix()
			s.logf("[Geo] Updated %s, %d bytes", f.name, f.size)
			results = append(results, throne.GeoFileResult{Name: f.name, OK: true})
			next(i + 1)
		})
	}
	next(0)
	return s.waited(ctx, o, wait, func() any { return map[string]any{"state": "downloading"} })
}

func (s *Server) addGroupRequest(ctx context.Context, r *request) *response {
	name, _, bad := stringField(r.fields, "name")
	if bad != nil {
		return bad
	}
	link, _, bad := stringField(r.fields, "url")
	if bad != nil {
		return bad
	}
	autoUpdate, bad := boolField(r.fields, "auto_update", true)
	if bad != nil {
		return bad
	}
	update, bad := boolField(r.fields, "update", true)
	if bad != nil {
		return bad
	}
	wait, bad := waitTime(r.fields, 60*time.Second)
	switch {
	case bad != nil:
		return bad
	case strings.TrimSpace(name) == "":
		return badRequest("name is missing")
	case link != "" && httpURL(link) != "":
		return badRequest("url must be an http or https URL")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.addGroup(name, link)
	g.autoUpdate = autoUpdate
	s.logf("[Throne] Added the group %s", name)
	s.bump(throne.TopicGroups)
	if link == "" || !update {
		return ok(map[string]any{"group": s.groupView(g), "update": nil})
	}
	o := s.startUpdate(g)
	if r := s.waited(ctx, o, wait, func() any { return map[string]any{"group": s.groupView(g)} }); r == nil || r.status == 202 {
		return r
	}
	return ok(map[string]any{"group": s.groupView(g), "update": o.result})
}

// startUpdate updates a subscription group, or returns the update under way. s.mu is held.
func (s *Server) startUpdate(g *group) *op {
	if g.update != nil {
		return g.update
	}
	o := newOp()
	g.update = o
	s.logf("[Subscription] Updating %s", g.name)
	s.after(time.Second+time.Duration(s.rnd.IntN(1000))*time.Millisecond, func() {
		g.update = nil
		switch {
		case s.group(g.id) == nil:
			o.result = throne.UpdateResult{Error: "The group was deleted"}
		case strings.Contains(g.url, "fail"):
			o.result = throne.UpdateResult{Error: "HTTP 403 Forbidden"}
			s.logf("%s [Subscription] %s: HTTP 403 Forbidden", level("WARN"), g.name)
		default:
			o.result = s.refresh(g)
			g.updatedAt = time.Now().Unix()
			s.logf("[Subscription] %s: %d added, %d updated, %d removed", g.name, o.result.Added, o.result.Updated, o.result.Removed)
			s.bump(throne.TopicGroups, throne.TopicProfiles)
		}
		o.end(ok(nil))
	})
	return o
}

// refresh makes up what a subscription update changes. s.mu is held.
func (s *Server) refresh(g *group) throne.UpdateResult {
	res := throne.UpdateResult{OK: true}
	if len(g.profiles) == 0 {
		for range 8 + s.rnd.IntN(7) {
			s.addProfile(g, s.madeUp())
			res.Added++
		}
		return res
	}
	if len(g.profiles) > 5 && s.rnd.IntN(2) == 0 {
		id := g.profiles[s.rnd.IntN(len(g.profiles))]
		if id == s.running && s.state != "idle" {
			res.Kept++
		} else {
			s.removeProfiles([]int{id})
			res.Removed++
		}
	}
	res.Updated = s.rnd.IntN(len(g.profiles) + 1)
	for range s.rnd.IntN(3) {
		s.addProfile(g, s.madeUp())
		res.Added++
	}
	return res
}

func (s *Server) updateGroup(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 60*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.group(r.ids[0])
	switch {
	case g == nil:
		return notFound("No group %d", r.ids[0])
	case g.url == "":
		return fail(409, "not_subscription", "The group %s has no subscription", g.name)
	case g.archived:
		return fail(409, "group_archived", "The group %s is archived", g.name)
	}
	o := s.startUpdate(g)
	if r := s.waited(ctx, o, wait, func() any { return map[string]any{"group": s.groupView(g)} }); r == nil || r.status == 202 {
		return r
	}
	if !o.result.OK {
		return fail(502, "fetch_failed", "Could not download the subscription: %s", o.result.Error)
	}
	return ok(map[string]any{"group": s.groupView(g), "update": o.result})
}

func (s *Server) updateGroups(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 120*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var subs []*group
	ids := []int{}
	for _, g := range s.groups {
		if g.url != "" && !g.archived {
			subs, ids = append(subs, g), append(ids, g.id)
		}
	}
	o := newOp()
	results := []throne.GroupUpdate{}
	var next func(i int)
	next = func(i int) {
		if i == len(subs) {
			o.end(ok(map[string]any{"results": results}))
			return
		}
		u := s.startUpdate(subs[i])
		u.then(func() {
			results = append(results, throne.GroupUpdate{ID: subs[i].id, UpdateResult: u.result})
			next(i + 1)
		})
	}
	next(0)
	return s.waited(ctx, o, wait, func() any { return map[string]any{"queued": ids} })
}

// removeProfiles deletes profiles; s.mu is held.
func (s *Server) removeProfiles(ids []int) {
	for _, id := range ids {
		if p := s.profiles[id]; p != nil {
			if g := s.group(p.Group); g != nil {
				g.profiles = slices.DeleteFunc(g.profiles, func(x int) bool { return x == id })
			}
			delete(s.profiles, id)
		}
	}
}

// deleteWhenStopped deletes with remove, after stopping the running profile when stop
// says so, and answers with done. s.mu is held.
func (s *Server) deleteWhenStopped(ctx context.Context, wait time.Duration, stop bool, remove func(), done map[string]any) *response {
	if !stop {
		remove()
		return ok(done)
	}
	o := s.stopProfile()
	o.then(remove)
	if !s.await(ctx, o, wait) {
		if ctx.Err() != nil {
			return nil
		}
		early := map[string]any{"state": "stopping"}
		for k, v := range done {
			early[k] = v
		}
		return accepted(early)
	}
	return ok(done)
}

func (s *Server) deleteGroup(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 30*time.Second)
	if bad != nil {
		return bad
	}
	id := r.ids[0]
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.group(id)
	switch {
	case g == nil:
		return notFound("No group %d", id)
	case len(s.groups) == 1:
		return fail(409, "last_group", "Throne keeps at least one group")
	case g.update != nil:
		return busy("The subscription of %s is updating", g.name)
	case s.change != nil:
		return busy("A profile is starting or stopping")
	}
	stop := s.state == "running" && slices.Contains(g.profiles, s.running)
	return s.deleteWhenStopped(ctx, wait, stop, func() {
		s.removeProfiles(slices.Clone(g.profiles))
		s.groups = slices.DeleteFunc(s.groups, func(x *group) bool { return x == g })
		if s.shown == id {
			s.shown = s.groups[0].id
		}
		s.logf("[Throne] Deleted the group %s", g.name)
		s.bump(throne.TopicGroups, throne.TopicProfiles)
	}, map[string]any{"deleted": id})
}

func (s *Server) deleteProfiles(ctx context.Context, r *request) *response {
	wait, bad := waitTime(r.fields, 30*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids, resp := s.selection(r.fields)
	if resp != nil {
		return resp
	}
	if s.change != nil {
		return busy("A profile is starting or stopping")
	}
	stop := s.state == "running" && slices.Contains(ids, s.running)
	return s.deleteWhenStopped(ctx, wait, stop, func() {
		s.removeProfiles(ids)
		s.logf("[Throne] Deleted %d profiles", len(ids))
		s.bump(throne.TopicGroups, throne.TopicProfiles)
	}, map[string]any{"deleted": len(ids)})
}

// entry is a profile that an import found, or a problem with one.
type entry struct {
	profile  throne.Profile
	home     string
	severity string // "", or the severity of the problem
	field    string
	message  string
	example  string
}

var linkTypes = map[string]string{
	"vless": "vless", "vmess": "vmess", "trojan": "trojan", "ss": "shadowsocks", "hysteria2": "hysteria2",
	"hy2": "hysteria2", "hysteria": "hysteria", "tuic": "tuic", "socks": "socks", "socks5": "socks",
	"socks4": "socks", "http": "http", "https": "http", "wireguard": "wireguard", "wg": "wireguard",
	"anytls": "anytls", "naive+https": "naive",
}

// parseLinks reads share links, one per line, or a base64 subscription body of them.
func parseLinks(text string) []entry {
	if !strings.Contains(text, "://") {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			if b, err := enc.DecodeString(strings.Join(strings.Fields(text), "")); err == nil && bytes.Contains(b, []byte("://")) {
				text = string(b)
				break
			}
		}
	}
	var entries []entry
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		entries = append(entries, parseLink(line))
	}
	return entries
}

func parseLink(line string) entry {
	example := line
	if len(example) > 40 {
		example = example[:37] + "..."
	}
	var scheme, typ, host, port, name string
	if payload, ok := strings.CutPrefix(line, "vmess://"); ok && !strings.Contains(payload, "@") {
		// The common vmess link is base64 of a JSON object.
		var v struct {
			PS   string `json:"ps"`
			Add  string `json:"add"`
			Port any    `json:"port"`
		}
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil || json.Unmarshal(b, &v) != nil {
			return entry{severity: throne.SeverityError, field: "link", message: "The vmess link does not decode", example: "vmess://..."}
		}
		scheme, typ, host, port, name = "vmess", "vmess", v.Add, fmt.Sprint(v.Port), v.PS
	} else {
		u, err := url.Parse(line)
		if err != nil || linkTypes[strings.ToLower(u.Scheme)] == "" {
			return entry{severity: throne.SeverityError, field: "link", message: "Not a share link that Throne knows", example: example}
		}
		scheme, typ, host, port, name = u.Scheme, linkTypes[strings.ToLower(u.Scheme)], u.Hostname(), u.Port(), u.Fragment
	}
	example = scheme + "://" + net.JoinHostPort(host, port)
	if name != "" {
		example += " #" + name
	}
	if port == "" {
		port = "443"
	}
	n, err := strconv.Atoi(port)
	switch {
	case host == "":
		return entry{severity: throne.SeverityError, field: "address", message: "The link has no server address", example: example}
	case err != nil || n < 1 || n > 65535:
		return entry{severity: throne.SeverityError, field: "port", message: "Port out of range", example: example}
	}
	e := entry{profile: throne.Profile{Name: name, Type: typ, Address: net.JoinHostPort(host, port)}}
	if name == "" {
		e.profile.Name = host
		e.severity, e.field, e.message, e.example = throne.SeverityWarning, "name", "The link has no name; the profile is named after its server", example
	}
	return e
}

func (s *Server) importProfiles(ctx context.Context, r *request) *response {
	as, _, bad := stringField(r.fields, "as")
	if bad != nil {
		return bad
	}
	text, _, bad := stringField(r.fields, "text")
	if bad != nil {
		return bad
	}
	link, _, bad := stringField(r.fields, "url")
	if bad != nil {
		return bad
	}
	gid, hasGroup, bad := intField(r.fields, "group")
	if bad != nil {
		return bad
	}
	wait, bad := waitTime(r.fields, 60*time.Second)
	switch {
	case bad != nil:
		return bad
	case as == "profiles" && strings.TrimSpace(text) == "":
		return badRequest("text is empty")
	case as == "url" && httpURL(link) != "":
		return badRequest("url must be an http or https URL")
	case as != "profiles" && as != "url":
		return badRequest("as must be profiles or url")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !hasGroup {
		gid = s.shown
	}
	g := s.group(gid)
	switch {
	case g == nil:
		return notFound("No group %d", gid)
	case g.archived:
		return fail(409, "group_archived", "The group %s is archived", g.name)
	case g.url != "":
		return fail(409, "subscription_group", "The next update of the subscription of %s would remove the profiles", g.name)
	}
	o := newOp()
	delay := 300 * time.Millisecond
	if as == "url" {
		delay = 1200 * time.Millisecond
	}
	s.after(delay, func() {
		var entries []entry
		switch {
		case s.group(gid) == nil:
			o.end(notFound("No group %d", gid))
			return
		case as == "url" && strings.Contains(link, "fail"):
			o.end(fail(502, "fetch_failed", "Could not download %s: HTTP 404 Not Found", link))
			return
		case as == "url":
			for range 6 {
				entries = append(entries, s.madeUp())
			}
		default:
			entries = parseLinks(text)
		}
		o.end(s.importEntries(g, entries))
	})
	return s.waited(ctx, o, wait, func() any { return map[string]any{"group": gid, "state": "queued"} })
}

// importEntries adds what an import found to g and answers the import. s.mu is held.
func (s *Server) importEntries(g *group, entries []entry) *response {
	res := map[string]any{"group": g.id}
	ids, issues := []int{}, []throne.ImportIssue{}
	failed, warned := 0, 0
	for _, e := range entries {
		if e.severity != throne.SeverityError {
			ids = append(ids, s.addProfile(g, e).ID)
		}
		if e.severity == "" {
			continue
		}
		if e.severity == throne.SeverityError {
			failed++
		} else {
			warned++
		}
		i := slices.IndexFunc(issues, func(x throne.ImportIssue) bool {
			return x.Severity == e.severity && x.Field == e.field && x.Message == e.message
		})
		if i < 0 {
			issues = append(issues, throne.ImportIssue{Severity: e.severity, Field: e.field, Message: e.message, Examples: []string{}})
			i = len(issues) - 1
		}
		issues[i].Count++
		if len(issues[i].Examples) < 3 {
			issues[i].Examples = append(issues[i].Examples, e.example)
		}
	}
	if len(ids) == 0 {
		msg := "Nothing was imported"
		if len(issues) > 0 {
			msg = fmt.Sprintf("Nothing was imported: %d entries failed, such as %q: %s", failed, issues[0].Examples[0], issues[0].Message)
		}
		s.logf("%s [Import] %s", level("WARN"), msg)
		return fail(422, "nothing_imported", "%s", msg)
	}
	s.logf("[Import] Imported %d profiles into %s, %d failed", len(ids), g.name, failed)
	s.bump(throne.TopicGroups, throne.TopicProfiles)
	res["imported"], res["profile_ids"], res["failed"], res["warned"] = len(ids), ids, failed, warned
	res["issues"], res["errors"] = issues, []string{}
	return ok(res)
}

// expire drops the backups and uploads that nobody asked for in 10 minutes. s.mu is held.
func (s *Server) expire() {
	for id, b := range s.backups {
		if time.Since(b.seen) > 10*time.Minute {
			delete(s.backups, id)
		}
	}
	for id, u := range s.uploads {
		if time.Since(u.seen) > 10*time.Minute {
			delete(s.uploads, id)
		}
	}
}

var allParts = []string{throne.PartProfiles, throne.PartRoutes, throne.PartSettings, throne.PartOTP, throne.PartIPLists, throne.PartIcons}

// partList reads a list of part names.
func partList(f map[string]any) ([]string, *response) {
	v, present := f["parts"]
	if !present || v == nil {
		return nil, nil
	}
	list, isList := v.([]any)
	if !isList || len(list) == 0 {
		return nil, badRequest("parts must be a list of part names")
	}
	var parts []string
	for _, x := range list {
		p, _ := x.(string)
		if !slices.Contains(allParts, p) {
			return nil, badRequest("Unknown part %v; the parts are %s", x, strings.Join(allParts, ", "))
		}
		if !slices.Contains(parts, p) {
			parts = append(parts, p)
		}
	}
	return parts, nil
}

func (s *Server) createBackup(ctx context.Context, r *request) *response {
	parts, bad := partList(r.fields)
	if bad != nil {
		return bad
	}
	if parts == nil {
		parts = allParts
	}
	wait, bad := waitTime(r.fields, 120*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	if len(s.backups) >= 2 {
		return busy("Throne keeps at most 2 backups")
	}
	// The fake has no icon files.
	parts = slices.DeleteFunc(slices.Clone(parts), func(p string) bool { return p == throne.PartIcons })
	if len(parts) == 0 {
		return fail(500, "internal", "There is nothing to back up")
	}
	b := &backup{id: s.nextBackup, parts: parts, built: newOp(), seen: time.Now()}
	s.nextBackup++
	s.backups[b.id] = b
	s.after(800*time.Millisecond, func() {
		b.data = s.archive(parts)
		sum := sha256.Sum256(b.data)
		b.sha = hex.EncodeToString(sum[:])
		s.logf("[Backup] Built a backup of %s, %d bytes", strings.Join(parts, ", "), len(b.data))
		b.built.end(ok(map[string]any{"id": b.id, "size": len(b.data), "sha256": b.sha, "parts": parts}))
	})
	return s.waited(ctx, b.built, wait, func() any { return map[string]any{"id": b.id, "state": "building"} })
}

// archive makes a small archive in the fake's own format: "THRN", a version byte and JSON.
func (s *Server) archive(parts []string) []byte {
	doc := map[string]any{"created_at": time.Now().Format("Mon Jan 2 15:04:05 2006"), "platform": "linux",
		"parts": parts, "made_by": "fakethrone " + Version}
	if slices.Contains(parts, throne.PartProfiles) {
		var profiles []throne.Profile
		for _, g := range s.groups {
			for _, id := range g.profiles {
				profiles = append(profiles, *s.profiles[id])
			}
		}
		doc["profiles"] = profiles
	}
	if slices.Contains(parts, throne.PartRoutes) {
		doc["routes"] = s.routes
	}
	if slices.Contains(parts, throne.PartSettings) {
		doc["settings"] = s.settings
	}
	b, _ := json.Marshal(doc)
	return append([]byte("THRN\x01"), b...)
}

func (s *Server) readBackup(_ context.Context, r *request) *response {
	offset, bad := queryInt(r, "offset", 0, 0, -1)
	if bad != nil {
		return bad
	}
	length, bad := queryInt(r, "length", 4<<20, 1, 16<<20)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	b := s.backups[r.ids[0]]
	switch {
	case b == nil:
		return notFound("No backup %d", r.ids[0])
	case b.data == nil:
		return busy("The backup is still being built")
	case offset > int64(len(b.data)):
		return badRequest("offset is beyond the size of %d bytes", len(b.data))
	}
	b.seen = time.Now()
	end := min(offset+length, int64(len(b.data)))
	return ok(map[string]any{"id": b.id, "size": len(b.data), "sha256": b.sha, "parts": b.parts, "offset": offset,
		"data": b.data[offset:end], "eof": end == int64(len(b.data))})
}

func (s *Server) deleteBackup(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backups[r.ids[0]] == nil {
		return notFound("No backup %d", r.ids[0])
	}
	delete(s.backups, r.ids[0])
	return ok(map[string]any{"deleted": r.ids[0]})
}

const chunkLimit = 512 << 10

func (s *Server) openUpload(_ context.Context, r *request) *response {
	size, _, bad := intField(r.fields, "size")
	if bad != nil {
		return bad
	}
	sha, _, bad := stringField(r.fields, "sha256")
	if bad != nil {
		return bad
	}
	if _, err := hex.DecodeString(sha); err != nil || len(sha) != 64 || size < 1 {
		return badRequest("Give the archive's size and its SHA-256 in 64 hexadecimal digits")
	}
	if size > 1<<30 {
		return fail(413, "too_large", "The archive is larger than 1 GiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	if len(s.uploads) >= 2 {
		return busy("Throne keeps at most 2 uploads")
	}
	u := &upload{id: s.nextUpload, size: int64(size), sha: strings.ToLower(sha), state: throne.UploadUploading, seen: time.Now()}
	s.nextUpload++
	s.uploads[u.id] = u
	return ok(map[string]any{"id": u.id, "chunk_limit": chunkLimit})
}

func (s *Server) putChunk(ctx context.Context, r *request) *response {
	offset, _, bad := intField(r.fields, "offset")
	if bad != nil {
		return bad
	}
	var chunk struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &chunk); err != nil {
		return badRequest("data must be base64")
	}
	wait, bad := waitTime(r.fields, 120*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	u := s.uploads[r.ids[0]]
	switch {
	case u == nil:
		return notFound("No upload %d", r.ids[0])
	case u.state == throne.UploadInspecting:
		return busy("The archive is being checked")
	case u.state != throne.UploadUploading:
		return badRequest("The upload is complete")
	case int64(offset) != int64(len(u.data)):
		return fail(409, "bad_offset", "The upload stands at offset %d", len(u.data))
	case len(chunk.Data) == 0 || len(chunk.Data) > chunkLimit:
		return badRequest("data must hold 1 to %d bytes", chunkLimit)
	case int64(len(u.data)+len(chunk.Data)) > u.size:
		return badRequest("The chunk ends past the size of %d bytes", u.size)
	}
	u.seen = time.Now()
	u.data = append(u.data, chunk.Data...)
	if int64(len(u.data)) < u.size {
		return ok(map[string]any{"id": u.id, "size": u.size, "received": len(u.data)})
	}
	if sum := sha256.Sum256(u.data); hex.EncodeToString(sum[:]) != u.sha {
		delete(s.uploads, u.id)
		return fail(422, "checksum_mismatch", "The archive does not match its SHA-256")
	}
	u.state, u.checked = throne.UploadInspecting, newOp()
	s.after(600*time.Millisecond, func() {
		if !bytes.HasPrefix(u.data, []byte("THRN")) {
			u.state, u.err = throne.UploadFailed, "The file is not a Throne backup archive"
			u.checked.end(fail(422, "invalid_archive", "%s", u.err))
			return
		}
		u.state, u.archive = throne.UploadComplete, readArchive(u.data)
		u.checked.end(ok(map[string]any{"id": u.id, "size": u.size, "received": len(u.data), "archive": u.archive}))
	})
	resp := s.waited(ctx, u.checked, wait, func() any { return map[string]any{"id": u.id, "state": throne.UploadInspecting} })
	if resp != nil && resp.status == 422 {
		// Throne keeps a refused archive only after it answered 202.
		delete(s.uploads, u.id)
	}
	return resp
}

// readArchive describes an archive: one of the fake's own, or else, such as for one of a
// real Throne, the usual parts.
func readArchive(data []byte) *throne.Archive {
	a := &throne.Archive{CreatedAt: time.Now().Format("Mon Jan 2 15:04:05 2006"), Platform: "linux",
		Parts: []string{throne.PartProfiles, throne.PartRoutes, throne.PartSettings, throne.PartOTP, throne.PartIPLists}}
	if len(data) > 5 {
		json.Unmarshal(data[5:], a)
	}
	return a
}

func (s *Server) uploadView(u *upload) map[string]any {
	v := map[string]any{"id": u.id, "size": u.size, "received": len(u.data), "state": u.state}
	switch u.state {
	case throne.UploadComplete:
		v["archive"] = u.archive
	case throne.UploadFailed:
		v["error"] = u.err
	}
	return v
}

func (s *Server) getUpload(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	u := s.uploads[r.ids[0]]
	if u == nil {
		return notFound("No upload %d", r.ids[0])
	}
	u.seen = time.Now()
	return ok(s.uploadView(u))
}

func (s *Server) deleteUpload(_ context.Context, r *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.uploads[r.ids[0]] == nil {
		return notFound("No upload %d", r.ids[0])
	}
	delete(s.uploads, r.ids[0])
	return ok(map[string]any{"deleted": r.ids[0]})
}

func (s *Server) applyRestore(ctx context.Context, r *request) *response {
	parts, bad := partList(r.fields)
	if bad != nil {
		return bad
	}
	wait, bad := waitTime(r.fields, 60*time.Second)
	if bad != nil {
		return bad
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	u := s.uploads[r.ids[0]]
	switch {
	case u == nil:
		return notFound("No upload %d", r.ids[0])
	case u.state == throne.UploadUploading:
		return fail(409, "incomplete", "The upload is not complete")
	case u.state == throne.UploadInspecting:
		return busy("The archive is still being checked")
	case u.state == throne.UploadFailed:
		return fail(422, "invalid_archive", "%s", u.err)
	case s.change != nil || s.testRunning():
		return busy("A profile is starting or stopping, or a test is running")
	}
	if parts == nil {
		for _, p := range u.archive.Parts {
			if p != throne.PartSettings && p != throne.PartIcons {
				parts = append(parts, p)
			}
		}
		if parts == nil {
			return badRequest("The archive holds only settings and icons: choose the parts to restore")
		}
	}
	for _, p := range parts {
		if !slices.Contains(u.archive.Parts, p) {
			return badRequest("The archive has no %s", p)
		}
	}
	o := newOp()
	s.logf("[Restore] Restoring %s", strings.Join(parts, ", "))
	s.after(500*time.Millisecond, func() {
		delete(s.uploads, u.id)
		o.end(ok(map[string]any{"restored": parts, "skipped_rules": 0, "restarting": true}))
		s.after(200*time.Millisecond, func() { s.restart("restarting") })
	})
	return s.waited(ctx, o, wait, func() any { return map[string]any{"id": u.id, "state": "applying"} })
}

func (s *Server) restartRequest(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.change != nil {
		return busy("A profile is starting or stopping")
	}
	s.after(200*time.Millisecond, func() { s.restart("starting") })
	return accepted(map[string]any{"restarting": true, "resume": true})
}

func (s *Server) shutdownRequest(_ context.Context, _ *request) *response {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.change != nil {
		return busy("A profile is starting or stopping")
	}
	s.logf("[Throne] Shutdown requested; fakethrone keeps running, where Throne would exit now")
	return accepted(map[string]any{"stopping": true})
}
