package api

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

const maxReaderRangeHeaderBytes = 128

func (s *Server) handleReaderArticleContent(w http.ResponseWriter, r *http.Request) {
	fragmentID := r.PathValue("fragmentId")
	revisionID, ok := requiredSingleReaderQuery(w, r, "revision_id")
	if !ok {
		return
	}
	if !validReaderResourceID(fragmentID) || !validReaderResourceID(revisionID) {
		writeReaderProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader resource identity", "fragment and revision identifiers must be safe path values", nil)
		return
	}
	format, err := requestedArticleFormat(r)
	if err != nil {
		status := http.StatusNotAcceptable
		if _, explicit := r.URL.Query()["format"]; explicit {
			status = http.StatusBadRequest
		}
		writeReaderProblem(w, r, status, "validation_failed", "Unsupported article representation", err.Error(), nil)
		return
	}
	instance, ok := s.openReaderResourceApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	resource, err := instance.ReaderResources.Article(r.Context(), fragmentID, revisionID, format)
	if err != nil {
		writeReaderResourceError(w, r, err)
		return
	}
	setReaderResourceHeaders(w)
	w.Header().Set("Content-Type", resource.ContentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Vary", "Accept")
	w.Header().Set("ETag", quotedDigest(resource.Digest.Value))
	w.Header().Set("Content-Digest", rfc9530Digest(resource.Digest.Value))
	w.Header().Set("Repr-Digest", rfc9530Digest(resource.Digest.Value))
	w.Header().Set("X-FE-Fragment-ID", resource.CanonicalFragmentID)
	w.Header().Set("X-FE-Revision-ID", resource.Revision.ID)
	w.Header().Set("X-FE-Source-Content-Digest", resource.Revision.ContentDigest)
	w.Header().Set("X-FE-Normalizer", resource.Revision.Normalizer.Adapter+"/"+resource.Revision.Normalizer.Version)
	// Article bodies are small representations rather than seekable media.
	// Ignore Range so a caller cannot turn format negotiation into a partial
	// document with misleading content semantics.
	r.Header.Del("Range")
	http.ServeContent(w, r, "", resource.Revision.CommittedAt, bytes.NewReader(resource.Body))
}

func (s *Server) handleReaderMediaContent(w http.ResponseWriter, r *http.Request) {
	variantID := r.PathValue("variantId")
	fragmentID, ok := requiredSingleReaderQuery(w, r, "fragment_id")
	if !ok {
		return
	}
	revisionID, ok := requiredSingleReaderQuery(w, r, "revision_id")
	if !ok {
		return
	}
	if !validReaderResourceID(variantID) || !validReaderResourceID(fragmentID) || !validReaderResourceID(revisionID) {
		writeReaderProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader resource identity", "fragment, revision, and variant identifiers must be safe values", nil)
		return
	}
	instance, ok := s.openReaderResourceApp(w, r)
	if !ok {
		return
	}
	defer instance.Close()
	resource, err := instance.ReaderResources.OpenMedia(r.Context(), fragmentID, revisionID, variantID)
	if err != nil {
		writeReaderResourceError(w, r, err)
		return
	}
	defer resource.Close()
	if rawRange := r.Header.Get("Range"); rawRange != "" && !validSingleByteRange(rawRange, resource.ByteSize) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", resource.ByteSize))
		writeReaderProblem(w, r, http.StatusRequestedRangeNotSatisfiable, "validation_failed", "Invalid media range", "only one bounded byte range is supported", nil)
		return
	}
	setReaderResourceHeaders(w)
	w.Header().Set("Content-Type", resource.ContentType)
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("ETag", quotedDigest(resource.Digest.Value))
	w.Header().Set("Repr-Digest", rfc9530Digest(resource.Digest.Value))
	if r.Header.Get("Range") == "" {
		w.Header().Set("Content-Digest", rfc9530Digest(resource.Digest.Value))
	}
	w.Header().Set("X-FE-Fragment-ID", resource.CanonicalFragmentID)
	w.Header().Set("X-FE-Revision-ID", resource.RevisionID)
	w.Header().Set("X-FE-Asset-Variant-ID", resource.Variant.ID)
	if resource.Legacy {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, "", resource.ModifiedAt, resource.File)
}

func setReaderResourceHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
}

func requiredSingleReaderQuery(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	values, present := r.URL.Query()[name]
	if !present || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		writeReaderProblem(w, r, http.StatusBadRequest, "validation_failed", "Invalid Reader resource request", name+" is required exactly once", nil)
		return "", false
	}
	return strings.TrimSpace(values[0]), true
}

func validReaderResourceID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func requestedArticleFormat(r *http.Request) (service.ArticleFormat, error) {
	values, present := r.URL.Query()["format"]
	if present {
		if len(values) != 1 {
			return "", fmt.Errorf("format must be supplied at most once")
		}
		switch strings.ToLower(strings.TrimSpace(values[0])) {
		case "html":
			return service.ArticleHTML, nil
		case "markdown":
			return service.ArticleMarkdown, nil
		default:
			return "", fmt.Errorf("format must be html or markdown")
		}
	}
	accept := strings.TrimSpace(r.Header.Get("Accept"))
	if accept == "" {
		return service.ArticleHTML, nil
	}
	type choice struct {
		format service.ArticleFormat
		q      float64
		order  int
	}
	best := choice{q: -1}
	for i, part := range strings.Split(accept, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			return "", fmt.Errorf("Accept header is invalid")
		}
		q := 1.0
		if raw := params["q"]; raw != "" {
			q, err = strconv.ParseFloat(raw, 64)
			if err != nil || q < 0 || q > 1 {
				return "", fmt.Errorf("Accept quality must be between zero and one")
			}
		}
		if q == 0 {
			continue
		}
		var format service.ArticleFormat
		switch strings.ToLower(mediaType) {
		case "text/markdown":
			format = service.ArticleMarkdown
		case "text/html", "application/xhtml+xml", "text/*", "*/*":
			format = service.ArticleHTML
		default:
			continue
		}
		if q > best.q || (q == best.q && i < best.order) {
			best = choice{format: format, q: q, order: i}
		}
	}
	if best.q < 0 {
		return "", fmt.Errorf("Accept must allow text/html or text/markdown")
	}
	return best.format, nil
}

func validSingleByteRange(value string, size int64) bool {
	if len(value) == 0 || len(value) > maxReaderRangeHeaderBytes || strings.Contains(value, ",") || !strings.HasPrefix(value, "bytes=") {
		return false
	}
	raw := strings.TrimPrefix(value, "bytes=")
	if strings.TrimSpace(raw) != raw || strings.Count(raw, "-") != 1 {
		return false
	}
	parts := strings.SplitN(raw, "-", 2)
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		return err == nil && suffix > 0 && size > 0
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return false
	}
	if parts[1] == "" {
		return true
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && end >= start
}

func (s *Server) openReaderResourceApp(w http.ResponseWriter, r *http.Request) (*app.App, bool) {
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeReaderProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader resource service unavailable", "", nil)
		return nil, false
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		writeReaderProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader resource service unavailable", "", nil)
		return nil, false
	}
	return instance, true
}

func writeReaderResourceError(w http.ResponseWriter, r *http.Request, err error) {
	var unavailable *service.ReaderResourceUnavailableError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeReaderProblem(w, r, http.StatusNotFound, "not_found", "Reader resource not found", "", nil)
	case errors.As(err, &unavailable):
		detail := "resource state is " + string(unavailable.State) + "; " + unavailable.Reason
		writeReaderProblem(w, r, http.StatusConflict, "capability_unavailable", "Reader resource is not available", detail, &unavailable.Retryable)
	case service.IsReaderResourceIntegrityFailure(err):
		writeReaderProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader resource failed integrity verification", "", nil)
	default:
		writeReaderProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader resource service failed", "", nil)
	}
}

func writeReaderProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string, retryable *bool) {
	problem := capturecontract.APIProblem{Type: "about:blank", Title: title, Status: status, Detail: detail, Instance: r.URL.Path, Code: code, Retryable: retryable}
	if code == "validation_failed" {
		message := detail
		if message == "" {
			message = title
		}
		problem.Errors = []capturecontract.FieldViolation{{Pointer: "", Code: "invalid", Message: message}}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(problem)
	}
}

func quotedDigest(value string) string {
	return `"sha256-` + value + `"`
}

func rfc9530Digest(value string) string {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return ""
	}
	return "sha-256=:" + base64.StdEncoding.EncodeToString(raw) + ":"
}
