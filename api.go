package throne

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// pollInterval is how often RunTest asks for a Job that still runs, Backup for an
// archive that Throne still builds, and UploadRestore for one that Throne still checks.
var pollInterval = time.Second

// api implements the requests of Client and Session.
type api struct {
	do func(ctx context.Context, r *call, out any) error
}

// fetch makes the call and decodes the response body into a new T.
func fetch[T any](ctx context.Context, a api, r *call) (*T, error) {
	v := new(T)
	if err := a.do(ctx, r, v); err != nil {
		return nil, err
	}
	return v, nil
}

// Call sends any request and decodes the response body into out, unless out is nil.
// query and body may be nil, and path has no query string. Every 2xx status, 202
// included, is a success; a status of 400 or more returns an *APIError. Call sends body
// as it is: it adds no wait or timeout field. Without a deadline in ctx it allows for the
// longest time Throne may hold a request.
func (a api) Call(ctx context.Context, method, path string, query map[string]string, body, out any) error {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return fmt.Errorf("throne: invalid path %q: want a path such as /v1/status, without a query string", path)
	}
	return a.do(ctx, &call{method: method, path: path, query: query, body: body, wait: maxWait}, out)
}

// Response is a response as Throne sent it.
type Response struct {
	// Status is below 400, such as 200 or 202.
	Status int
	// Body is the JSON body, or nil when the response has none.
	Body json.RawMessage
}

// Send sends any request like Call, and returns the status and body as Throne sent them.
// A status of 400 or more returns an *APIError.
func (a api) Send(ctx context.Context, method, path string, query map[string]string, body any) (*Response, error) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("throne: invalid path %q: want a path such as /v1/status, without a query string", path)
	}
	resp := new(Response)
	if err := a.do(ctx, &call{method: method, path: path, query: query, body: body, wait: maxWait}, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// Events returns the topics that changed after an earlier Events (GET /v1/events). With
// q.Wait, Throne holds the request while nothing has changed, so a loop of Events calls
// follows Throne's changes.
func (a api) Events(ctx context.Context, q EventsQuery) (*Events, error) {
	query := map[string]string{}
	if q.Instance != "" {
		query["instance"] = q.Instance
		query["after"] = strconv.FormatInt(q.After, 10)
	}
	r := &call{method: "GET", path: "/v1/events", query: query}
	if t := waitSeconds(q.Wait); t > 0 {
		query["timeout"] = strconv.Itoa(t)
		r.wait = time.Duration(t) * time.Second
	}
	return fetch[Events](ctx, a, r)
}

// waitSeconds turns a hold time into the timeout query parameter: whole seconds, at most 300.
func waitSeconds(d time.Duration) int {
	return min(int(d/time.Second), int(maxWait/time.Second))
}

// Status returns Throne's state (GET /v1/status).
func (a api) Status(ctx context.Context) (*Status, error) {
	return fetch[Status](ctx, a, &call{method: "GET", path: "/v1/status"})
}

// Groups lists the groups in Throne's tab order (GET /v1/groups).
func (a api) Groups(ctx context.Context) ([]Group, error) {
	r, err := fetch[struct {
		Groups []Group `json:"groups"`
	}](ctx, a, &call{method: "GET", path: "/v1/groups"})
	if err != nil {
		return nil, err
	}
	return r.Groups, nil
}

// Profiles lists profiles (GET /v1/profiles). The zero Selection lists every profile,
// group by group in tab order; otherwise each field of sel that is set filters.
func (a api) Profiles(ctx context.Context, sel Selection) ([]Profile, error) {
	if sel.IDs != nil && len(sel.IDs) == 0 {
		return []Profile{}, nil
	}
	q := map[string]string{}
	if sel.Group != nil {
		q["group"] = strconv.Itoa(*sel.Group)
	}
	if sel.Name != "" {
		q["name"] = sel.Name
	}
	if len(sel.IDs) > 0 {
		ids := make([]string, len(sel.IDs))
		for i, id := range sel.IDs {
			ids[i] = strconv.Itoa(id)
		}
		q["ids"] = strings.Join(ids, ",")
	}
	r, err := fetch[struct {
		Profiles []Profile `json:"profiles"`
	}](ctx, a, &call{method: "GET", path: "/v1/profiles", query: q})
	if err != nil {
		return nil, err
	}
	return r.Profiles, nil
}

// Profile returns one profile (GET /v1/profiles/{id}).
func (a api) Profile(ctx context.Context, id int) (*Profile, error) {
	return fetch[Profile](ctx, a, &call{method: "GET", path: "/v1/profiles/" + strconv.Itoa(id)})
}

// Connect starts the profile with this ID (PUT /v1/connection) and returns the Status.
//
// Unless NoWait is given, Throne responds once the profile runs or failed to start. If
// the wait ends first, Connect returns the current Status, in state StateConnecting,
// without an error, and the start carries on in Throne. A profile that already runs is
// left alone, at once, unless Restart is given.
func (a api) Connect(ctx context.Context, id int, opts ...Option) (*Status, error) {
	return a.connect(ctx, map[string]any{"profile": id}, opts)
}

// ConnectLast starts the profile Throne would resume: the running one, else the one that
// ran last, else the remembered one. It waits like Connect.
func (a api) ConnectLast(ctx context.Context, opts ...Option) (*Status, error) {
	return a.connect(ctx, map[string]any{"select": "last"}, opts)
}

// ConnectLowestLatency starts the selected profile with the lowest latency. sel picks a
// group and a name, not IDs; a nil sel.Group means the group Throne shows. Throne first
// runs a URL test on every candidate without a recent result (see MaxAge), as part of
// the wait. It waits like Connect.
func (a api) ConnectLowestLatency(ctx context.Context, sel Selection, opts ...Option) (*Status, error) {
	if sel.IDs != nil {
		return nil, errors.New("throne: ConnectLowestLatency selects by group and name, not by IDs")
	}
	body := sel.fields(map[string]any{"select": "lowest_latency"})
	if o := collect(opts); o.maxAge != nil {
		body["max_age"] = *o.maxAge
	}
	return a.connect(ctx, body, opts)
}

func (a api) connect(ctx context.Context, body map[string]any, opts []Option) (*Status, error) {
	o := collect(opts)
	if o.restart {
		body["restart"] = true
	}
	return a.change(ctx, "PUT", "/v1/connection", body, o)
}

// Disconnect stops the running profile (DELETE /v1/connection) and returns the Status.
// Stopping when nothing runs is not an error. It waits like Connect.
func (a api) Disconnect(ctx context.Context, opts ...Option) (*Status, error) {
	return a.change(ctx, "DELETE", "/v1/connection", map[string]any{}, collect(opts))
}

// SetModes switches Tun and the system proxy (PUT /v1/modes) and returns the Status. A
// nil field of m stays as it is. Turning Tun on or off restarts a running profile;
// SetModes waits for that like Connect.
func (a api) SetModes(ctx context.Context, m Modes, opts ...Option) (*Status, error) {
	body := map[string]any{}
	if m.Tun != nil {
		body["tun"] = *m.Tun
	}
	if m.SystemProxy != nil {
		body["system_proxy"] = *m.SystemProxy
	}
	return a.change(ctx, "PUT", "/v1/modes", body, collect(opts))
}

// Routes lists the routing profiles (GET /v1/routes).
func (a api) Routes(ctx context.Context) ([]Route, error) {
	r, err := fetch[struct {
		Routes []Route `json:"routes"`
	}](ctx, a, &call{method: "GET", path: "/v1/routes"})
	if err != nil {
		return nil, err
	}
	return r.Routes, nil
}

// SetRoute makes a routing profile active (PUT /v1/routes/active) and returns the
// Status. A running profile restarts to apply it, which SetRoute waits for like Connect.
func (a api) SetRoute(ctx context.Context, id int, opts ...Option) (*Status, error) {
	return a.change(ctx, "PUT", "/v1/routes/active", map[string]any{"id": id}, collect(opts))
}

// change sends a request that changes the connection, with the waiting fields, and
// returns the Status.
func (a api) change(ctx context.Context, method, path string, body map[string]any, o options) (*Status, error) {
	return fetch[Status](ctx, a, waiting(ctx, method, path, body, o, defaultWait))
}

// waiting returns a call with the waiting fields added to body; def is the timeout Throne
// applies when the body names none.
func waiting(ctx context.Context, method, path string, body map[string]any, o options, def time.Duration) *call {
	wait := def
	if o.noWait {
		body["wait"] = false
		wait = 0
	} else if t, ok := timeoutFor(ctx); ok {
		body["timeout"] = t
	}
	r := &call{method: method, path: path, wait: wait}
	if len(body) > 0 {
		r.body = body
	}
	return r
}

// StartTest starts a test of the selected profiles (POST /v1/tests) and returns its Job
// at once, usually in state JobRunning. Throne runs one test at a time.
func (a api) StartTest(ctx context.Context, kind TestKind, sel Selection) (*Job, error) {
	return fetch[Job](ctx, a, &call{method: "POST", path: "/v1/tests", body: sel.fields(map[string]any{"kind": kind})})
}

// RunTest runs a test of the selected profiles to its end and returns the Job with its
// results. Throne holds the request while the test runs, and RunTest polls the Job if
// it still runs after that. If ctx ends or polling fails first, RunTest returns the last
// Job it saw along with the error; the test goes on in Throne.
func (a api) RunTest(ctx context.Context, kind TestKind, sel Selection) (*Job, error) {
	body := sel.fields(map[string]any{"kind": kind, "wait": true})
	if t, ok := timeoutFor(ctx); ok {
		body["timeout"] = t
	}
	job, err := fetch[Job](ctx, a, &call{method: "POST", path: "/v1/tests", body: body, wait: maxWait})
	for err == nil && job.State == JobRunning {
		if err := pause(ctx); err != nil {
			return job, err
		}
		var next *Job
		if next, err = a.Job(ctx, job.ID); err == nil {
			job = next
		}
	}
	return job, err
}

// Jobs lists the last tests, newest first, without their results (GET /v1/tests).
func (a api) Jobs(ctx context.Context) ([]Job, error) {
	r, err := fetch[struct {
		Jobs []Job `json:"jobs"`
	}](ctx, a, &call{method: "GET", path: "/v1/tests"})
	if err != nil {
		return nil, err
	}
	return r.Jobs, nil
}

// Job returns a test (GET /v1/tests/{id}).
func (a api) Job(ctx context.Context, id int) (*Job, error) {
	return fetch[Job](ctx, a, &call{method: "GET", path: "/v1/tests/" + strconv.Itoa(id)})
}

// CancelJob stops a running test (DELETE /v1/tests/{id}) and returns its Job, then in
// state JobCancelled. If the test has not stopped after 10 seconds, the Job comes back
// still in state JobRunning. A finished Job comes back unchanged.
func (a api) CancelJob(ctx context.Context, id int) (*Job, error) {
	return fetch[Job](ctx, a, &call{method: "DELETE", path: "/v1/tests/" + strconv.Itoa(id), wait: cancelWait})
}

// ClearResults clears the latency, speed, country and exit IP results of the selected
// profiles (DELETE /v1/results) and returns how many profiles it cleared.
func (a api) ClearResults(ctx context.Context, sel Selection) (int, error) {
	r, err := fetch[struct {
		Cleared int `json:"cleared"`
	}](ctx, a, &call{method: "DELETE", path: "/v1/results", body: sel.fields(map[string]any{})})
	if err != nil {
		return 0, err
	}
	return r.Cleared, nil
}

// LiveStats returns the current traffic rates (GET /v1/stats/live).
func (a api) LiveStats(ctx context.Context) (*LiveStats, error) {
	return fetch[LiveStats](ctx, a, &call{method: "GET", path: "/v1/stats/live"})
}

// Traffic returns traffic totals (GET /v1/stats/traffic). It fails with
// CodeStatsDisabled when traffic statistics are off in Throne.
func (a api) Traffic(ctx context.Context, q TrafficQuery) (*TrafficStats, error) {
	query := map[string]string{}
	if q.By != "" {
		query["by"] = q.By
	}
	if !q.From.IsZero() {
		query["from"] = strconv.FormatInt(q.From.Unix(), 10)
	}
	if !q.To.IsZero() {
		query["to"] = strconv.FormatInt(q.To.Unix(), 10)
	}
	return fetch[TrafficStats](ctx, a, &call{method: "GET", path: "/v1/stats/traffic", query: query})
}

// Selection picks profiles: by ID, or by group and name.
type Selection struct {
	// IDs picks these profiles, in this order.
	IDs []int
	// Group picks the profiles of this group, in Throne's order. Nil means no group;
	// ConnectLowestLatency then uses the group Throne shows.
	Group *int
	// Name keeps the profiles whose names match this case-insensitive regular expression
	// (PCRE syntax) anywhere. "" keeps every profile. Tests and ClearResults take Name
	// only with Group, not with IDs.
	Name string
}

// SelectProfiles picks the profiles with these IDs, in this order.
func SelectProfiles(ids ...int) Selection {
	if ids == nil {
		ids = []int{}
	}
	return Selection{IDs: ids}
}

// SelectGroup picks the profiles of a group whose names match name; "" matches every name.
func SelectGroup(group int, name string) Selection {
	return Selection{Group: &group, Name: name}
}

// fields adds the selection to a request body.
func (s Selection) fields(body map[string]any) map[string]any {
	if s.IDs != nil {
		body["profiles"] = s.IDs
	}
	if s.Group != nil {
		body["group"] = *s.Group
	}
	if s.Name != "" {
		body["name"] = s.Name
	}
	return body
}

// Modes are the modes SetModes changes. A nil field stays as it is.
type Modes struct {
	Tun         *bool `json:"tun,omitempty"`
	SystemProxy *bool `json:"system_proxy,omitempty"`
}

// TrafficQuery selects traffic totals. The zero value asks for the totals by profile
// over the last 24 hours.
type TrafficQuery struct {
	// By is TrafficByProfile or TrafficByApp; "" means by profile.
	By string
	// From and To bound the period. A zero time leaves that bound to Throne, which
	// defaults to the last 24 hours.
	From, To time.Time
}

// An Option adjusts a request that changes something and can wait for it: Connect,
// ConnectLast, ConnectLowestLatency, Disconnect, SetModes and SetRoute, and the management
// requests Import, AddGroup, UpdateGroup, UpdateGroups, DeleteGroup, DeleteProfiles,
// UpdateRuleSets, UpdateGeoAssets, Backup and ApplyRestore. Each Option says which of them
// it affects; the others ignore it.
type Option func(*options)

type options struct {
	noWait      bool
	restart     bool
	maxAge      *int
	onlyMissing bool
}

func collect(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// NoWait makes Throne respond at once, rather than when the change has finished; the
// change carries on in Throne. The requests that change the connection then return the
// current Status, and the others what their documentation gives for a wait that ended
// first. It affects every request that takes Options.
func NoWait() Option {
	return func(o *options) { o.noWait = true }
}

// Restart makes Connect, ConnectLast and ConnectLowestLatency restart a profile that
// already runs.
func Restart() Option {
	return func(o *options) { o.restart = true }
}

// MaxAge makes ConnectLowestLatency count only latency results younger than d, and test
// the other candidates first. Throne's default is one hour; MaxAge(0) tests them all.
func MaxAge(d time.Duration) Option {
	s := max(int(d/time.Second), 0)
	return func(o *options) { o.maxAge = &s }
}

// OnlyMissing makes UpdateGeoAssets skip the files that are present.
func OnlyMissing() Option {
	return func(o *options) { o.onlyMissing = true }
}

// pause waits pollInterval, or returns ctx's error when ctx ends first.
func pause(ctx context.Context) error {
	t := time.NewTimer(pollInterval)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("throne: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// timeoutFor turns ctx's deadline into the timeout field: the time left minus a margin,
// in whole seconds from 1 to 300.
func timeoutFor(ctx context.Context) (int, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	t := int((time.Until(deadline) - waitMargin) / time.Second)
	return min(max(t, 1), int(maxWait/time.Second)), true
}
