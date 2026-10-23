package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	throne "github.com/throneproj/Throne-api"
	"github.com/throneproj/Throne-api/internal/wire"
)

// tracked notes when Throne last answered a call, which tells a closed connection from a
// wrong key.
type tracked struct {
	Throne
	s *Server
}

func (t tracked) Send(ctx context.Context, method, path string, query map[string]string, body any) (*throne.Response, error) {
	r, err := t.Throne.Send(ctx, method, path, query, body)
	t.s.observe(err)
	return r, err
}

func (t tracked) Events(ctx context.Context, q throne.EventsQuery) (*throne.Events, error) {
	ev, err := t.Throne.Events(ctx, q)
	t.s.observe(err)
	return ev, err
}

func (t tracked) Logs(ctx context.Context, q throne.LogQuery) (*throne.Logs, error) {
	l, err := t.Throne.Logs(ctx, q)
	t.s.observe(err)
	return l, err
}

func (t tracked) Backup(ctx context.Context, w io.Writer, parts []string, opts ...throne.Option) (*throne.BackupInfo, error) {
	info, err := t.Throne.Backup(ctx, w, parts, opts...)
	t.s.observe(err)
	return info, err
}

func (t tracked) UploadRestore(ctx context.Context, r io.ReadSeeker) (*throne.RestoreUpload, error) {
	up, err := t.Throne.UploadRestore(ctx, r)
	t.s.observe(err)
	return up, err
}

// observe records an answer from Throne: a success, or an API error, which proves the
// key as well.
func (s *Server) observe(err error) {
	var apiErr *throne.APIError
	if err == nil || errors.As(err, &apiErr) {
		s.lastOK.Store(s.now().UnixNano())
	}
}

// answeredRecently says whether Throne answered a call within recentOK.
func (s *Server) answeredRecently() bool {
	last := s.lastOK.Load()
	return last != 0 && s.now().Sub(time.Unix(0, last)) < s.t.recentOK
}

// acquire takes one of the connections to Throne that requests share, or returns false
// when ctx ends first; release gives it back. A change, any request but a GET, may wait
// for Throne long, so changes leave one connection to GETs.
func (s *Server) acquire(ctx context.Context, change bool) bool {
	if change {
		select {
		case s.changes <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}
	select {
	case s.calls <- struct{}{}:
		return true
	case <-ctx.Done():
		if change {
			<-s.changes
		}
		return false
	}
}

func (s *Server) release(change bool) {
	<-s.calls
	if change {
		<-s.changes
	}
}

// writeThroneError answers with Throne's error, or with the gateway's for a failure to
// reach Throne.
func (s *Server) writeThroneError(w http.ResponseWriter, err error) {
	status, code, message := s.classify(err)
	writeError(w, status, code, message)
}

// classify turns an error of a call to Throne into a status, code and message: Throne's
// own for an *throne.APIError.
func (s *Server) classify(err error) (int, string, string) {
	var apiErr *throne.APIError
	addr := s.cfg.ThroneAddr
	detail := strings.TrimPrefix(err.Error(), "throne: ")
	switch {
	case errors.As(err, &apiErr):
		code := apiErr.Code
		if code == "" {
			code = "error"
		}
		return apiErr.Status, code, apiErr.Message
	case errors.Is(err, throne.ErrRejected) && s.answeredRecently():
		return http.StatusBadGateway, codeThroneUnreachable, "Throne at " + addr + " closed the connection without " +
			"a response: it may be restarting, or busy with too many connections"
	case errors.Is(err, throne.ErrRejected):
		return http.StatusBadGateway, codeThroneRejected, "Throne at " + addr + " closed the connection without a " +
			"response: the gateway's key is probably wrong, or Throne refuses the gateway's address"
	case errors.Is(err, wire.ErrTooLarge):
		return http.StatusRequestEntityTooLarge, throne.CodeTooLarge, "The request is too large for the Throne API"
	case errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "connect to "):
		return http.StatusGatewayTimeout, codeThroneTimeout, "Throne at " + addr + " did not answer in time"
	}
	return http.StatusBadGateway, codeThroneUnreachable, "Throne at " + addr + " is not reachable: " + detail
}
