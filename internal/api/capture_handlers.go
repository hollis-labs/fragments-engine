package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

func (s *Server) handleCaptureManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireCaptureJSON(w, r) {
		return
	}
	raw, ok := readBoundedJSON(w, r, maxCaptureManifestBytes)
	if !ok {
		return
	}
	instance, ok := s.openCaptureApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	response, err := instance.Captures.AcceptManifest(r.Context(), raw)
	if err != nil {
		writeCaptureError(w, r, err)
		return
	}
	status := http.StatusCreated
	if response.IdempotentReplay {
		status = http.StatusOK
	}
	writeJSON(w, status, response)
}

func (s *Server) handleCaptureResource(w http.ResponseWriter, r *http.Request) {
	parts, err := capturePathParts(r.URL.EscapedPath())
	if err != nil || len(parts) == 0 {
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Capture resource not found", "")
		return
	}
	instance, ok := s.openCaptureApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	captureID := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		status, err := instance.Captures.GetStatus(r.Context(), captureID)
		if err != nil {
			writeCaptureError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case len(parts) == 2 && parts[1] == "complete" && r.Method == http.MethodPost:
		if !requireCaptureJSON(w, r) {
			return
		}
		raw, ok := readBoundedJSON(w, r, maxCaptureCompletionBytes)
		if !ok {
			return
		}
		status, err := instance.Captures.Complete(r.Context(), captureID, raw)
		if err != nil {
			writeCaptureError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, status)
	case len(parts) == 4 && parts[1] == "assets" && parts[3] == "content" && r.Method == http.MethodPut:
		s.handleCaptureAssetUpload(w, r, instance, captureID, parts[2])
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCaptureAssetUpload(w http.ResponseWriter, r *http.Request, instance *app.App, captureID, clientVariantID string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !allowedCaptureMediaType(mediaType) {
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid asset content type", "Content-Type must be application/octet-stream, image/*, video/*, audio/*, or text/*")
		return
	}
	digest, err := parseRFC9530SHA256(r.Header.Get("Digest"))
	if err != nil {
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Digest header", err.Error())
		return
	}
	if r.ContentLength > maxCaptureAssetBytes {
		writeCaptureProblem(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "Asset payload too large", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxCaptureAssetBytes)
	if _, err := instance.Captures.StoreAssetContent(r.Context(), captureID, clientVariantID, digest, r.Body); err != nil {
		writeCaptureError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) openCaptureApp(w http.ResponseWriter, r *http.Request) (*app.App, bool) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Capture service unavailable", err.Error())
		return nil, false
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Capture service unavailable", err.Error())
		return nil, false
	}
	return instance, true
}

func readBoundedJSON(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	if r.ContentLength > limit {
		writeCaptureProblem(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "JSON payload too large", "")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeCaptureProblem(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "JSON payload too large", "")
		} else {
			writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid JSON payload", err.Error())
		}
		return nil, false
	}
	return raw, true
}

func requireCaptureJSON(w http.ResponseWriter, r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid JSON content type", "Content-Type must be application/json")
		return false
	}
	return true
}

func capturePathParts(escapedPath string) ([]string, error) {
	const prefix = "/v1/captures/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return nil, errors.New("not a capture path")
	}
	raw := strings.TrimPrefix(escapedPath, prefix)
	if raw == "" {
		return nil, errors.New("missing capture ID")
	}
	segments := strings.Split(raw, "/")
	parts := make([]string, len(segments))
	for i, segment := range segments {
		value, err := url.PathUnescape(segment)
		if err != nil || strings.TrimSpace(value) == "" || strings.Contains(value, "/") {
			return nil, errors.New("invalid capture path segment")
		}
		parts[i] = value
	}
	return parts, nil
}

func allowedCaptureMediaType(value string) bool {
	if value == "application/octet-stream" {
		return true
	}
	for _, prefix := range []string{"image/", "video/", "audio/", "text/"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func parseRFC9530SHA256(value string) (domain.ContentDigest, error) {
	const prefix = "sha-256=:"
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, ":") {
		return domain.ContentDigest{}, errors.New("Digest must use sha-256=:base64:")
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(value, prefix), ":")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != 32 {
		return domain.ContentDigest{}, errors.New("Digest must contain one 32-byte SHA-256 value")
	}
	return domain.ContentDigest{Algorithm: "sha256", Value: hex.EncodeToString(decoded)}, nil
}

func writeCaptureError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *capturecontract.ValidationError
	var captureConflict *repository.CaptureConflictError
	var mediaConflict *repository.MediaConflictError
	var completionConflict *repository.CaptureCompletionReportConflictError
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeCaptureProblem(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "Payload too large", "")
	case service.IsDigestMismatch(err):
		writeCaptureProblem(w, r, http.StatusConflict, "digest_mismatch", "Asset digest mismatch", err.Error())
	case errors.As(err, &validation):
		code := "validation_failed"
		if strings.Contains(err.Error(), "schema_version") {
			code = "unsupported_contract_version"
		}
		writeCaptureProblem(w, r, http.StatusBadRequest, code, "Capture contract validation failed", err.Error())
	case errors.Is(err, sql.ErrNoRows):
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Capture resource not found", "")
	case errors.As(err, &captureConflict), errors.As(err, &mediaConflict), errors.As(err, &completionConflict):
		writeCaptureProblem(w, r, http.StatusConflict, "conflict", "Capture conflict", err.Error())
	case service.IsInvalidCaptureRequest(err):
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Capture request rejected", err.Error())
	default:
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Capture service failed", "")
	}
}

func writeCaptureProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	problem := capturecontract.APIProblem{Type: "about:blank", Title: title,
		Status: status, Detail: detail, Instance: r.URL.Path, Code: code}
	if code == "validation_failed" {
		problem.Errors = []capturecontract.FieldViolation{{Pointer: "", Code: "invalid", Message: firstProblemMessage(detail, title)}}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func firstProblemMessage(detail, title string) string {
	if strings.TrimSpace(detail) != "" {
		return detail
	}
	return title
}
