package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

const maxReaderCommandBytes int64 = 1 << 20

type readerCommandExecutor interface {
	Execute(context.Context, string, string, capturecontract.ReaderCommand) (service.ReaderCommandExecution, error)
}

// handleReaderCommand is intentionally not registered in Server.Handler until
// the parallel Reader projection task supplies the production ReaderProjector.
// The transport implementation is complete and exercised through the injected
// handler seam below.
func (s *Server) handleReaderCommand(w http.ResponseWriter, r *http.Request) {
	fragmentID, err := readerCommandPathFragment(r.URL.EscapedPath())
	if err != nil {
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Reader item not found", "")
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader command service unavailable", "")
		return
	}
	instance, err := openReaderCommandApp(r.Context(), cfg)
	if err != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader command service unavailable", "")
		return
	}
	defer instance.close()
	executeReaderCommandHTTP(w, r, fragmentID, instance.executor)
}

// readerCommandApp is a small local abstraction that keeps the testable HTTP
// edge independent of app.App's concrete lifecycle.
type readerCommandApp struct {
	executor readerCommandExecutor
	close    func() error
}

var openReaderCommandApp = func(ctx context.Context, cfg config.Config) (readerCommandApp, error) {
	instance, err := app.Open(ctx, cfg)
	if err != nil {
		return readerCommandApp{}, err
	}
	return readerCommandApp{executor: instance.ReaderCommands, close: instance.Close}, nil
}

func executeReaderCommandHTTP(w http.ResponseWriter, r *http.Request, fragmentID string, executor readerCommandExecutor) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireCaptureJSON(w, r) {
		return
	}
	raw, ok := readBoundedJSON(w, r, maxReaderCommandBytes)
	if !ok {
		return
	}
	command, err := capturecontract.DecodeReaderCommand(raw)
	if err != nil {
		writeReaderCommandError(w, r, err)
		return
	}
	if executor == nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader command service unavailable", "")
		return
	}
	result, err := executor.Execute(r.Context(), service.LocalReaderPrincipal, fragmentID, command)
	if err != nil {
		writeReaderCommandError(w, r, err)
		return
	}
	encoded, err := json.Marshal(result.Item)
	if err != nil || capturecontract.ValidateJSON(capturecontract.SchemaReaderItem, encoded) != nil {
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader projection is invalid", "")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func writeReaderCommandError(w http.ResponseWriter, r *http.Request, err error) {
	var validation *capturecontract.ValidationError
	var commandConflict *repository.ReaderCommandConflictError
	var revisionConflict *repository.ReaderRevisionConflictError
	var noteConflict *repository.CuratedNoteConflictError
	var mediaConflict *repository.MediaConflictError
	var positionError *repository.ReaderPositionError
	var executionError *service.ReaderCommandExecutionError
	switch {
	case errors.As(err, &validation), service.IsInvalidReaderCommand(err), errors.As(err, &positionError):
		writeCaptureProblem(w, r, http.StatusBadRequest, "validation_failed", "Reader command validation failed", err.Error())
	case errors.Is(err, sql.ErrNoRows):
		writeCaptureProblem(w, r, http.StatusNotFound, "not_found", "Reader command target not found", "")
	case errors.As(err, &commandConflict), errors.As(err, &revisionConflict), errors.As(err, &noteConflict), errors.As(err, &mediaConflict):
		writeCaptureProblem(w, r, http.StatusConflict, "conflict", "Reader command conflict", err.Error())
	case errors.As(err, &executionError) && executionError.Code == "conflict":
		writeCaptureProblem(w, r, http.StatusConflict, "conflict", "Reader command conflict", err.Error())
	default:
		writeCaptureProblem(w, r, http.StatusInternalServerError, "internal_error", "Reader command failed", "")
	}
}

func readerCommandPathFragment(escapedPath string) (string, error) {
	const prefix = "/v1/reader/items/"
	const suffix = "/commands"
	if !strings.HasPrefix(escapedPath, prefix) || !strings.HasSuffix(escapedPath, suffix) {
		return "", errors.New("not a reader command path")
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(escapedPath, prefix), suffix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", errors.New("invalid reader fragment path")
	}
	fragmentID, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(fragmentID) == "" || utf8.RuneCountInString(fragmentID) > 255 ||
		strings.ContainsAny(fragmentID, "/\\") || strings.IndexFunc(fragmentID, unicode.IsControl) >= 0 {
		return "", errors.New("invalid reader fragment path")
	}
	return fragmentID, nil
}
