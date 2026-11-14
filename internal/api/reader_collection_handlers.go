package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

// maxReaderCollectionBytes fits a full reorder of the largest collection.
const maxReaderCollectionBytes int64 = 2 << 20

// registerReaderCollectionRoutes mounts the collection routes described in
// docs/reader-collections.md. Collections belong to the Reader principal.
func (s *Server) registerReaderCollectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/reader/collections", s.handleReaderCollectionList)
	mux.HandleFunc("POST /v1/reader/collections", s.handleReaderCollectionCreate)
	mux.HandleFunc("GET /v1/reader/collections/{collectionId}", s.handleReaderCollectionGet)
	mux.HandleFunc("PATCH /v1/reader/collections/{collectionId}", s.handleReaderCollectionUpdate)
	mux.HandleFunc("DELETE /v1/reader/collections/{collectionId}", s.handleReaderCollectionDelete)
	mux.HandleFunc("GET /v1/reader/collections/{collectionId}/items", s.handleReaderCollectionItems)
	mux.HandleFunc("POST /v1/reader/collections/{collectionId}/items", s.handleReaderCollectionAddItem)
	mux.HandleFunc("DELETE /v1/reader/collections/{collectionId}/items/{fragmentId}", s.handleReaderCollectionRemoveItem)
	mux.HandleFunc("PUT /v1/reader/collections/{collectionId}/order", s.handleReaderCollectionOrder)
}

func (s *Server) handleReaderCollectionList(w http.ResponseWriter, r *http.Request) {
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.List(r.Context(), readerAPIPrincipal)
	writeReaderCollectionResult(w, r, http.StatusOK, capturecontract.SchemaReaderCollectionList, result, err)
}

func (s *Server) handleReaderCollectionCreate(w http.ResponseWriter, r *http.Request) {
	raw, ok := readReaderCollectionBody(w, r)
	if !ok {
		return
	}
	request, err := capturecontract.DecodeReaderCollectionCreate(raw)
	if err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.Create(r.Context(), readerAPIPrincipal, request)
	writeReaderCollectionResult(w, r, http.StatusCreated, capturecontract.SchemaReaderCollection, result, err)
}

func (s *Server) handleReaderCollectionGet(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.Get(r.Context(), readerAPIPrincipal, collectionID)
	writeReaderCollectionResult(w, r, http.StatusOK, capturecontract.SchemaReaderCollection, result, err)
}

func (s *Server) handleReaderCollectionUpdate(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	raw, ok := readReaderCollectionBody(w, r)
	if !ok {
		return
	}
	request, err := capturecontract.DecodeReaderCollectionUpdate(raw)
	if err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.Update(r.Context(), readerAPIPrincipal, collectionID, request)
	writeReaderCollectionResult(w, r, http.StatusOK, capturecontract.SchemaReaderCollection, result, err)
}

func (s *Server) handleReaderCollectionDelete(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	expected, ok := readerCollectionExpectedRevision(w, r)
	if !ok {
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	if err := instance.ReaderCollections.Delete(r.Context(), readerAPIPrincipal, collectionID, expected); err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReaderCollectionItems(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	if len(r.URL.Query()["cursor"]) > 1 {
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader query", "cursor may appear at most once")
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.ListItems(r.Context(), readerAPIPrincipal, collectionID, r.URL.Query().Get("cursor"))
	if err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	// The service has already validated the page against its schema.
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReaderCollectionAddItem(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	raw, ok := readReaderCollectionBody(w, r)
	if !ok {
		return
	}
	request, err := capturecontract.DecodeReaderCollectionAddItem(raw)
	if err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.AddItem(r.Context(), readerAPIPrincipal, collectionID, request)
	writeReaderCollectionResult(w, r, http.StatusOK, capturecontract.SchemaReaderCollection, result, err)
}

func (s *Server) handleReaderCollectionRemoveItem(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	fragmentID, ok := readerCollectionPathID(w, r, "fragmentId")
	if !ok {
		return
	}
	expected, ok := readerCollectionExpectedRevision(w, r)
	if !ok {
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.RemoveItem(r.Context(), readerAPIPrincipal, collectionID, fragmentID, expected)
	writeReaderCollectionResult(w, r, http.StatusOK, capturecontract.SchemaReaderCollection, result, err)
}

func (s *Server) handleReaderCollectionOrder(w http.ResponseWriter, r *http.Request) {
	collectionID, ok := readerCollectionPathID(w, r, "collectionId")
	if !ok {
		return
	}
	raw, ok := readReaderCollectionBody(w, r)
	if !ok {
		return
	}
	request, err := capturecontract.DecodeReaderCollectionOrder(raw)
	if err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	instance, ok := s.openReaderCollectionApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	result, err := instance.ReaderCollections.Reorder(r.Context(), readerAPIPrincipal, collectionID, request)
	writeReaderCollectionResult(w, r, http.StatusOK, capturecontract.SchemaReaderCollection, result, err)
}

func (s *Server) openReaderCollectionApp(w http.ResponseWriter, r *http.Request) (*app.App, bool) {
	instance, ok := s.openReaderApp(w, r)
	if !ok {
		return nil, false
	}
	if instance.ReaderCollections == nil {
		_ = instance.Close()
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader service unavailable", "")
		return nil, false
	}
	return instance, true
}

func readReaderCollectionBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if !requireCaptureJSON(w, r) {
		return nil, false
	}
	return readBoundedJSON(w, r, maxReaderCollectionBytes)
}

// readerCollectionPathID reads one path identifier. A malformed one cannot
// name anything, so it is a 404 like an unknown one.
func readerCollectionPathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	value := r.PathValue(name)
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 255 ||
		strings.ContainsAny(value, "/\\") || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Reader collection target not found", "")
		return "", false
	}
	return value, true
}

func readerCollectionExpectedRevision(w http.ResponseWriter, r *http.Request) (int, bool) {
	values := r.URL.Query()["expected_revision"]
	if len(values) == 1 {
		if value, err := strconv.Atoi(values[0]); err == nil && value >= 0 {
			return value, true
		}
	}
	writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader query", "expected_revision is required once and must be a non-negative integer")
	return 0, false
}

func writeReaderCollectionResult(w http.ResponseWriter, r *http.Request, status int, schema capturecontract.SchemaName, result any, err error) {
	if err != nil {
		writeReaderCollectionError(w, r, err)
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || capturecontract.ValidateJSON(schema, encoded) != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader collection is invalid", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func writeReaderCollectionError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *capturecontract.ValidationError
	var revisionConflict *repository.ReaderRevisionConflictError
	var collectionConflict *repository.ReaderCollectionConflictError
	switch {
	case errors.As(err, &validation), service.IsInvalidReaderRequest(err):
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Reader collection request rejected", err.Error())
	case errors.Is(err, sql.ErrNoRows):
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Reader collection target not found", "")
	case errors.As(err, &revisionConflict), errors.As(err, &collectionConflict):
		writeCaptureProblem(w, r, http.StatusConflict, "conflict", "Reader collection conflict", err.Error())
	default:
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader collection request failed", "")
	}
}
