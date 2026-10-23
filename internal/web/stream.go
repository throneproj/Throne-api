package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// allTopics are the topics of /v1/events, in Throne's order.
var allTopics = []string{throne.TopicStatus, throne.TopicGroups, throne.TopicProfiles, throne.TopicTests, throne.TopicSettings}

const (
	logBacklog = 500 // lines of the first lines event
	logPage    = 500 // lines per later request
)

// sseWriter writes server-sent events.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func (s *Server) startSSE(w http.ResponseWriter) (*sseWriter, error) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sw := &sseWriter{w: w, rc: http.NewResponseController(w), timeout: s.t.streamWrite}
	return sw, sw.flush()
}

func (sw *sseWriter) event(name string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	sw.rc.SetWriteDeadline(time.Now().Add(sw.timeout))
	if _, err := fmt.Fprintf(sw.w, "event: %s\ndata: %s\n\n", name, b); err != nil {
		return err
	}
	return sw.flush()
}

func (sw *sseWriter) ping() error {
	sw.rc.SetWriteDeadline(time.Now().Add(sw.timeout))
	if _, err := io.WriteString(sw.w, ": ping\n\n"); err != nil {
		return err
	}
	return sw.flush()
}

func (sw *sseWriter) flush() error {
	err := sw.rc.Flush()
	sw.rc.SetWriteDeadline(time.Time{})
	return err
}

// throneState is the data of a throne event.
type throneState struct {
	Reachable bool   `json:"reachable"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (s *Server) unreachable(err error) *throneState {
	_, code, message := s.classify(err)
	return &throneState{Code: code, Message: message}
}

// transient says whether an error of a stream's call to Throne is worth retrying: any
// but an API error other than 503, which Throne answers while it starts, restarts or
// exits.
func transient(err error) bool {
	var apiErr *throne.APIError
	return !errors.As(err, &apiErr) || apiErr.Status == http.StatusServiceUnavailable
}

// sleep waits d, or returns false when ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// hub follows Throne's /v1/events for every /api/events stream, while there is one.
type hub struct {
	s    *Server
	mu   sync.Mutex
	subs map[*eventSub]struct{}
	gen  uint64
	stop context.CancelFunc
	// down is Throne's state while the hub cannot reach it, else nil.
	down *throneState
}

func newHub(s *Server) *hub {
	return &hub{s: s, subs: map[*eventSub]struct{}{}}
}

// eventSub is one stream's events that are not written yet: topics, and the last
// change in reachability.
type eventSub struct {
	mu     sync.Mutex
	topics map[string]bool
	throne *throneState
	wake   chan struct{}
}

func (sub *eventSub) add(topics []string, st *throneState) {
	sub.mu.Lock()
	for _, t := range topics {
		sub.topics[t] = true
	}
	if st != nil {
		sub.throne = st
	}
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// write writes the pending events. Topics that changed before Throne went away go
// first; after it came back, the throne event goes first, since every topic follows it.
func (sub *eventSub) write(sw *sseWriter) error {
	sub.mu.Lock()
	var topics []string
	for _, t := range allTopics {
		if sub.topics[t] {
			topics = append(topics, t)
		}
	}
	clear(sub.topics)
	st := sub.throne
	sub.throne = nil
	sub.mu.Unlock()
	if st != nil && st.Reachable {
		if err := sw.event("throne", st); err != nil {
			return err
		}
		st = nil
	}
	if len(topics) > 0 {
		if err := sw.event("changes", map[string][]string{"changes": topics}); err != nil {
			return err
		}
	}
	if st != nil {
		return sw.event("throne", st)
	}
	return nil
}

// subscribe adds a stream, which starts with every topic, and starts following Throne
// if this is the first.
func (h *hub) subscribe() *eventSub {
	sub := &eventSub{topics: map[string]bool{}, wake: make(chan struct{}, 1)}
	h.mu.Lock()
	defer h.mu.Unlock()
	sub.add(allTopics, h.down)
	h.subs[sub] = struct{}{}
	if h.stop == nil {
		var ctx context.Context
		ctx, h.stop = context.WithCancel(h.s.ctx)
		h.gen++
		go h.run(ctx, h.gen)
	}
	return sub
}

// unsubscribe removes a stream; without streams the hub stops following Throne and
// forgets what it knew.
func (h *hub) unsubscribe(sub *eventSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub)
	if len(h.subs) == 0 && h.stop != nil {
		h.stop()
		h.stop, h.down = nil, nil
		h.gen++
	}
}

// broadcast passes topics and a reachability change to every stream, unless the loop
// of generation gen was stopped meanwhile.
func (h *hub) broadcast(gen uint64, topics []string, st *throneState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if gen != h.gen {
		return
	}
	for sub := range h.subs {
		sub.add(topics, st)
	}
}

// lost records that Throne is unreachable; streams hear of it when the reason changes.
func (h *hub) lost(gen uint64, st *throneState) {
	h.mu.Lock()
	if gen != h.gen || (h.down != nil && *h.down == *st) {
		h.mu.Unlock()
		return
	}
	h.down = st
	h.mu.Unlock()
	h.broadcast(gen, nil, st)
}

// reached records that Throne answered; after an outage every stream gets the news and
// every topic.
func (h *hub) reached(gen uint64) {
	h.mu.Lock()
	was := h.down
	if gen == h.gen {
		h.down = nil
	}
	h.mu.Unlock()
	if was != nil {
		h.broadcast(gen, allTopics, &throneState{Reachable: true})
	}
}

// run follows Throne until ctx ends: long polls of /v1/events, or for a Throne without
// them, polls of /v1/status.
func (h *hub) run(ctx context.Context, gen uint64) {
	var p eventPoll
	for ctx.Err() == nil {
		var err error
		if p.legacy {
			err = h.pollStatus(ctx, gen, &p)
		} else {
			err = h.pollEvents(ctx, gen, &p)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.lost(gen, h.s.unreachable(err))
			p = eventPoll{}
			sleep(ctx, h.s.t.retry)
		}
	}
}

// eventPoll is where the hub stands in Throne's events.
type eventPoll struct {
	instance string
	cursor   int64
	legacy   bool
	status   []byte
	full     time.Time
}

func (h *hub) pollEvents(ctx context.Context, gen uint64, p *eventPoll) error {
	start := time.Now()
	ev, err := h.s.throne.Events(ctx, throne.EventsQuery{Instance: p.instance, After: p.cursor, Wait: h.s.t.eventsWait})
	if throne.ErrorCode(err) == throne.CodeNotFound {
		h.reached(gen)
		p.legacy, p.full = true, time.Now()
		return nil
	}
	if err != nil {
		return err
	}
	h.reached(gen)
	// The first answer names every topic, which the streams got when they started.
	if p.instance != "" && len(ev.Changes) > 0 {
		h.broadcast(gen, ev.Changes, nil)
	}
	p.instance, p.cursor = ev.Instance, ev.Cursor
	if len(ev.Changes) == 0 && time.Since(start) < h.s.t.minPoll {
		sleep(ctx, h.s.t.minPoll)
	}
	return nil
}

func (h *hub) pollStatus(ctx context.Context, gen uint64, p *eventPoll) error {
	resp, err := h.s.throne.Send(ctx, http.MethodGet, "/v1/status", nil, nil)
	if err != nil {
		return err
	}
	h.reached(gen)
	switch {
	case time.Since(p.full) >= h.s.t.fullRefresh:
		p.full = time.Now()
		h.broadcast(gen, allTopics, nil)
	case p.status != nil && string(p.status) != string(resp.Body):
		h.broadcast(gen, []string{throne.TopicStatus}, nil)
	}
	p.status = resp.Body
	sleep(ctx, h.s.t.statusPoll)
	return nil
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, sess *session) {
	sw, err := s.startSSE(w)
	if err != nil {
		return
	}
	sub := s.hub.subscribe()
	defer s.hub.unsubscribe(sub)
	ping := time.NewTicker(s.t.ping)
	defer ping.Stop()
	for {
		if err := sub.write(sw); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case <-sess.done:
			return
		case <-sub.wake:
		case <-ping.C:
			if !s.sessions.touch(sess) || sw.ping() != nil {
				return
			}
		}
	}
}

// sseEvent is an event for a stream's writer.
type sseEvent struct {
	name string
	data any
}

type logLines struct {
	Lines   []throne.LogLine `json:"lines"`
	Dropped int64            `json:"dropped"`
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, sess *session) {
	sw, err := s.startSSE(w)
	if err != nil {
		return
	}
	sub := s.logs.subscribe()
	defer s.logs.unsubscribe(sub)
	ping := time.NewTicker(s.t.ping)
	defer ping.Stop()
	for {
		events, end := sub.take()
		for _, ev := range events {
			if sw.event(ev.name, ev.data) != nil {
				return
			}
		}
		if end {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			return
		case <-sess.done:
			return
		case <-sub.wake:
		case <-ping.C:
			if !s.sessions.touch(sess) || sw.ping() != nil {
				return
			}
		}
	}
}

// logFollower follows Throne's log for every /api/logs stream, while there is one, over
// a single connection. It keeps the newest lines for the streams that start later.
type logFollower struct {
	s    *Server
	mu   sync.Mutex
	subs map[*logSub]struct{}
	gen  uint64
	stop context.CancelFunc
	// ring holds the newest lines once loaded, which the first answer does.
	ring   []throne.LogLine
	loaded bool
	down   *throneState
}

func newLogFollower(s *Server) *logFollower {
	return &logFollower{s: s, subs: map[*logSub]struct{}{}}
}

// logSub is one stream's events that are not written yet.
type logSub struct {
	mu    sync.Mutex
	queue []sseEvent
	end   bool // the queue ends the stream
	wake  chan struct{}
}

// push queues an event; lines join queued lines, of which the newest logBacklog stay and
// the others count as dropped.
func (sub *logSub) push(name string, data any) {
	sub.mu.Lock()
	if n := len(sub.queue); name == "lines" && n > 0 && sub.queue[n-1].name == "lines" {
		last, next := sub.queue[n-1].data.(logLines), data.(logLines)
		merged := append(append([]throne.LogLine(nil), last.Lines...), next.Lines...)
		dropped := last.Dropped + next.Dropped
		if cut := len(merged) - logBacklog; cut > 0 {
			merged, dropped = merged[cut:], dropped+int64(cut)
		}
		sub.queue[n-1].data = logLines{merged, dropped}
	} else {
		sub.queue = append(sub.queue, sseEvent{name, data})
	}
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// finish queues the error event that ends the stream.
func (sub *logSub) finish(e apiError) {
	sub.mu.Lock()
	sub.queue, sub.end = append(sub.queue, sseEvent{"error", e}), true
	sub.mu.Unlock()
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

func (sub *logSub) take() ([]sseEvent, bool) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	q := sub.queue
	sub.queue = nil
	return q, sub.end
}

// subscribe adds a stream, which gets the kept lines at once, and starts following the
// log if this is the first.
func (f *logFollower) subscribe() *logSub {
	sub := &logSub{wake: make(chan struct{}, 1)}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loaded {
		sub.push("lines", logLines{slices.Clone(f.ring), 0})
	}
	if f.down != nil {
		sub.push("throne", f.down)
	}
	f.subs[sub] = struct{}{}
	if f.stop == nil {
		var ctx context.Context
		ctx, f.stop = context.WithCancel(f.s.ctx)
		f.gen++
		go f.run(ctx, f.gen)
	}
	return sub
}

func (f *logFollower) unsubscribe(sub *logSub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs, sub)
	if len(f.subs) == 0 {
		f.halt()
	}
}

// halt stops following the log and forgets it; f.mu is held.
func (f *logFollower) halt() {
	if f.stop != nil {
		f.stop()
	}
	f.stop, f.ring, f.loaded, f.down = nil, nil, false, nil
	f.gen++
}

// run follows the log until ctx ends or Throne answers with an error that retrying does
// not mend.
func (f *logFollower) run(ctx context.Context, gen uint64) {
	var instance string
	var cursor int64
	var more bool
	for ctx.Err() == nil {
		q := throne.LogQuery{Limit: logBacklog}
		if instance != "" {
			q = throne.LogQuery{Instance: instance, After: cursor, Limit: logPage}
			if !more {
				q.Wait = f.s.t.eventsWait
			}
		}
		start := time.Now()
		l, err := f.s.throne.Logs(ctx, q)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			st := f.s.unreachable(err)
			if !transient(err) {
				f.fail(gen, apiError{st.Code, st.Message})
				return
			}
			f.lost(gen, st)
			sleep(ctx, f.s.t.retry)
			continue
		}
		f.got(gen, instance, l)
		instance, cursor, more = l.Instance, l.Cursor, l.More
		if !more && len(l.Lines) == 0 && time.Since(start) < f.s.t.minPoll {
			sleep(ctx, f.s.t.minPoll)
		}
	}
}

// got passes on an answer: the first is the backlog, one of another instance means that
// Throne restarted.
func (f *logFollower) got(gen uint64, instance string, l *throne.Logs) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen != f.gen {
		return
	}
	if f.down != nil {
		f.down = nil
		f.each(func(sub *logSub) { sub.push("throne", &throneState{Reachable: true}) })
	}
	lines := l.Lines
	if lines == nil {
		lines = []throne.LogLine{}
	}
	switch {
	case !f.loaded || l.Instance != instance:
		reset := f.loaded
		f.ring, f.loaded = keepNewest(slices.Clone(lines)), true
		f.each(func(sub *logSub) {
			if reset {
				sub.push("reset", struct{}{})
			}
			sub.push("lines", logLines{slices.Clone(f.ring), 0})
		})
	case len(lines) > 0 || l.Dropped > 0:
		f.ring = keepNewest(append(f.ring, lines...))
		f.each(func(sub *logSub) { sub.push("lines", logLines{lines, l.Dropped}) })
	}
}

// keepNewest returns the newest logBacklog lines.
func keepNewest(lines []throne.LogLine) []throne.LogLine {
	if cut := len(lines) - logBacklog; cut > 0 {
		return append(lines[:0:0], lines[cut:]...)
	}
	return lines
}

// lost tells the streams that Throne is unreachable, when the reason changes.
func (f *logFollower) lost(gen uint64, st *throneState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen != f.gen || (f.down != nil && *f.down == *st) {
		return
	}
	f.down = st
	f.each(func(sub *logSub) { sub.push("throne", st) })
}

// fail ends every stream with an error event, and stops.
func (f *logFollower) fail(gen uint64, e apiError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if gen != f.gen {
		return
	}
	f.each(func(sub *logSub) {
		sub.finish(e)
		delete(f.subs, sub)
	})
	f.halt()
}

// each runs fn for every stream; f.mu is held.
func (f *logFollower) each(fn func(sub *logSub)) {
	for sub := range f.subs {
		fn(sub)
	}
}
