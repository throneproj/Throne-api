package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed ui
var ui embed.FS

func embedded() fs.FS {
	sub, err := fs.Sub(ui, "ui")
	if err != nil {
		panic(err)
	}
	return sub
}

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".svg":         "image/svg+xml",
	".json":        "application/json",
	".webmanifest": "application/manifest+json",
	".png":         "image/png",
	".ico":         "image/x-icon",
	".webp":        "image/webp",
	".woff2":       "font/woff2",
	".txt":         "text/plain; charset=utf-8",
	".md":          "text/plain; charset=utf-8",
}

// serveAsset serves the browser app's files: / is index.html, and there is no listing of
// directories.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	if fi, err := fs.Stat(s.assets, name); err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct, ok := contentTypes[strings.ToLower(path.Ext(name))]
	if !ok {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if strings.HasPrefix(name, "vendor/") {
		w.Header().Set("Cache-Control", "max-age=86400")
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
