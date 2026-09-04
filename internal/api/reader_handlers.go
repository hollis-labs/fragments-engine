package api

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

const readerAPIPrincipal = "local-user"

func (s *Server) handleReaderItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(r.URL.Query()["scope"]) != 1 || len(r.URL.Query()["cursor"]) > 1 {
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader query", "scope is required once and cursor may appear at most once")
		return
	}
	instance, ok := s.openReaderApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.Reader.List(r.Context(), service.ReaderListRequest{
		Scope: r.URL.Query().Get("scope"), Cursor: r.URL.Query().Get("cursor"), PrincipalID: readerAPIPrincipal,
	})
	if err != nil {
		writeReaderError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReaderItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fragmentID, err := readerFragmentID(r.URL.EscapedPath())
	if err != nil {
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Reader item not found", "")
		return
	}
	values, revisionPresent := r.URL.Query()["revision_id"]
	if len(values) > 1 || (revisionPresent && (len(values) != 1 || strings.TrimSpace(values[0]) == "")) {
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader query", "revision_id must be a non-empty identifier when supplied")
		return
	}
	instance, ok := s.openReaderApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.Reader.Get(r.Context(), service.ReaderGetRequest{
		FragmentID: fragmentID, RevisionID: r.URL.Query().Get("revision_id"), PrincipalID: readerAPIPrincipal,
	})
	if err != nil {
		writeReaderError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) openReaderApp(w http.ResponseWriter, r *http.Request) (*app.App, bool) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader service unavailable", "")
		return nil, false
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader service unavailable", "")
		return nil, false
	}
	if instance.Reader == nil {
		_ = instance.Close()
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader service unavailable", "")
		return nil, false
	}
	return instance, true
}

func readerFragmentID(escapedPath string) (string, error) {
	const prefix = "/v1/reader/items/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", errors.New("not a Reader item path")
	}
	raw := strings.TrimPrefix(escapedPath, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", errors.New("Reader item path must contain one fragment ID")
	}
	fragmentID, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(fragmentID) == "" || strings.Contains(fragmentID, "/") {
		return "", errors.New("Reader fragment ID is invalid")
	}
	return fragmentID, nil
}

func writeReaderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Reader item not found", "")
	case service.IsInvalidReaderRequest(err):
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Reader request rejected", err.Error())
	default:
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader service failed", "")
	}
}
