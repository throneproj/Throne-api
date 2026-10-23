package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	throne "github.com/throneproj/Throne-api"
)

// readBody reads a small request body of at most limit bytes within bodyRead, or writes
// the error and returns false.
func (s *Server) readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(s.t.bodyRead))
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	// The server's background read after the body must not inherit the deadline: its
	// timeout would cancel the request's context.
	rc.SetReadDeadline(time.Time{})
	if err != nil {
		writeBodyError(w, r, err, limit)
		return nil, false
	}
	return b, true
}

// writeBodyError answers a request whose body could not be read.
func writeBodyError(w http.ResponseWriter, r *http.Request, err error, limit int64) {
	closeUnread(w, r)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, throne.CodeTooLarge, "The body is larger than "+sizeText(limit))
	case errors.Is(err, os.ErrDeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, "request_timeout", "The body did not arrive in time")
	default:
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "The body could not be read")
	}
}

func sizeText(n int64) string {
	if n >= 1<<20 {
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n>>10, 10) + " KiB"
}

// readJSON decodes a JSON body into v, or writes the error and returns false.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	b, ok := s.readBody(w, r, limit)
	if !ok {
		return false
	}
	if json.Unmarshal(b, v) != nil {
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "The body is not the expected JSON object")
		return false
	}
	return true
}

// readOptionalJSON is readJSON for a body that may be empty.
func (s *Server) readOptionalJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	b, ok := s.readBody(w, r, limit)
	if !ok {
		return false
	}
	if len(bytes.TrimSpace(b)) > 0 && json.Unmarshal(b, v) != nil {
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "The body is not the expected JSON object")
		return false
	}
	return true
}

// closeUnread closes an HTTP/1 connection after the response to a request whose body is
// left unread: net/http would otherwise read the rest of the body, without a deadline,
// before it answers. HTTP/2 needs nothing, and takes Connection: close as a GOAWAY for
// all the connection's streams.
func closeUnread(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 1 && r.ContentLength != 0 {
		w.Header().Set("Connection", "close")
	}
}
