package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"

	throne "github.com/throneproj/Throne-api"
)

// masked stands for a value that differs from run to run. The key stays, so a field that
// appears or disappears still shows in a diff.
const masked = "<masked>"

// runKeys name the fields whose values depend on the run: timestamps, measurements,
// counters, subscription churn and the Throne build.
var runKeys = map[string]bool{
	"latency_ms":    true,
	"latency_at":    true,
	"download_bps":  true,
	"upload_bps":    true,
	"country":       true,
	"ip_out":        true,
	"traffic_up":    true,
	"traffic_down":  true,
	"started_at":    true,
	"finished_at":   true,
	"updated_at":    true,
	"profile_count": true,
	"up":            true,
	"down":          true,
	"from":          true,
	"to":            true,
	"version":       true,
}

var jobPath = regexp.MustCompile(`^/v1/tests/\d+$`)

func normalizePath(path string) string {
	if jobPath.MatchString(path) {
		return "/v1/tests/{job}"
	}
	return path
}

// normalize decodes a JSON value, lets view reshape it while it still holds the real IDs,
// and masks what differs between runs.
func normalize(raw []byte, view func(any) any) any {
	if len(raw) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return fmt.Sprintf("<undecodable: %v>", err)
	}
	if view != nil {
		v = view(v)
	}
	return mask(v)
}

func mask(v any) any {
	switch v := v.(type) {
	case map[string]any:
		_, hasKind := v["kind"]
		_, hasIDs := v["profile_ids"]
		for k, x := range v {
			if runKeys[k] || k == "id" && hasKind && hasIDs {
				v[k] = masked
			} else {
				v[k] = mask(x)
			}
		}
	case []any:
		for i, x := range v {
			v[i] = mask(x)
		}
	}
	return v
}

// numberID reads an ID that normalize decoded as a json.Number.
func numberID(v any) (int, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	id, err := strconv.Atoi(n.String())
	return id, err == nil
}

// idsView reduces a profile list to which of want it holds, so a group whose other
// profiles change between runs still diffs cleanly.
func idsView(want []int) func(any) any {
	return func(v any) any {
		found := map[int]bool{}
		if m, ok := v.(map[string]any); ok {
			list, _ := m["profiles"].([]any)
			for _, p := range list {
				if pm, ok := p.(map[string]any); ok {
					if id, ok := numberID(pm["id"]); ok {
						found[id] = true
					}
				}
			}
		}
		includes := []int{}
		for _, id := range want {
			if found[id] {
				includes = append(includes, id)
			}
		}
		return map[string]any{"includes": includes}
	}
}

// jobView keeps only the job with this ID from a job list, which also holds the jobs of
// earlier runs.
func jobView(id int) func(any) any {
	return func(v any) any {
		m, _ := v.(map[string]any)
		list, _ := m["jobs"].([]any)
		for _, j := range list {
			if jm, ok := j.(map[string]any); ok {
				if got, ok := numberID(jm["id"]); ok && got == id {
					return map[string]any{"job": jm}
				}
			}
		}
		return map[string]any{"job": nil}
	}
}

// cancelView reduces a job that a cancel raced: cancelled and done are both correct
// outcomes, and its profiles are a whole group.
func cancelView(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	if _, ok := m["profile_ids"]; ok {
		m["profile_ids"] = masked
	}
	if _, ok := m["results"]; ok {
		m["results"] = masked
	}
	if s, _ := m["state"].(string); s == throne.JobCancelled || s == throne.JobDone {
		m["state"] = throne.JobCancelled + "|" + throne.JobDone
	}
	return m
}

type request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  map[string]string `json:"query,omitempty"`
	Body   any               `json:"body,omitempty"`
}

// failure is an error response without its message, which may be translated.
type failure struct {
	Status int    `json:"status,omitempty"`
	Code   string `json:"code"`
}

type entry struct {
	Step     string   `json:"step"`
	Request  *request `json:"request,omitempty"`
	Response any      `json:"response,omitempty"`
	Error    *failure `json:"error,omitempty"`
	Value    any      `json:"value,omitempty"`
}

type transcript struct {
	Subtest string  `json:"subtest"`
	Skipped string  `json:"skipped,omitempty"`
	Failed  bool    `json:"failed,omitempty"`
	Entries []entry `json:"entries"`
}

// caller is what throne.Client and throne.Session offer for any request.
type caller interface {
	Call(ctx context.Context, method, path string, query map[string]string, body, out any) error
}

// exchange is one request to send and record. view, when set, reshapes the response
// before it is masked.
type exchange struct {
	step   string
	method string
	path   string
	query  map[string]string
	body   map[string]any
	view   func(any) any
}

func (x exchange) request() *request {
	r := &request{Method: x.method, Path: normalizePath(x.path), Query: x.query}
	if x.body != nil {
		b, err := json.Marshal(x.body)
		if err != nil {
			panic(err)
		}
		r.Body = normalize(b, nil)
	}
	return r
}

// send makes the request without recording it.
func send(ctx context.Context, c caller, x exchange) (json.RawMessage, error) {
	var body any
	if x.body != nil {
		body = x.body
	}
	var raw json.RawMessage
	err := c.Call(ctx, x.method, x.path, x.query, body, &raw)
	return raw, err
}

func failureOf(err error) *failure {
	var e *throne.APIError
	switch {
	case errors.As(err, &e):
		return &failure{Status: e.Status, Code: e.Code}
	case errors.Is(err, throne.ErrRejected):
		return &failure{Code: "rejected"}
	case errors.Is(err, context.DeadlineExceeded):
		return &failure{Code: "timeout"}
	}
	return &failure{Code: "transport"}
}

// recorder collects the transcript of one subtest. It is safe for concurrent use.
type recorder struct {
	mu      sync.Mutex
	entries []entry
	skipped string
}

func (r *recorder) add(e entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
}

// record adds a request and its outcome, as send returned them.
func (r *recorder) record(x exchange, raw json.RawMessage, err error) {
	e := entry{Step: x.step, Request: x.request()}
	if err != nil {
		e.Error = failureOf(err)
	} else {
		e.Response = normalize(raw, x.view)
	}
	r.add(e)
}

// note adds a fact that is not a single exchange, such as the outcome of a race.
func (r *recorder) note(step string, value any) {
	r.add(entry{Step: step, Value: value})
}

// skip skips the subtest and keeps the reason in its transcript.
func (r *recorder) skip(t testing.TB, reason string) {
	t.Helper()
	r.mu.Lock()
	r.skipped = reason
	r.mu.Unlock()
	t.Skip(reason)
}

func (r *recorder) write(dir, file, subtest string, failed bool) error {
	r.mu.Lock()
	tr := transcript{Subtest: subtest, Skipped: r.skipped, Failed: failed, Entries: r.entries}
	r.mu.Unlock()
	if tr.Entries == nil {
		tr.Entries = []entry{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tr); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, file), buf.Bytes(), 0o600)
}
