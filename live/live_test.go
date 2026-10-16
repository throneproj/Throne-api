//go:build live

package live

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	throne "github.com/throneproj/Throne-api"
)

const (
	// changeWait is the timeout field, in seconds, of requests that start, stop or restart.
	changeWait  = 90
	changeLimit = changeWait*time.Second + 15*time.Second
	readLimit   = 30 * time.Second
	settleLimit = 90 * time.Second
	testLimit   = 5 * time.Minute
	pollEvery   = 500 * time.Millisecond
	burstSize   = 6
	// exitLimit bounds the wait for Throne to exit after a restart or a shutdown, and
	// backLimit the wait for it to answer again after a restart.
	exitLimit = 60 * time.Second
	backLimit = 2 * time.Minute
)

// scratchName names the group the suite adds and deletes again; it is the same in every
// run, and so are the profiles it imports into it, which nothing connects to.
const (
	scratchName  = "throne-live scratch"
	scratchLinks = "socks5://127.0.0.1:1080#throne-live-1\nsocks5://127.0.0.1:1081#throne-live-2"
)

type config struct {
	profile     int
	profile2    int // -1 when unset
	route       int // -1 when unset
	dir         string
	tun         bool
	proxy       bool
	killCore    bool
	destructive bool
}

func envID(name string, required bool) (int, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		if required {
			return -1, fmt.Errorf("set %s; see live/README.md", name)
		}
		return -1, nil
	}
	id, err := strconv.Atoi(v)
	if err != nil || id < 0 {
		return -1, fmt.Errorf("%s=%q is not an ID", name, v)
	}
	return id, nil
}

func loadConfig(t *testing.T) config {
	t.Helper()
	var cfg config
	var err error
	if cfg.profile, err = envID("THRONE_LIVE_PROFILE", true); err != nil {
		t.Fatal(err)
	}
	if cfg.profile2, err = envID("THRONE_LIVE_PROFILE2", false); err != nil {
		t.Fatal(err)
	}
	if cfg.profile2 == cfg.profile {
		t.Fatal("THRONE_LIVE_PROFILE2 must differ from THRONE_LIVE_PROFILE")
	}
	if cfg.route, err = envID("THRONE_LIVE_ROUTE", false); err != nil {
		t.Fatal(err)
	}
	cfg.dir = os.Getenv("THRONE_LIVE_TRANSCRIPT_DIR")
	if cfg.dir == "" {
		cfg.dir = filepath.Join(os.TempDir(), "throne-live", time.Now().Format("20060102-150405"))
	}
	cfg.tun = os.Getenv("THRONE_LIVE_ALLOW_TUN") == "1"
	cfg.proxy = os.Getenv("THRONE_LIVE_SYSTEM_PROXY") == "1"
	cfg.killCore = os.Getenv("THRONE_LIVE_KILL_CORE") == "1"
	cfg.destructive = os.Getenv("THRONE_LIVE_DESTRUCTIVE") == "1"
	return cfg
}

type suite struct {
	c    *throne.Client
	cfg  config
	orig throne.Status
	tmp  string
	// archive is the backup archive the backup subtest downloaded, for the restore.
	archive string
	// exited says that the shutdown subtest made Throne exit.
	exited bool

	mu   sync.Mutex
	jobs []int
}

func TestLive(t *testing.T) {
	cfg := loadConfig(t)
	c, err := throne.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	s := &suite{c: c, cfg: cfg, tmp: t.TempDir()}
	ctx, cancel := context.WithTimeout(t.Context(), settleLimit)
	st, err := s.settle(ctx)
	cancel()
	if err != nil {
		t.Fatalf("Throne does not settle: %v", err)
	}
	s.orig = *st
	t.Cleanup(func() { s.restore(t) })
	t.Logf("transcripts: %s", cfg.dir)

	steps := []struct {
		name string
		run  func(*testing.T, *recorder)
	}{
		{"status", s.testStatus},
		{"listing", s.testListing},
		{"connect", s.testConnect},
		{"route", s.testRoute},
		{"switch", s.testSwitch},
		{"contention", s.testContention},
		{"disconnect", s.testDisconnect},
		{"latency", s.testLatency},
		{"cancel", s.testCancel},
		{"modes", s.testModes},
		{"core-kill", s.testCoreKill},
		{"capabilities", s.testCapabilities},
		{"settings", s.testSettings},
		{"scratch-group", s.testScratchGroup},
		{"geo", s.testGeo},
		{"rule-sets", s.testRuleSets},
		{"backup", s.testBackup},
		{"restore", s.testRestore},
		{"restart", s.testRestart},
		{"shutdown", s.testShutdown},
	}
	for i, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			r := &recorder{}
			file := fmt.Sprintf("%02d-%s.json", i+1, step.name)
			t.Cleanup(func() {
				if err := r.write(cfg.dir, file, step.name, t.Failed()); err != nil {
					t.Errorf("write transcript: %v", err)
				}
			})
			step.run(t, r)
		})
	}
}

func profileIDs(profiles []throne.Profile) []int {
	ids := make([]int, len(profiles))
	for i, p := range profiles {
		ids[i] = p.ID
	}
	return ids
}

// sameIDs compares as sets: listings come in Throne's order, not the order asked for.
func sameIDs(a, b []int) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// ids are the configured profiles.
func (s *suite) ids() []int {
	if s.cfg.profile2 >= 0 {
		return []int{s.cfg.profile, s.cfg.profile2}
	}
	return []int{s.cfg.profile}
}

func (s *suite) track(job int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
}

// call sends a recorded request and decodes its response into out, unless out is nil.
func (s *suite) call(t *testing.T, r *recorder, limit time.Duration, x exchange, out any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()
	raw, err := send(ctx, s.c, x)
	r.record(x, raw, err)
	if err != nil {
		return err
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: decode: %v", x.step, err)
		}
	}
	return nil
}

func (s *suite) get(t *testing.T, r *recorder, step, path string, query map[string]string, out any, view func(any) any) {
	t.Helper()
	if err := s.call(t, r, readLimit, exchange{step: step, method: "GET", path: path, query: query, view: view}, out); err != nil {
		t.Fatalf("%s: %v", step, err)
	}
}

// change sends a recorded request that waits for the connection, and returns the Status.
func (s *suite) change(t *testing.T, r *recorder, step, method, path string, body map[string]any) (*throne.Status, error) {
	t.Helper()
	body["timeout"] = changeWait
	var st throne.Status
	if err := s.call(t, r, changeLimit, exchange{step: step, method: method, path: path, body: body}, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *suite) mustChange(t *testing.T, r *recorder, step, method, path string, body map[string]any) *throne.Status {
	t.Helper()
	st, err := s.change(t, r, step, method, path, body)
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	return st
}

// changeRetry is change, retrying while the error code is one of retry, and records only
// the last attempt, so that a race with Throne leaves no trace in the transcript.
func (s *suite) changeRetry(t *testing.T, r *recorder, step, method, path string, body map[string]any, retry ...string) *throne.Status {
	t.Helper()
	body["timeout"] = changeWait
	x := exchange{step: step, method: method, path: path, body: body}
	deadline := time.Now().Add(settleLimit)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), changeLimit)
		raw, err := send(ctx, s.c, x)
		cancel()
		if err != nil && slices.Contains(retry, throne.ErrorCode(err)) && time.Now().Before(deadline) {
			t.Logf("%s: %v; retrying", step, err)
			pause(t.Context(), time.Second)
			continue
		}
		r.record(x, raw, err)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		var st throne.Status
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("%s: decode: %v", step, err)
		}
		return &st
	}
}

func pause(ctx context.Context, d time.Duration) error {
	tm := time.NewTimer(d)
	defer tm.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-tm.C:
		return nil
	}
}

func describe(st *throne.Status) string {
	p := "none"
	if st.Profile != nil {
		p = strconv.Itoa(st.Profile.ID)
	}
	return fmt.Sprintf("state=%s profile=%s core=%v tun=%v system_proxy=%v route=%d", st.State, p, st.Core, st.Tun, st.SystemProxy, st.Route.ID)
}

func wantRunning(t *testing.T, st *throne.Status, id int) {
	t.Helper()
	if st.State != throne.StateRunning || st.Profile == nil || st.Profile.ID != id || !st.Profile.Running {
		t.Fatalf("want profile %d running, got %s", id, describe(st))
	}
}

func wantIdle(t *testing.T, st *throne.Status) {
	t.Helper()
	if st.State != throne.StateIdle || st.Profile != nil {
		t.Fatalf("want idle, got %s", describe(st))
	}
}

// settled says whether nothing starts or stops and the core is up.
func settled(st *throne.Status) bool {
	return st.Core && (st.State == throne.StateIdle || st.State == throne.StateRunning)
}

// settle polls the Status, unrecorded, until it is settled.
func (s *suite) settle(ctx context.Context) (*throne.Status, error) {
	for {
		st, err := s.c.Status(ctx)
		if err == nil && settled(st) {
			return st, nil
		}
		if perr := pause(ctx, pollEvery); perr != nil {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("still %s", describe(st))
		}
	}
}

func (s *suite) mustSettle(t *testing.T) *throne.Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), settleLimit)
	defer cancel()
	st, err := s.settle(ctx)
	if err != nil {
		t.Fatalf("Throne does not settle: %v", err)
	}
	return st
}

// ensureRunning brings the precondition of a subtest about unrecorded: this profile runs.
func (s *suite) ensureRunning(t *testing.T, id int) {
	t.Helper()
	st := s.mustSettle(t)
	if st.State == throne.StateRunning && st.Profile != nil && st.Profile.ID == id {
		return
	}
	deadline := time.Now().Add(settleLimit)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), changeLimit)
		st, err := s.c.Connect(ctx, id)
		cancel()
		if throne.ErrorCode(err) == throne.CodeBusy && time.Now().Before(deadline) {
			pause(t.Context(), time.Second)
			continue
		}
		if err != nil {
			t.Fatalf("precondition: connect %d: %v", id, err)
		}
		wantRunning(t, st, id)
		return
	}
}

func (s *suite) ensureIdle(t *testing.T) {
	t.Helper()
	if st := s.mustSettle(t); st.State == throne.StateIdle {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), changeLimit)
	defer cancel()
	st, err := s.c.Disconnect(ctx)
	if err != nil {
		t.Fatalf("precondition: disconnect: %v", err)
	}
	wantIdle(t, st)
}

func (s *suite) testStatus(t *testing.T, r *recorder) {
	var st throne.Status
	s.get(t, r, "status", "/v1/status", nil, &st, nil)
	if !settled(&st) {
		t.Errorf("not settled: %s", describe(&st))
	}
	if st.Version == "" {
		t.Error("no version")
	}
	if !slices.Contains([]string{"off", "arming", "armed", "failed"}, st.KillSwitch) {
		t.Errorf("kill_switch %q", st.KillSwitch)
	}
}

func (s *suite) testListing(t *testing.T, r *recorder) {
	var groups struct {
		Groups []throne.Group `json:"groups"`
	}
	s.get(t, r, "groups", "/v1/groups", nil, &groups, nil)

	ids := s.ids()
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = strconv.Itoa(id)
	}
	var profiles struct {
		Profiles []throne.Profile `json:"profiles"`
	}
	s.get(t, r, "profiles by id", "/v1/profiles", map[string]string{"ids": strings.Join(list, ",")}, &profiles, nil)
	if got := profileIDs(profiles.Profiles); !sameIDs(got, ids) {
		t.Fatalf("asked for profiles %v, got %v", ids, got)
	}

	var p throne.Profile
	s.get(t, r, "profile", "/v1/profiles/"+strconv.Itoa(s.cfg.profile), nil, &p, nil)
	if p.ID != s.cfg.profile || p.Type == "autoselector" {
		t.Fatalf("profile %d: got id %d type %q; THRONE_LIVE_PROFILE must be a regular profile", s.cfg.profile, p.ID, p.Type)
	}
	if !slices.ContainsFunc(groups.Groups, func(g throne.Group) bool { return g.ID == p.Group }) {
		t.Errorf("group %d of profile %d is not listed", p.Group, p.ID)
	}

	var group struct {
		Profiles []throne.Profile `json:"profiles"`
	}
	s.get(t, r, "group profiles", "/v1/profiles", map[string]string{"group": strconv.Itoa(p.Group)}, &group, idsView(ids))
	if !slices.ContainsFunc(group.Profiles, func(q throne.Profile) bool { return q.ID == p.ID }) {
		t.Errorf("group %d does not list profile %d", p.Group, p.ID)
	}

	err := s.call(t, r, readLimit, exchange{step: "unknown profile", method: "GET", path: "/v1/profiles/2147483647"}, nil)
	if throne.ErrorCode(err) != throne.CodeNotFound {
		t.Errorf("unknown profile: got %v, want %s", err, throne.CodeNotFound)
	}

	var routes struct {
		Routes []throne.Route `json:"routes"`
	}
	s.get(t, r, "routes", "/v1/routes", nil, &routes, nil)
	if s.cfg.route >= 0 && !slices.ContainsFunc(routes.Routes, func(rt throne.Route) bool { return rt.ID == s.cfg.route }) {
		t.Errorf("route %d is not listed", s.cfg.route)
	}
}

func (s *suite) testConnect(t *testing.T, r *recorder) {
	id := s.cfg.profile
	s.ensureIdle(t)
	st := s.mustChange(t, r, "connect", "PUT", "/v1/connection", map[string]any{"profile": id})
	wantRunning(t, st, id)

	var now throne.Status
	s.get(t, r, "status", "/v1/status", nil, &now, nil)
	wantRunning(t, &now, id)
	if now.LastProfileID != id {
		t.Errorf("last_profile_id %d, want %d", now.LastProfileID, id)
	}

	st = s.mustChange(t, r, "connect running profile", "PUT", "/v1/connection", map[string]any{"profile": id})
	wantRunning(t, st, id)
	st = s.mustChange(t, r, "restart", "PUT", "/v1/connection", map[string]any{"profile": id, "restart": true})
	wantRunning(t, st, id)
}

func (s *suite) testRoute(t *testing.T, r *recorder) {
	if s.cfg.route < 0 {
		r.skip(t, "THRONE_LIVE_ROUTE is not set")
	}
	if !s.orig.Route.Active {
		r.skip(t, "Throne has no active routing profile to return to")
	}
	id := s.cfg.profile
	s.ensureRunning(t, id)
	st := s.mustChange(t, r, "activate route", "PUT", "/v1/routes/active", map[string]any{"id": s.cfg.route})
	wantRunning(t, st, id)
	if st.Route.ID != s.cfg.route {
		t.Fatalf("route %d active, want %d", st.Route.ID, s.cfg.route)
	}
	back := s.orig.Route.ID
	st = s.mustChange(t, r, "restore route", "PUT", "/v1/routes/active", map[string]any{"id": back})
	wantRunning(t, st, id)
	if st.Route.ID != back {
		t.Fatalf("route %d active, want %d", st.Route.ID, back)
	}
}

func (s *suite) testSwitch(t *testing.T, r *recorder) {
	if s.cfg.profile2 < 0 {
		r.skip(t, "THRONE_LIVE_PROFILE2 is not set")
	}
	s.ensureRunning(t, s.cfg.profile)
	st := s.mustChange(t, r, "switch", "PUT", "/v1/connection", map[string]any{"profile": s.cfg.profile2})
	wantRunning(t, st, s.cfg.profile2)

	var profiles struct {
		Profiles []throne.Profile `json:"profiles"`
	}
	ids := fmt.Sprintf("%d,%d", s.cfg.profile, s.cfg.profile2)
	s.get(t, r, "profiles", "/v1/profiles", map[string]string{"ids": ids}, &profiles, nil)
	for _, p := range profiles.Profiles {
		if p.Running != (p.ID == s.cfg.profile2) {
			t.Errorf("profile %d: running=%v", p.ID, p.Running)
		}
	}
}

// outcome classes a response for the races, where the exact response depends on timing.
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	if code := throne.ErrorCode(err); code != "" {
		return code
	}
	return "error: " + err.Error()
}

func (s *suite) testContention(t *testing.T, r *recorder) {
	target := s.cfg.profile
	if s.cfg.profile2 >= 0 {
		target = s.cfg.profile2
	}
	s.ensureRunning(t, target)

	// Restarts of one profile: whichever wins, the same profile ends up running.
	sessions := make([]*throne.Session, burstSize)
	for i := range sessions {
		ctx, cancel := context.WithTimeout(t.Context(), readLimit)
		sess, err := s.c.Dial(ctx)
		cancel()
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer sess.Close()
		sessions[i] = sess
	}
	x := exchange{step: "concurrent restarts", method: "PUT", path: "/v1/connection",
		body: map[string]any{"profile": target, "restart": true, "timeout": changeWait}}
	outcomes := make([]string, burstSize)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, sess := range sessions {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(t.Context(), changeLimit)
			defer cancel()
			<-start
			_, err := send(ctx, sess, x)
			outcomes[i] = outcome(err)
		})
	}
	close(start)
	wg.Wait()
	t.Logf("concurrent restarts: %v", outcomes)
	allowed := []string{"ok", throne.CodeBusy, throne.CodeSuperseded}
	inSet := !slices.ContainsFunc(outcomes, func(o string) bool { return !slices.Contains(allowed, o) })
	someOK := slices.Contains(outcomes, "ok")
	r.note(x.step, map[string]any{
		"request":         x.request(),
		"count":           burstSize,
		"allowed":         allowed,
		"all_allowed":     inSet,
		"at_least_one_ok": someOK,
	})
	if !inSet {
		t.Errorf("outcomes %v, want each of %v", outcomes, allowed)
	}
	if !someOK {
		t.Errorf("outcomes %v: no restart succeeded", outcomes)
	}
	s.mustSettle(t)
	var st throne.Status
	s.get(t, r, "status after restarts", "/v1/status", nil, &st, nil)
	wantRunning(t, &st, target)

	// A stop right behind a start: busy while it starts, or a stop if the start already finished.
	ctx, cancel := context.WithTimeout(t.Context(), changeLimit)
	defer cancel()
	startX := exchange{step: "start without waiting", method: "PUT", path: "/v1/connection",
		body: map[string]any{"profile": target, "restart": true, "wait": false}}
	stopX := exchange{step: "stop while starting", method: "DELETE", path: "/v1/connection",
		body: map[string]any{"timeout": changeWait}}
	_, startErr := send(ctx, s.c, startX)
	_, stopErr := send(ctx, s.c, stopX)
	t.Logf("start without waiting: %s; stop while starting: %s", outcome(startErr), outcome(stopErr))
	stopAllowed := outcome(stopErr) == "ok" || throne.ErrorCode(stopErr) == throne.CodeBusy
	r.note(stopX.step, map[string]any{
		"requests":     []*request{startX.request(), stopX.request()},
		"start":        outcome(startErr),
		"allowed":      []string{"ok", throne.CodeBusy},
		"stop_allowed": stopAllowed,
	})
	if startErr != nil {
		t.Errorf("start without waiting: %v", startErr)
	}
	if !stopAllowed {
		t.Errorf("stop while starting: %v", stopErr)
	}
	s.mustSettle(t)
}

func (s *suite) testDisconnect(t *testing.T, r *recorder) {
	s.ensureRunning(t, s.cfg.profile)
	st := s.mustChange(t, r, "disconnect", "DELETE", "/v1/connection", map[string]any{})
	wantIdle(t, st)

	var now throne.Status
	s.get(t, r, "status", "/v1/status", nil, &now, nil)
	wantIdle(t, &now)
	if now.LastProfileID != s.cfg.profile {
		t.Errorf("last_profile_id %d, want %d", now.LastProfileID, s.cfg.profile)
	}

	st = s.mustChange(t, r, "disconnect when idle", "DELETE", "/v1/connection", map[string]any{})
	wantIdle(t, st)
}

// startTest starts a test, retrying while Throne still finishes an earlier one, and
// records the last attempt.
func (s *suite) startTest(t *testing.T, r *recorder, step string, body map[string]any, view func(any) any) *throne.Job {
	t.Helper()
	x := exchange{step: step, method: "POST", path: "/v1/tests", body: body, view: view}
	deadline := time.Now().Add(settleLimit)
	for {
		ctx, cancel := context.WithTimeout(t.Context(), readLimit)
		raw, err := send(ctx, s.c, x)
		cancel()
		if throne.ErrorCode(err) == throne.CodeBusy && time.Now().Before(deadline) {
			pause(t.Context(), time.Second)
			continue
		}
		r.record(x, raw, err)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		var job throne.Job
		if err := json.Unmarshal(raw, &job); err != nil {
			t.Fatalf("%s: decode: %v", step, err)
		}
		s.track(job.ID)
		return &job
	}
}

// waitJob polls a test, unrecorded, until it ends.
func (s *suite) waitJob(t *testing.T, id int) *throne.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testLimit)
	defer cancel()
	for {
		job, err := s.c.Job(ctx, id)
		if err != nil {
			t.Fatalf("poll test: %v", err)
		}
		if job.State != throne.JobRunning {
			return job
		}
		if pause(ctx, pollEvery) != nil {
			t.Fatalf("test still running after %v", testLimit)
		}
	}
}

func (s *suite) testLatency(t *testing.T, r *recorder) {
	s.ensureIdle(t)
	ids := s.ids()
	job := s.startTest(t, r, "start", map[string]any{"kind": throne.TestLatency, "profiles": ids}, nil)
	if job.Kind != throne.TestLatency || !slices.Equal(job.ProfileIDs, ids) {
		t.Fatalf("job kind %q profiles %v, want latency %v", job.Kind, job.ProfileIDs, ids)
	}
	s.waitJob(t, job.ID)

	var done throne.Job
	s.get(t, r, "result", "/v1/tests/"+strconv.Itoa(job.ID), nil, &done, nil)
	if done.State != throne.JobDone {
		t.Errorf("state %q, want %q", done.State, throne.JobDone)
	}
	if got := profileIDs(done.Results); !sameIDs(got, ids) {
		t.Errorf("results for %v, want %v", got, ids)
	}

	var jobs struct {
		Jobs []throne.Job `json:"jobs"`
	}
	s.get(t, r, "job list", "/v1/tests", nil, &jobs, jobView(job.ID))
	i := slices.IndexFunc(jobs.Jobs, func(j throne.Job) bool { return j.ID == job.ID })
	switch {
	case i < 0:
		t.Errorf("test %d is not listed", job.ID)
	case jobs.Jobs[i].State != throne.JobDone || jobs.Jobs[i].Results != nil:
		t.Errorf("listed test %d: state %q with %d results", job.ID, jobs.Jobs[i].State, len(jobs.Jobs[i].Results))
	}
}

func (s *suite) testCancel(t *testing.T, r *recorder) {
	ctx, cancel := context.WithTimeout(t.Context(), readLimit)
	p, err := s.c.Profile(ctx, s.cfg.profile)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	// A whole group, so the test usually still runs when the cancel arrives.
	job := s.startTest(t, r, "start", map[string]any{"kind": throne.TestLatency, "group": p.Group}, cancelView)
	if !slices.Contains(job.ProfileIDs, p.ID) {
		t.Errorf("group test leaves out profile %d", p.ID)
	}

	var got throne.Job
	x := exchange{step: "cancel", method: "DELETE", path: "/v1/tests/" + strconv.Itoa(job.ID), view: cancelView}
	if err := s.call(t, r, readLimit, x, &got); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	t.Logf("cancel: %s", got.State)
	s.waitJob(t, job.ID)
	s.get(t, r, "after cancel", "/v1/tests/"+strconv.Itoa(job.ID), nil, &got, cancelView)
	if got.State != throne.JobCancelled && got.State != throne.JobDone {
		t.Errorf("state %q after cancel", got.State)
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// toggleMode switches one mode away from its original value and back. refusal is the
// error code Throne may answer when switching it on, which ends the toggle.
func (s *suite) toggleMode(t *testing.T, r *recorder, field, name string, orig bool, refusal string, get func(*throne.Status) bool) {
	t.Helper()
	to := !orig
	st, err := s.change(t, r, name+" "+onOff(to), "PUT", "/v1/modes", map[string]any{field: to})
	if to && throne.ErrorCode(err) == refusal {
		t.Logf("%s: %v", name, err)
		return
	}
	if err != nil {
		t.Fatalf("%s %s: %v", name, onOff(to), err)
	}
	wantRunning(t, st, s.cfg.profile)
	if get(st) != to {
		t.Fatalf("%s is %v, want %v", name, get(st), to)
	}
	st = s.mustChange(t, r, name+" "+onOff(orig), "PUT", "/v1/modes", map[string]any{field: orig})
	wantRunning(t, st, s.cfg.profile)
	if get(st) != orig {
		t.Fatalf("%s is %v, want %v", name, get(st), orig)
	}
}

func (s *suite) testModes(t *testing.T, r *recorder) {
	s.ensureRunning(t, s.cfg.profile)
	o := s.orig
	st := s.mustChange(t, r, "unchanged modes", "PUT", "/v1/modes", map[string]any{"tun": o.Tun, "system_proxy": o.SystemProxy})
	wantRunning(t, st, s.cfg.profile)
	if st.Tun != o.Tun || st.SystemProxy != o.SystemProxy {
		t.Fatalf("modes changed: %s", describe(st))
	}
	if s.cfg.proxy {
		s.toggleMode(t, r, "system_proxy", "system proxy", o.SystemProxy, throne.CodeMixedInboundDisabled,
			func(st *throne.Status) bool { return st.SystemProxy })
	} else {
		r.note("system proxy", "THRONE_LIVE_SYSTEM_PROXY is not set")
	}
	if s.cfg.tun {
		s.toggleMode(t, r, "tun", "tun", o.Tun, throne.CodeNeedsPrivileges,
			func(st *throne.Status) bool { return st.Tun })
	} else {
		r.note("tun", "THRONE_LIVE_ALLOW_TUN is not set")
	}
}

func killCore() ([]byte, error) {
	if runtime.GOOS == "windows" {
		return exec.Command("taskkill", "/F", "/IM", "ThroneCore.exe").CombinedOutput()
	}
	return exec.Command("pkill", "-9", "-x", "ThroneCore").CombinedOutput()
}

func (s *suite) testCoreKill(t *testing.T, r *recorder) {
	if !s.cfg.killCore {
		r.skip(t, "THRONE_LIVE_KILL_CORE is not set")
	}
	id := s.cfg.profile
	s.ensureRunning(t, id)
	if out, err := killCore(); err != nil {
		t.Fatalf("kill ThroneCore: %v: %s", err, out)
	}

	// Throne may restart the core before a poll sees it down, so the outage is only logged.
	outage, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		st, err := s.c.Status(outage)
		if err != nil {
			t.Logf("outage seen: %v", err)
			break
		}
		if !st.Core || st.State != throne.StateRunning {
			t.Logf("outage seen: %s", describe(st))
			break
		}
		if pause(outage, 100*time.Millisecond) != nil {
			t.Log("no outage seen")
			break
		}
	}

	// Settled on two polls in a row, so that a restart Throne does by itself has begun.
	ctx, cancel := context.WithTimeout(t.Context(), settleLimit)
	defer cancel()
	for streak := 0; ; {
		if st, err := s.c.Status(ctx); err == nil && settled(st) {
			streak++
		} else {
			streak = 0
		}
		if streak == 2 {
			break
		}
		if pause(ctx, time.Second) != nil {
			t.Fatalf("core did not recover within %v", settleLimit)
		}
	}
	r.note("core recovered", true)

	st := s.changeRetry(t, r, "reconnect", "PUT", "/v1/connection", map[string]any{"profile": id, "restart": true},
		throne.CodeBusy, throne.CodeSuperseded, throne.CodeCoreUnavailable)
	wantRunning(t, st, id)
	var now throne.Status
	s.get(t, r, "status", "/v1/status", nil, &now, nil)
	wantRunning(t, &now, id)
	if !now.Core {
		t.Error("core is down")
	}
}

// admin skips the subtest when err says that Throne refuses admin requests from here.
func (s *suite) admin(t *testing.T, r *recorder, err error) {
	t.Helper()
	if throne.ErrorCode(err) == throne.CodeForbidden {
		r.skip(t, "Throne answers admin requests only on its own device: run the suite there, "+
			"or allow management from the local network")
	}
}

// wantCode fails the test unless err is an error response with this code.
func wantCode(t *testing.T, step string, err error, code string) {
	t.Helper()
	if throne.ErrorCode(err) != code {
		t.Errorf("%s: got %v, want %s", step, err, code)
	}
}

// drop deletes, unrecorded, a group, backup or upload that a failed subtest left.
func (s *suite) drop(t *testing.T, path string) {
	ctx, cancel := context.WithTimeout(context.Background(), changeLimit)
	defer cancel()
	if err := s.c.Call(ctx, "DELETE", path, nil, nil, nil); err != nil && throne.ErrorCode(err) != throne.CodeNotFound {
		t.Logf("delete %s: %v", path, err)
	}
}

// watch opens a Session, whose connection ends when Throne exits.
func (s *suite) watch(t *testing.T) *throne.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), readLimit)
	defer cancel()
	sess, err := s.c.Dial(ctx)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// awaitExit polls on sess, unrecorded, until its connection ends: that Throne has exited.
// A new connection could already reach the Throne that replaces it.
func (s *suite) awaitExit(t *testing.T, sess *throne.Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), exitLimit)
	defer cancel()
	for {
		if _, err := sess.Status(ctx); err != nil && throne.ErrorCode(err) == "" && ctx.Err() == nil {
			return
		}
		if pause(ctx, 200*time.Millisecond) != nil {
			t.Fatalf("Throne did not exit within %v", exitLimit)
		}
	}
}

// awaitBack polls, unrecorded, until Throne answers again and has settled; with running,
// until it runs a profile.
func (s *suite) awaitBack(t *testing.T, running bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), backLimit)
	defer cancel()
	for {
		st, err := s.c.Status(ctx)
		if err == nil && settled(st) && (!running || st.State == throne.StateRunning) {
			return
		}
		if pause(ctx, pollEvery) != nil {
			if err == nil {
				err = fmt.Errorf("still %s", describe(st))
			}
			t.Fatalf("Throne is not back within %v: %v", backLimit, err)
		}
	}
}

func (s *suite) testCapabilities(t *testing.T, r *recorder) {
	s.mustSettle(t)
	view := func(v any) any {
		m, _ := v.(map[string]any)
		return map[string]any{"capabilities": m["capabilities"], "restart_needed": m["restart_needed"], "degraded": m["degraded"]}
	}
	var st map[string]any
	s.get(t, r, "status", "/v1/status", nil, &st, view)
	caps, ok := st["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities %v, want an object", st["capabilities"])
	}
	if tun, present := caps["tun"]; !present || tun != nil {
		if _, ok := tun.(bool); !ok {
			t.Errorf("capabilities.tun %v (present %v), want a bool or null", tun, present)
		}
	}
	for _, k := range []string{"kill_switch", "headless"} {
		if _, ok := caps[k].(bool); !ok {
			t.Errorf("capabilities.%s %v, want a bool", k, caps[k])
		}
	}
	if _, ok := st["restart_needed"].(bool); !ok {
		t.Errorf("restart_needed %v, want a bool", st["restart_needed"])
	}
	degraded, present := st["degraded"]
	var want any
	if st["kill_switch"] == "failed" {
		want = throne.DegradedKillSwitchFailed
	}
	if !present || degraded != want {
		t.Errorf("degraded %v (present %v), want %v", degraded, present, want)
	}
}

func (s *suite) testSettings(t *testing.T, r *recorder) {
	var got struct {
		Settings map[string]any `json:"settings"`
	}
	s.get(t, r, "settings", "/v1/settings", nil, &got, nil)
	orig, ok := got.Settings["test_url"].(string)
	if !ok {
		t.Fatalf("test_url %v, want a string", got.Settings["test_url"])
	}
	alt := "https://www.gstatic.com/generate_204"
	if orig == alt {
		alt = "http://cp.cloudflare.com/"
	}
	put := func(step string, values map[string]any, out *throne.SettingsChange) error {
		t.Helper()
		var dst any
		if out != nil {
			dst = out
		}
		return s.call(t, r, readLimit, exchange{step: step, method: "PUT", path: "/v1/settings",
			body: map[string]any{"settings": values}}, dst)
	}

	var ch throne.SettingsChange
	err := put("change test_url", map[string]any{"test_url": alt}, &ch)
	s.admin(t, r, err)
	if err != nil {
		t.Fatalf("change test_url: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), readLimit)
		defer cancel()
		if now, err := s.c.Settings(ctx); err == nil && now["test_url"] != orig {
			if _, err := s.c.SetSettings(ctx, map[string]any{"test_url": orig}); err != nil {
				t.Logf("put back test_url: %v", err)
			}
		}
	})
	if !slices.Equal(ch.Changed, []string{"test_url"}) || ch.Settings["test_url"] != alt {
		t.Errorf("change test_url: changed %v, test_url %v", ch.Changed, ch.Settings["test_url"])
	}
	ch = throne.SettingsChange{}
	if err := put("same test_url", map[string]any{"test_url": alt}, &ch); err != nil {
		t.Fatalf("same test_url: %v", err)
	}
	if len(ch.Changed) != 0 {
		t.Errorf("same test_url: changed %v", ch.Changed)
	}

	// Throne checks every value before it writes any.
	wantCode(t, "one bad value", put("one bad value", map[string]any{"test_url": orig, "test_concurrency": 0}, nil),
		throne.CodeBadRequest)
	wantCode(t, "bad URL", put("bad URL", map[string]any{"test_url": "ftp://example.com/"}, nil), throne.CodeBadRequest)
	wantCode(t, "unknown setting", put("unknown setting", map[string]any{"no_such_setting": true}, nil), throne.CodeBadRequest)
	ctx, cancel := context.WithTimeout(t.Context(), readLimit)
	now, err := s.c.Settings(ctx)
	cancel()
	if err != nil || now["test_url"] != alt {
		t.Errorf("after refused changes: test_url %v, %v; want %s", now["test_url"], err, alt)
	}

	ch = throne.SettingsChange{}
	if err := put("restore test_url", map[string]any{"test_url": orig}, &ch); err != nil {
		t.Fatalf("restore test_url: %v", err)
	}
	if !slices.Equal(ch.Changed, []string{"test_url"}) || ch.Settings["test_url"] != orig {
		t.Errorf("restore test_url: changed %v, test_url %v", ch.Changed, ch.Settings["test_url"])
	}
}

func (s *suite) testScratchGroup(t *testing.T, r *recorder) {
	var added throne.GroupResult
	x := exchange{step: "add group", method: "POST", path: "/v1/groups", body: map[string]any{"name": scratchName},
		view: maskView("group.id")}
	err := s.call(t, r, readLimit, x, &added)
	s.admin(t, r, err)
	if err != nil {
		t.Fatalf("add group: %v", err)
	}
	gid := added.Group.ID
	path := "/v1/groups/" + strconv.Itoa(gid)
	t.Cleanup(func() { s.drop(t, path) })
	if added.Group.Name != scratchName || added.Group.Subscription || added.Update != nil {
		t.Errorf("added %+v, update %v", added.Group, added.Update)
	}

	var imp throne.ImportResult
	x = exchange{step: "import", method: "POST", path: "/v1/import",
		body:     map[string]any{"as": "profiles", "text": scratchLinks, "group": gid, "timeout": changeWait},
		bodyView: maskView("group"), view: maskView("group", "profile_ids")}
	if err := s.call(t, r, changeLimit, x, &imp); err != nil {
		t.Fatalf("import: %v", err)
	}
	if imp.Group != gid || imp.Imported != 2 || len(imp.ProfileIDs) != 2 {
		t.Fatalf("import: %+v", imp)
	}
	ctx, cancel := context.WithTimeout(t.Context(), readLimit)
	profiles, err := s.c.Profiles(ctx, throne.SelectGroup(gid, ""))
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	if got := profileIDs(profiles); !sameIDs(got, imp.ProfileIDs) {
		t.Errorf("group %d holds %v, want %v", gid, got, imp.ProfileIDs)
	}
	for _, p := range profiles {
		if p.Type != "socks" || !strings.HasPrefix(p.Name, "throne-live-") {
			t.Errorf("imported %s profile %q", p.Type, p.Name)
		}
	}
	err = s.call(t, r, changeLimit, exchange{step: "import nothing", method: "POST", path: "/v1/import",
		body: map[string]any{"as": "profiles", "text": "not a profile", "group": gid}, bodyView: maskView("group")}, nil)
	wantCode(t, "import nothing", err, throne.CodeNothingImported)

	var del throne.Deletion
	x = exchange{step: "delete profile", method: "DELETE", path: "/v1/profiles",
		body: map[string]any{"profiles": imp.ProfileIDs[:1], "timeout": changeWait}, bodyView: maskView("profiles")}
	if err := s.call(t, r, changeLimit, x, &del); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	if del != (throne.Deletion{Deleted: 1}) {
		t.Errorf("delete profile: %+v", del)
	}
	del = throne.Deletion{}
	x = exchange{step: "delete group", method: "DELETE", path: path, body: map[string]any{"timeout": changeWait},
		view: maskView("deleted")}
	if err := s.call(t, r, changeLimit, x, &del); err != nil {
		t.Fatalf("delete group: %v", err)
	}
	if del != (throne.Deletion{Deleted: gid}) {
		t.Errorf("delete group: %+v", del)
	}
	err = s.call(t, r, readLimit, exchange{step: "delete group again", method: "DELETE", path: path}, nil)
	wantCode(t, "delete group again", err, throne.CodeNotFound)

	ctx, cancel = context.WithTimeout(t.Context(), readLimit)
	groups, err := s.c.Groups(ctx)
	cancel()
	if err != nil || slices.ContainsFunc(groups, func(g throne.Group) bool { return g.ID == gid }) {
		t.Errorf("group %d is still listed (%v)", gid, err)
	}
}

func (s *suite) testGeo(t *testing.T, r *recorder) {
	var got struct {
		Files []throne.GeoAsset `json:"files"`
	}
	s.get(t, r, "geo files", "/v1/geo-assets", nil, &got, nil)
	if len(got.Files) == 0 {
		t.Fatal("no geo files")
	}
	present := true
	for _, f := range got.Files {
		if f.Name == "" || f.URL == "" {
			t.Errorf("geo file %+v", f)
		}
		present = present && f.Present
	}
	err := s.call(t, r, readLimit, exchange{step: "unknown file", method: "POST", path: "/v1/geo-assets/update",
		body: map[string]any{"files": []string{"geocity"}}}, nil)
	wantCode(t, "unknown file", err, throne.CodeBadRequest)
	if !present {
		r.note("update missing", "a geo file is missing, and the suite downloads none")
		return
	}

	// With every file present, only_missing downloads nothing.
	var up throne.GeoUpdate
	x := exchange{step: "update missing", method: "POST", path: "/v1/geo-assets/update",
		body: map[string]any{"only_missing": true, "timeout": changeWait}}
	if err := s.call(t, r, changeLimit, x, &up); err != nil {
		t.Fatalf("update missing: %v", err)
	}
	if len(up.Files) != len(got.Files) {
		t.Errorf("update missing: %d results for %d files", len(up.Files), len(got.Files))
	}
	for _, f := range up.Files {
		if !f.OK || !f.Skipped {
			t.Errorf("update missing: %+v, want skipped", f)
		}
	}
}

func (s *suite) testRuleSets(t *testing.T, r *recorder) {
	s.ensureIdle(t)
	err := s.call(t, r, readLimit, exchange{step: "update while idle", method: "POST", path: "/v1/rule-sets/update"}, nil)
	wantCode(t, "update while idle", err, throne.CodeNotRunning)

	s.ensureRunning(t, s.cfg.profile)
	var up throne.RuleSetUpdate
	x := exchange{step: "update", method: "POST", path: "/v1/rule-sets/update", body: map[string]any{"timeout": changeWait},
		view: ruleSetView}
	if err := s.call(t, r, changeLimit, x, &up); err != nil {
		t.Fatalf("update: %v", err)
	}
	t.Logf("rule-sets: state %q, %d updated, %d failed", up.State, up.Updated, len(up.Failures))
}

func (s *suite) testBackup(t *testing.T, r *recorder) {
	var b struct {
		throne.BackupInfo
		State string `json:"state"`
	}
	x := exchange{step: "create", method: "POST", path: "/v1/backups",
		body: map[string]any{"parts": []string{throne.PartProfiles, throne.PartRoutes}, "timeout": changeWait},
		view: maskView("id")}
	err := s.call(t, r, changeLimit, x, &b)
	s.admin(t, r, err)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	path := "/v1/backups/" + strconv.Itoa(b.ID)
	t.Cleanup(func() { s.drop(t, path) })
	if b.State != "" {
		t.Fatalf("the backup is still %s after %d seconds", b.State, changeWait)
	}
	if b.Size < 8 || len(b.SHA256) != 64 || !slices.Equal(b.Parts, []string{throne.PartProfiles, throne.PartRoutes}) {
		t.Errorf("backup %+v", b.BackupInfo)
	}

	// The first 8 bytes are the magic and the format version, which the transcript keeps.
	var head struct {
		Data []byte `json:"data"`
		EOF  bool   `json:"eof"`
	}
	x = exchange{step: "read head", method: "GET", path: path, query: map[string]string{"offset": "0", "length": "8"},
		view: maskView("id")}
	if err := s.call(t, r, readLimit, x, &head); err != nil {
		t.Fatalf("read head: %v", err)
	}
	if len(head.Data) != 8 || !bytes.HasPrefix(head.Data, []byte("THRN")) || head.EOF {
		t.Errorf("archive starts with %q, eof %v", head.Data, head.EOF)
	}
	err = s.call(t, r, readLimit, exchange{step: "read past the end", method: "GET", path: path,
		query: map[string]string{"offset": "1099511627776"}}, nil)
	wantCode(t, "read past the end", err, throne.CodeBadRequest)
	var del struct {
		Deleted int `json:"deleted"`
	}
	x = exchange{step: "delete", method: "DELETE", path: path, view: maskView("deleted")}
	if err := s.call(t, r, readLimit, x, &del); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if del.Deleted != b.ID {
		t.Errorf("deleted %d, want %d", del.Deleted, b.ID)
	}
	err = s.call(t, r, readLimit, exchange{step: "read deleted", method: "GET", path: path}, nil)
	wantCode(t, "read deleted", err, throne.CodeNotFound)

	// A whole archive, through the library, for the restore subtest.
	file := filepath.Join(s.tmp, "live.thrbackup")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), changeLimit)
	info, err := s.c.Backup(ctx, f, nil)
	cancel()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("download a backup: %v", err)
	}
	archive, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	ok := bytes.HasPrefix(archive, []byte("THRN")) && int64(len(archive)) == info.Size
	r.note("download", map[string]any{"parts": info.Parts, "magic_and_size": ok})
	if !ok {
		t.Fatalf("downloaded %d bytes starting %q, want %d starting THRN", len(archive), archive[:min(4, len(archive))], info.Size)
	}
	s.archive = file
}

func (s *suite) testRestore(t *testing.T, r *recorder) {
	if !s.cfg.destructive {
		r.skip(t, "THRONE_LIVE_DESTRUCTIVE is not set")
	}
	if s.archive == "" {
		r.skip(t, "the backup subtest made no archive")
	}
	archive, err := os.ReadFile(s.archive)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive)
	var open struct {
		ID         int `json:"id"`
		ChunkLimit int `json:"chunk_limit"`
	}
	x := exchange{step: "open upload", method: "POST", path: "/v1/restores",
		body: map[string]any{"size": len(archive), "sha256": hex.EncodeToString(sum[:])}, view: maskView("id")}
	err = s.call(t, r, readLimit, x, &open)
	s.admin(t, r, err)
	if err != nil {
		t.Fatalf("open upload: %v", err)
	}
	path := "/v1/restores/" + strconv.Itoa(open.ID)
	applied := false
	t.Cleanup(func() {
		if !applied {
			s.drop(t, path)
		}
	})
	err = s.call(t, r, readLimit, exchange{step: "apply incomplete", method: "POST", path: path + "/apply"}, nil)
	wantCode(t, "apply incomplete", err, throne.CodeIncomplete)
	var st throne.RestoreUpload
	s.get(t, r, "status while uploading", path, nil, &st, maskView("id"))
	if st.State != throne.UploadUploading || st.Received != 0 || st.Archive != nil {
		t.Errorf("status while uploading: %+v", st)
	}

	// Only the last chunk is recorded: how many come before it depends on the archive's size.
	n := open.ChunkLimit
	if n <= 0 {
		n = 512 << 10
	}
	var up throne.RestoreUpload
	for offset := 0; offset < len(archive); {
		end := min(offset+n, len(archive))
		x := exchange{step: "upload last chunk", method: "PUT", path: path,
			body:     map[string]any{"offset": offset, "data": archive[offset:end]},
			bodyView: maskView("offset", "data"), view: maskView("id")}
		if end < len(archive) {
			ctx, cancel := context.WithTimeout(t.Context(), readLimit)
			_, err := send(ctx, s.c, x)
			cancel()
			if err != nil {
				t.Fatalf("upload at %d: %v", offset, err)
			}
		} else {
			x.body["timeout"] = changeWait
			if err := s.call(t, r, changeLimit, x, &up); err != nil {
				t.Fatalf("upload last chunk: %v", err)
			}
		}
		offset = end
	}
	if up.Archive == nil {
		t.Fatalf("Throne still checks the archive after %d seconds", changeWait)
	}
	st = throne.RestoreUpload{}
	s.get(t, r, "status when checked", path, nil, &st, maskView("id"))
	if st.State != throne.UploadComplete || st.Received != int64(len(archive)) || !reflect.DeepEqual(st.Archive, up.Archive) {
		t.Errorf("status when checked: %+v, want the archive %+v", st, up.Archive)
	}
	var want []string
	for _, p := range up.Archive.Parts {
		if p != throne.PartSettings && p != throne.PartIcons {
			want = append(want, p)
		}
	}
	err = s.call(t, r, readLimit, exchange{step: "apply an unknown part", method: "POST", path: path + "/apply",
		body: map[string]any{"parts": []string{"no_such_part"}}}, nil)
	wantCode(t, "apply an unknown part", err, throne.CodeBadRequest)

	s.mustSettle(t)
	sess := s.watch(t)
	var res throne.RestoreResult
	x = exchange{step: "apply", method: "POST", path: path + "/apply", body: map[string]any{"timeout": changeWait}}
	if err := s.call(t, r, changeLimit, x, &res); err != nil {
		if throne.ErrorCode(err) == throne.CodeInvalidArchive {
			r.skip(t, "Throne restores this archive only in its window: "+err.Error())
		}
		t.Fatalf("apply: %v", err)
	}
	applied = true
	if !res.Restarting || !slices.Equal(res.Restored, want) {
		t.Errorf("apply: %+v, want %v restored and a restart", res, want)
	}
	s.awaitExit(t, sess)
	s.awaitBack(t, false)
	r.note("back after the restart", true)
}

func (s *suite) testRestart(t *testing.T, r *recorder) {
	if !s.cfg.destructive {
		r.skip(t, "THRONE_LIVE_DESTRUCTIVE is not set")
	}
	id := s.cfg.profile
	s.ensureRunning(t, id)
	sess := s.watch(t)
	var res struct {
		Restarting bool `json:"restarting"`
		Resume     bool `json:"resume"`
	}
	err := s.call(t, r, readLimit, exchange{step: "restart", method: "POST", path: "/v1/restart"}, &res)
	s.admin(t, r, err)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !res.Restarting {
		t.Errorf("restart: %+v", res)
	}
	s.awaitExit(t, sess)
	s.awaitBack(t, res.Resume)
	r.note("back after the restart", true)

	var now throne.Status
	s.get(t, r, "status", "/v1/status", nil, &now, nil)
	if res.Resume {
		wantRunning(t, &now, id)
	} else {
		wantIdle(t, &now)
	}
}

func (s *suite) testShutdown(t *testing.T, r *recorder) {
	if !s.cfg.destructive {
		r.skip(t, "THRONE_LIVE_DESTRUCTIVE is not set")
	}
	// Throne is gone afterwards, so the state found at the start goes back first.
	s.restore(t)
	sess := s.watch(t)
	var res struct {
		Stopping bool `json:"stopping"`
	}
	err := s.call(t, r, readLimit, exchange{step: "shutdown", method: "POST", path: "/v1/shutdown"}, &res)
	s.admin(t, r, err)
	if err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	s.exited = true
	if !res.Stopping {
		t.Errorf("shutdown: %+v", res)
	}
	s.awaitExit(t, sess)
	ctx, cancel := context.WithTimeout(t.Context(), readLimit)
	defer cancel()
	_, err = s.c.Status(ctx)
	gone := err != nil && throne.ErrorCode(err) == ""
	r.note("gone", gone)
	if !gone {
		t.Errorf("Throne still answers after the shutdown: %v", err)
	}
}

// restore puts back the connection, modes and route found at the start, best effort.
func (s *suite) restore(t *testing.T) {
	if s.exited {
		t.Log("restore: Throne was shut down; start it again")
		return
	}
	within := func(d time.Duration, f func(ctx context.Context) error) error {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		return f(ctx)
	}
	logf := func(what string, err error) {
		if err != nil {
			t.Logf("restore: %s: %v", what, err)
		}
	}
	s.mu.Lock()
	jobs := slices.Clone(s.jobs)
	s.mu.Unlock()
	for _, id := range jobs {
		logf("cancel test", within(readLimit, func(ctx context.Context) error {
			job, err := s.c.Job(ctx, id)
			if err == nil && job.State == throne.JobRunning {
				_, err = s.c.CancelJob(ctx, id)
			}
			return err
		}))
	}

	var st *throne.Status
	if err := within(settleLimit, func(ctx context.Context) (err error) {
		st, err = s.settle(ctx)
		return err
	}); err != nil {
		logf("settle", err)
		return
	}
	o := s.orig
	sameRun := o.State == st.State && (o.Profile == nil) == (st.Profile == nil) && (o.Profile == nil || o.Profile.ID == st.Profile.ID)
	sameModes := o.Tun == st.Tun && o.SystemProxy == st.SystemProxy
	sameRoute := !o.Route.Active || o.Route.ID == st.Route.ID
	if sameRun && sameModes && sameRoute {
		return
	}
	t.Logf("restore: from %s to %s", describe(st), describe(&o))
	if st.State != throne.StateIdle {
		logf("disconnect", within(changeLimit, func(ctx context.Context) error {
			_, err := s.c.Disconnect(ctx)
			return err
		}))
	}
	if !sameRoute {
		logf("route", within(changeLimit, func(ctx context.Context) error {
			_, err := s.c.SetRoute(ctx, o.Route.ID)
			return err
		}))
	}
	if !sameModes {
		logf("modes", within(changeLimit, func(ctx context.Context) error {
			_, err := s.c.SetModes(ctx, throne.Modes{Tun: &o.Tun, SystemProxy: &o.SystemProxy})
			return err
		}))
	}
	if o.State == throne.StateRunning && o.Profile != nil {
		logf("connect", within(changeLimit, func(ctx context.Context) error {
			_, err := s.c.Connect(ctx, o.Profile.ID)
			return err
		}))
	}
}
