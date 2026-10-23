package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	throne "github.com/throneproj/Throne-api"
)

// handleProxy passes /api/v1/<rest> to Throne's /v1/<rest>.
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request, _ *session) {
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		w.Header().Set("Allow", "GET, POST, PUT, DELETE")
		writeError(w, http.StatusMethodNotAllowed, throne.CodeMethodNotAllowed, "Use GET, POST, PUT or DELETE")
		return
	}
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/v1/")
	if !ok || !validRest(rest) {
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "Invalid path")
		return
	}
	values, err := parseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "Invalid query string")
		return
	}
	if r.Method == http.MethodGet {
		// Held reads of /v1/events and /v1/logs belong to the streams, not to a shared connection.
		delete(values, "timeout")
	}
	var query map[string]string
	if len(values) > 0 {
		query = values
	}
	var body any
	if r.Method != http.MethodGet {
		b, ok := s.readBody(w, r, maxSmallBody)
		if !ok {
			return
		}
		if b = bytes.TrimSpace(b); len(b) > 0 {
			if !json.Valid(b) {
				writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "The body is not valid JSON")
				return
			}
			body = json.RawMessage(b)
		}
	}
	change := r.Method != http.MethodGet
	if !s.acquire(r.Context(), change) {
		return
	}
	resp, err := s.throne.Send(r.Context(), r.Method, "/v1/"+rest, query, body)
	s.release(change)
	if err != nil {
		if r.Context().Err() == nil {
			s.writeThroneError(w, err)
		}
		return
	}
	out := []byte(resp.Body)
	if len(out) == 0 {
		out = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.Status)
	w.Write(out)
}

// validRest accepts the rest of a Throne path: segments of letters, digits and -._~,
// none of them empty, . or ..
func validRest(rest string) bool {
	if rest == "" {
		return false
	}
	for seg := range strings.SplitSeq(rest, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0) {
				return false
			}
		}
	}
	return true
}

// parseQuery keeps the first value of each key.
func parseQuery(raw string) (map[string]string, error) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(values))
	for k, v := range values {
		m[k] = v[0]
	}
	return m, nil
}
