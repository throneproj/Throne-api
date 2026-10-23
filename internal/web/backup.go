package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	throne "github.com/throneproj/Throne-api"
)

const (
	tempBackup  = ".backup-"
	tempRestore = ".restore-"
	// maxRestore bounds an uploaded archive.
	maxRestore = 512 << 20
	// uploadIdle is how long a restore upload may stall.
	uploadIdle = time.Minute
)

// handleBackup has Throne build an archive, takes it into a temporary file, and sends it
// once it is complete and checked, so that a failure never looks like a short download.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request, _ *session) {
	var body struct {
		Parts []string `json:"parts"`
	}
	if !s.readOptionalJSON(w, r, maxSmallBody, &body) {
		return
	}
	f, err := os.CreateTemp(s.cfg.StateDir, tempBackup+"*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, throne.CodeInternal, "Could not create a temporary file: "+err.Error())
		return
	}
	defer removeTemp(f)
	if !s.acquire(r.Context(), true) {
		return
	}
	info, err := s.throne.Backup(r.Context(), f, body.Parts)
	s.release(true)
	if err != nil {
		if r.Context().Err() == nil {
			s.writeThroneError(w, err)
		}
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, throne.CodeInternal, "Could not read the archive: "+err.Error())
		return
	}
	name := "throne-backup-" + s.now().Format("20060102-150405") + ".thrbackup"
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	w.WriteHeader(http.StatusOK)
	io.CopyN(w, f, info.Size)
}

// handleRestore takes an archive into a temporary file and uploads it to Throne.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request, _ *session) {
	select {
	case s.restore <- struct{}{}:
		defer func() { <-s.restore }()
	default:
		writeError(w, http.StatusConflict, throne.CodeBusy, "Another archive is being uploaded")
		return
	}
	f, err := os.CreateTemp(s.cfg.StateDir, tempRestore+"*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, throne.CodeInternal, "Could not create a temporary file: "+err.Error())
		return
	}
	defer removeTemp(f)
	rc := http.NewResponseController(w)
	body := &idleReader{r: http.MaxBytesReader(w, r.Body, maxRestore), rc: rc}
	n, err := io.Copy(fileWriter{f}, body)
	rc.SetReadDeadline(time.Time{})
	switch {
	case errors.Is(err, errWrite):
		writeError(w, http.StatusInternalServerError, throne.CodeInternal, "Could not store the archive: "+err.Error())
		return
	case err != nil:
		writeBodyError(w, r, err, maxRestore)
		return
	case n == 0:
		writeError(w, http.StatusBadRequest, throne.CodeBadRequest, "The archive is empty")
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		writeError(w, http.StatusInternalServerError, throne.CodeInternal, "Could not read the archive: "+err.Error())
		return
	}
	if !s.acquire(r.Context(), true) {
		return
	}
	up, err := s.throne.UploadRestore(r.Context(), f)
	s.release(true)
	if err != nil {
		if r.Context().Err() == nil {
			s.writeThroneError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, up)
}

var errWrite = errors.New("write")

// fileWriter tells write errors apart from the read errors of io.Copy.
type fileWriter struct {
	f *os.File
}

func (fw fileWriter) Write(p []byte) (int, error) {
	n, err := fw.f.Write(p)
	if err != nil {
		err = fmt.Errorf("%w: %w", errWrite, err)
	}
	return n, err
}

// idleReader reads a request body with a read deadline that each read pushes back.
type idleReader struct {
	r  io.Reader
	rc *http.ResponseController
}

func (ir *idleReader) Read(p []byte) (int, error) {
	ir.rc.SetReadDeadline(time.Now().Add(uploadIdle))
	return ir.r.Read(p)
}

func removeTemp(f *os.File) {
	f.Close()
	os.Remove(f.Name())
}

// globIn returns the names in dir that match pattern.
func globIn(dir, pattern string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if ok, _ := filepath.Match(pattern, e.Name()); ok {
			names = append(names, filepath.Join(dir, e.Name()))
		}
	}
	return names, nil
}
