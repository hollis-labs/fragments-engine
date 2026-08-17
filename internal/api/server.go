package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

type Server struct {
	cfgPath string
}

func NewServer(cfgPath string) *Server {
	return &Server{cfgPath: cfgPath}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.Handle(sysopBasePath+"/", newSysopSPAHandler())
	mux.HandleFunc("/v1/ingests", s.handleListIngests)
	mux.HandleFunc("/v1/ingests/get", s.handleGetIngest)
	mux.HandleFunc("/v1/ingests/run", s.handleRunIngests)
	mux.HandleFunc("/v1/ingests/validate", s.handleValidateIngest)
	mux.HandleFunc("/v1/ingests/preview", s.handlePreviewIngest)
	mux.HandleFunc("/v1/ingests/archive-policy", s.handleArchivePolicyIngest)
	mux.HandleFunc("/v1/ingests/create", s.handleCreateIngest)
	mux.HandleFunc("/v1/ingests/update", s.handleUpdateIngest)
	mux.HandleFunc("/v1/ingests/delete", s.handleDeleteIngest)
	mux.HandleFunc("/v1/ingests/set-enabled", s.handleSetEnabledIngest)
	mux.HandleFunc("/v1/ingests/run-ingest", s.handleRunIngest)
	mux.HandleFunc("/v1/ingests/runs", s.handleListIngestRuns)
	mux.HandleFunc("/v1/ingests/schedules", s.handleIngestSchedules)
	mux.HandleFunc("/v1/ingests/schedules/update", s.handleUpdateIngestSchedule)
	mux.HandleFunc("/v1/ingests/schedules/delete", s.handleDeleteIngestSchedule)
	mux.HandleFunc("/v1/search", s.handleSearch)
	mux.HandleFunc("/v1/fragments", s.handleFragmentList)
	mux.HandleFunc("/v1/fragments/browse", s.handleFragmentBrowse)
	mux.HandleFunc("/v1/fragments/get", s.handleFragmentGet)
	mux.HandleFunc("/v1/fragments/update", s.handleFragmentUpdate)
	mux.HandleFunc("/v1/fragments/materialize-ffs", s.handleFragmentMaterializeFFS)
	mux.HandleFunc("/v1/fragments/related", s.handleFragmentRelated)
	mux.HandleFunc("/v1/fragments/attachment", s.handleFragmentAttachment)
	mux.HandleFunc("/v1/fragments/reanalyze-attachments", s.handleFragmentReanalyzeAttachments)
	mux.HandleFunc("/v1/entities", s.handleEntities)
	mux.HandleFunc("/v1/entities/fragments", s.handleEntityFragments)
	mux.HandleFunc("/v1/recall/status", s.handleRecallStatus)
	mux.HandleFunc("/v1/inbox", s.handleInboxList)
	mux.HandleFunc("/v1/inbox/entities", s.handleInboxEntities)
	mux.HandleFunc("/v1/inbox/entity-items", s.handleInboxEntityItems)
	mux.HandleFunc("/v1/queue/status", s.handleQueueStatus)
	mux.HandleFunc("/v1/queue/drain", s.handleQueueDrain)
	mux.HandleFunc("/v1/queue/destinations", s.handleQueueDestinations)
	mux.HandleFunc("/v1/queue/events", s.handleQueueEvents)
	mux.HandleFunc("/v1/queue/pending", s.handleQueuePending)
	mux.HandleFunc("/v1/queue/failed", s.handleQueueFailed)
	mux.HandleFunc("/v1/queue/replay", s.handleQueueReplay)
	mux.HandleFunc("/v1/queue/purge", s.handleQueuePurge)
	mux.HandleFunc("/v1/destinations", s.handleDestinations)
	mux.HandleFunc("/v1/destinations/create", s.handleDestinationCreate)
	mux.HandleFunc("/v1/destinations/status", s.handleDestinationStatus)
	mux.HandleFunc("/v1/destinations/validate", s.handleDestinationValidate)
	mux.HandleFunc("/v1/destinations/rename", s.handleDestinationRename)
	mux.HandleFunc("/v1/destinations/delete", s.handleDestinationDelete)
	mux.HandleFunc("/v1/destinations/retry", s.handleDestinationRetry)
	mux.HandleFunc("/v1/destinations/queue-policy", s.handleDestinationQueuePolicy)
	mux.HandleFunc("/v1/routes", s.handleRoutes)
	mux.HandleFunc("/v1/routes/create", s.handleRouteCreate)
	mux.HandleFunc("/v1/routes/rename", s.handleRouteRename)
	mux.HandleFunc("/v1/routes/preview", s.handleRoutePreview)
	mux.HandleFunc("/v1/routes/materialize", s.handleRouteMaterialize)
	mux.HandleFunc("/v1/routes/delete", s.handleRouteDelete)
	mux.HandleFunc("/v1/routes/apply-entity", s.handleRouteApplyEntity)
	mux.HandleFunc("/v1/route-log", s.handleRouteLog)
	mux.HandleFunc("/v1/intake", s.handleIntake)
	// Admin-grade endpoints — gated to localhost since the API has no auth
	// layer and serve-api binds all interfaces. See backlog CW-20260517-0010.
	mux.HandleFunc("/v1/jobs/ingest", localhostOnly(s.handleJobsIngest))
	mux.HandleFunc("/v1/workers/status", localhostOnly(s.handleWorkersStatus))
	mux.HandleFunc("/v1/config", localhostOnly(s.handleConfigGet))
	mux.HandleFunc("/v1/config/update", localhostOnly(s.handleConfigUpdate))
	return mux
}

type routeApplyEntityRequest struct {
	RouteID string `json:"route_id"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
	Limit   int    `json:"limit"`
}

type routeRenameRequest struct {
	RouteID string `json:"route_id"`
	Name    string `json:"name"`
}

type routePreviewRequest struct {
	RouteID string `json:"route_id"`
	Limit   int    `json:"limit"`
}

type routeMaterializeRequest struct {
	RouteID string `json:"route_id"`
	Limit   int    `json:"limit"`
}

type routeDeleteRequest struct {
	RouteID string `json:"route_id"`
	Force   bool   `json:"force"`
}

type queueDrainRequest struct {
	Limit int `json:"limit"`
}

type queueFailedActionRequest struct {
	ID    int64 `json:"id"`
	Force bool  `json:"force"`
}

type destinationRetryRequest struct {
	DestinationID string `json:"destination_id"`
	MaxAttempts   int    `json:"max_attempts"`
	BackoffMS     int    `json:"backoff_ms"`
}

type destinationQueuePolicyRequest struct {
	DestinationID            string `json:"destination_id"`
	ReplayCooldownSeconds    int    `json:"replay_cooldown_seconds"`
	MaxReplaysPerHour        int    `json:"max_replays_per_hour"`
	AlertPendingThreshold    int    `json:"alert_pending_threshold"`
	AlertDeadLetterThreshold int    `json:"alert_dead_letter_threshold"`
}

type destinationValidateRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ConfigJSON string `json:"config_json"`
}

type destinationCreateRequest struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ConfigJSON string `json:"config_json"`
}

type routeCreateRequest struct {
	Name             string  `json:"name"`
	MatchSource      string  `json:"match_source"`
	MatchType        string  `json:"match_type"`
	MatchEntityKind  string  `json:"match_entity_kind"`
	MatchEntityValue string  `json:"match_entity_value"`
	DestinationID    string  `json:"destination_id"`
	AutoRoute        bool    `json:"auto_route"`
	ConfidenceMin    float64 `json:"confidence_min"`
}

type destinationRenameRequest struct {
	DestinationID string `json:"destination_id"`
	Name          string `json:"name"`
}

type destinationDeleteRequest struct {
	DestinationID string `json:"destination_id"`
	Force         bool   `json:"force"`
}

type ingestByNameRequest struct {
	Name  string `json:"name"`
	Limit int    `json:"limit"`
}

type ingestArchivePolicyRequest struct {
	Name               string `json:"name"`
	ArchiveRoot        string `json:"archive_root"`
	CopyTextExports    bool   `json:"copy_text_exports"`
	DeleteCopiedSource bool   `json:"delete_copied_source"`
}

type ingestWriteRequest struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Enabled    bool              `json:"enabled"`
	SourceRoot string            `json:"source_root"`
	Namespace  string            `json:"namespace"`
	Rules      map[string]any    `json:"rules"`
	Labels     map[string]string `json:"labels"`
}

func (req ingestWriteRequest) toInput() service.IngestSourceInput {
	return service.IngestSourceInput{
		Name:       req.Name,
		Kind:       req.Kind,
		Enabled:    req.Enabled,
		SourceRoot: req.SourceRoot,
		Namespace:  req.Namespace,
		Rules:      req.Rules,
		Labels:     req.Labels,
	}
}

type ingestDeleteRequest struct {
	Name string `json:"name"`
}

type ingestSetEnabledRequest struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type ingestScheduleRequest struct {
	ID         string `json:"id"`
	IngestName string `json:"ingest_name"`
	CronExpr   string `json:"cron_expr"`
	Enabled    bool   `json:"enabled"`
}

func (req ingestScheduleRequest) toInput() service.IngestScheduleInput {
	return service.IngestScheduleInput{
		IngestName: req.IngestName,
		CronExpr:   req.CronExpr,
		Enabled:    req.Enabled,
	}
}

type fragmentReanalyzeRequest struct {
	FragmentID   string `json:"fragment_id"`
	AttachmentID string `json:"attachment_id"`
}

type fragmentUpdateRequest struct {
	FragmentID string   `json:"fragment_id"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Notes      string   `json:"notes"`
	Tags       []string `json:"tags"`
	SourceType string   `json:"source_type"`
}

type fragmentMaterializeFFSRequest struct {
	FragmentID string   `json:"fragment_id"`
	Title      string   `json:"title"`
	Summary    string   `json:"summary"`
	Notes      string   `json:"notes"`
	Tags       []string `json:"tags"`
	SourceType string   `json:"source_type"`
}

func (s *Server) ListenAndServe(addr string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.RunQueueDrainer(ctx, s.cfgPath)
	go app.RunIngestWorker(ctx, s.cfgPath)
	go app.RunIngestScheduler(ctx, s.cfgPath)
	go app.RunInboxReviewer(ctx, s.cfgPath)

	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRunIngests enqueues an async run for every enabled ingest and returns
// the created run ids immediately.
func (s *Server) handleRunIngests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()

	runs, err := instance.Fragments.EnqueueAllIngests(r.Context(), instance.IngestQueue, cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleRunIngest enqueues an async run for a single named ingest source.
func (s *Server) handleRunIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestByNameRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ingestCfg, _, err := config.FindIngest(cfg, input.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()

	runID, err := instance.Fragments.EnqueueIngestRun(r.Context(), instance.IngestQueue, ingestCfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "ingest_name": ingestCfg.Name})
}

// handleListIngestRuns returns recent ingest runs (newest first) for progress
// display in the Sysop Ingest page.
func (s *Server) handleListIngestRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()

	runs, err := instance.Fragments.ListIngestRuns(r.Context(), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) handleListIngests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := service.NewIngestAdminService(s.cfgPath).List(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingests": items})
}

// handleGetIngest returns the complete config record for a single ingest
// source — including the raw rules map — so the Sysop edit UI can round-trip
// rules without data loss. Unknown name → 400.
func (s *Server) handleGetIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	result, err := service.NewIngestAdminService(s.cfgPath).Get(r.Context(), name)
	if err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingest": result})
}

func (s *Server) handleValidateIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestByNameRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	result, err := service.NewIngestAdminService(s.cfgPath).Validate(r.Context(), input.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"validation": result})
}

func (s *Server) handlePreviewIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestByNameRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if input.Limit <= 0 {
		input.Limit = 10
	}
	result, err := service.NewIngestAdminService(s.cfgPath).Preview(r.Context(), input.Name, input.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview": result})
}

func (s *Server) handleArchivePolicyIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestArchivePolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	result, err := service.NewIngestAdminService(s.cfgPath).UpdateArchivePolicy(r.Context(), input.Name, input.ArchiveRoot, input.CopyTextExports, input.DeleteCopiedSource)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archive_policy": result})
}

func (s *Server) handleCreateIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	result, err := service.NewIngestAdminService(s.cfgPath).Create(r.Context(), input.toInput())
	if err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingest": result})
}

func (s *Server) handleUpdateIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	result, err := service.NewIngestAdminService(s.cfgPath).Update(r.Context(), input.toInput())
	if err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingest": result})
}

func (s *Server) handleDeleteIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := service.NewIngestAdminService(s.cfgPath).Delete(r.Context(), input.Name); err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": input.Name})
}

func (s *Server) handleSetEnabledIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestSetEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	result, err := service.NewIngestAdminService(s.cfgPath).SetEnabled(r.Context(), input.Name, input.Enabled)
	if err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingest": result})
}

// handleIngestSchedules lists ingest cron schedules (GET) or creates one (POST).
func (s *Server) handleIngestSchedules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		instance, err := app.Open(r.Context(), cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer instance.Close()
		items, err := instance.IngestSchedules.List(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schedules": items})
	case http.MethodPost:
		var input ingestScheduleRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
			http.Error(w, "invalid json body", http.StatusBadRequest)
			return
		}
		cfg, err := config.Load(s.cfgPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		instance, err := app.Open(r.Context(), cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer instance.Close()
		result, err := instance.IngestSchedules.Create(r.Context(), cfg, input.toInput())
		if err != nil {
			writeIngestAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schedule": result})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUpdateIngestSchedule updates an existing ingest cron schedule.
func (s *Server) handleUpdateIngestSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	result, err := instance.IngestSchedules.Update(r.Context(), cfg, input.ID, input.toInput())
	if err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule": result})
}

// handleDeleteIngestSchedule removes an ingest cron schedule.
func (s *Server) handleDeleteIngestSchedule(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input ingestScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	if err := instance.IngestSchedules.Delete(r.Context(), input.ID); err != nil {
		writeIngestAdminError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": input.ID})
}

// writeIngestAdminError maps a ValidationError to HTTP 400 and anything else to 500.
func writeIngestAdminError(w http.ResponseWriter, err error) {
	var verr service.ValidationError
	if errors.As(err, &verr) {
		http.Error(w, verr.Error(), http.StatusBadRequest)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	query := q.Get("q")
	entityKind := q.Get("entity_kind")
	entityValue := q.Get("entity_value")
	status := q.Get("status")
	if query == "" && (entityKind == "" || entityValue == "") {
		http.Error(w, "missing q or entity filter", http.StatusBadRequest)
		return
	}
	mode, ok := recall.ParseSearchMode(q.Get("mode"))
	if !ok {
		http.Error(w, "invalid mode: must be auto, semantic, or keyword", http.StatusBadRequest)
		return
	}
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		if n > service.MaxSearchLimit {
			http.Error(w, fmt.Sprintf("limit too large: max is %d", service.MaxSearchLimit), http.StatusBadRequest)
			return
		}
		limit = n
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	results, modeUsed, err := instance.Fragments.SearchFilteredMode(r.Context(), query, entityKind, entityValue, status, mode, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("search: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "mode_used": string(modeUsed)})
}

func (s *Server) handleInboxList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Inbox.ListDetailed(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRecallStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	writeJSON(w, http.StatusOK, map[string]any{"status": instance.RecallStatus()})
}

func (s *Server) handleQueueStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	stats, err := instance.Queue.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

func (s *Server) handleQueueDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input queueDrainRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Limit <= 0 {
		input.Limit = 100
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	processed, err := instance.Queue.Drain(r.Context(), input.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := instance.Queue.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"processed": processed, "stats": stats})
}

func (s *Server) handleQueueDestinations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Queue.ListDestinationSummaries(r.Context(), 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleQueueEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Queue.ListEvents(r.Context(), r.URL.Query().Get("destination_id"), 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []domain.QueueJobEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleQueuePending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Queue.ListPending(r.Context(), r.URL.Query().Get("destination_id"), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []domain.PendingDeliveryJob{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleQueueFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Queue.ListFailed(r.Context(), r.URL.Query().Get("destination_id"), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []domain.FailedDeliveryJob{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleQueueReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input queueFailedActionRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.ID <= 0 {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	if err := instance.Queue.ReplayFailed(r.Context(), input.ID, input.Force); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := instance.Queue.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replayed": input.ID, "stats": stats})
}

func (s *Server) handleQueuePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input queueFailedActionRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.ID <= 0 {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	if err := instance.Queue.PurgeFailed(r.Context(), input.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := instance.Queue.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"purged": input.ID, "stats": stats})
}

func (s *Server) handleInboxEntities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Inbox.ListEntityGroups(r.Context(), r.URL.Query().Get("kind"), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleInboxEntityItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind := r.URL.Query().Get("kind")
	value := r.URL.Query().Get("value")
	if kind == "" || value == "" {
		http.Error(w, "missing kind or value", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Inbox.ListByEntityDetailed(r.Context(), kind, value, 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleFragmentList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid offset", http.StatusBadRequest)
			return
		}
		offset = n
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, total, err := instance.Fragments.List(r.Context(), status, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []domain.Fragment{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleFragmentBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "invalid offset", http.StatusBadRequest)
			return
		}
		offset = n
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, total, err := instance.Fragments.ListBrowse(r.Context(), status, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []domain.FragmentBrowseItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleFragmentGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fragmentID := r.URL.Query().Get("fragment_id")
	if fragmentID == "" {
		http.Error(w, "missing fragment_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	detail, err := instance.Fragments.GetDetail(r.Context(), fragmentID, 10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": detail})
}

func (s *Server) handleFragmentUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input fragmentUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.FragmentID) == "" {
		http.Error(w, "fragment_id is required", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	detail, err := instance.Fragments.UpdateManualFragment(r.Context(), service.UpdateFragmentRequest{
		FragmentID: input.FragmentID,
		Title:      input.Title,
		Summary:    input.Summary,
		Notes:      input.Notes,
		Tags:       input.Tags,
		SourceType: input.SourceType,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": detail})
}

func (s *Server) handleFragmentMaterializeFFS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input fragmentMaterializeFFSRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	detail, err := instance.Fragments.UpdateManualFragment(r.Context(), service.UpdateFragmentRequest{
		FragmentID: input.FragmentID,
		Title:      input.Title,
		Summary:    input.Summary,
		Notes:      input.Notes,
		Tags:       input.Tags,
		SourceType: input.SourceType,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	destinationName, err := defaultFFSDestinationName(detail.Fragment.SourceType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := instance.Routing.MaterializeFragmentToDestination(r.Context(), detail.Fragment.ID, destinationName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	refreshed, err := instance.Fragments.GetDetail(r.Context(), detail.Fragment.ID, 10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"detail": refreshed, "result": result})
}

func (s *Server) handleFragmentRelated(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fragmentID := r.URL.Query().Get("fragment_id")
	if fragmentID == "" {
		http.Error(w, "missing fragment_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	results, err := instance.Fragments.Related(r.Context(), fragmentID, 10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleFragmentAttachment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fragmentID := r.URL.Query().Get("fragment_id")
	if fragmentID == "" {
		http.Error(w, "missing fragment_id", http.StatusBadRequest)
		return
	}
	attachmentID := r.URL.Query().Get("attachment_id")
	if attachmentID == "" {
		http.Error(w, "missing attachment_id", http.StatusBadRequest)
		return
	}
	variant := r.URL.Query().Get("variant")
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	detail, err := instance.Fragments.GetDetail(r.Context(), fragmentID, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	attachment, ok := findFragmentAttachment(detail.Attachments, attachmentID)
	if !ok {
		http.Error(w, "attachment not found", http.StatusNotFound)
		return
	}
	path := attachmentMediaPath(attachment, variant)
	if path == "" {
		if attachment.ExternalURL != "" {
			http.Redirect(w, r, attachment.ExternalURL, http.StatusTemporaryRedirect)
			return
		}
		http.Error(w, "attachment has no renderable media", http.StatusNotFound)
		return
	}
	_, err = os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "attachment file not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if attachment.MIMEType != "" {
		w.Header().Set("Content-Type", attachment.MIMEType)
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, path)
}

func findFragmentAttachment(items []domain.FragmentAttachment, attachmentID string) (domain.FragmentAttachment, bool) {
	for _, item := range items {
		if item.ID == attachmentID {
			return item, true
		}
	}
	return domain.FragmentAttachment{}, false
}

func attachmentMediaPath(item domain.FragmentAttachment, variant string) string {
	switch variant {
	case "preview":
		if item.PreviewStoragePath != "" {
			return item.PreviewStoragePath
		}
		if item.StoragePath != "" {
			return item.StoragePath
		}
		return item.SourcePath
	case "", "original":
		if item.StoragePath != "" {
			return item.StoragePath
		}
		return item.SourcePath
	default:
		return ""
	}
}

func (s *Server) handleFragmentReanalyzeAttachments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input fragmentReanalyzeRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.FragmentID == "" {
		http.Error(w, "fragment_id is required", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	result, err := instance.Fragments.ReanalyzeAttachments(r.Context(), input.FragmentID, input.AttachmentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handleRouteApplyEntity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req routeApplyEntityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if req.RouteID == "" || req.Kind == "" || req.Value == "" {
		http.Error(w, "missing route_id, kind, or value", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	result, err := instance.Routing.ApplyRouteByEntity(r.Context(), req.RouteID, req.Kind, req.Value, req.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

func (s *Server) handleEntities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Fragments.ListEntities(r.Context(), r.URL.Query().Get("kind"), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleEntityFragments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind := r.URL.Query().Get("kind")
	value := r.URL.Query().Get("value")
	if kind == "" || value == "" {
		http.Error(w, "missing kind or value", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	results, err := instance.Fragments.FragmentsByEntity(r.Context(), kind, value, 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) handleDestinations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Routing.ListDestinations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDestinationCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input destinationCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" || input.Kind == "" {
		http.Error(w, "missing name or kind", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	// AddDestination normalizes + validates the config internally via
	// normalizeDestination, so no separate ValidateDestination call is needed.
	item, err := instance.Routing.AddDestination(r.Context(), domain.Destination{
		Name:       input.Name,
		Kind:       input.Kind,
		ConfigJSON: input.ConfigJSON,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleDestinationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	destinationID := r.URL.Query().Get("destination_id")
	if destinationID != "" {
		item, err := instance.Routing.GetDestinationStatus(r.Context(), destinationID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"item": item})
		return
	}
	items, err := instance.Routing.ListDestinationStatus(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleDestinationValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input destinationValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Kind == "" {
		http.Error(w, "missing kind", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.ValidateDestination(r.Context(), domain.Destination{
		Name:       input.Name,
		Kind:       input.Kind,
		ConfigJSON: input.ConfigJSON,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleDestinationRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input destinationRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.DestinationID == "" || input.Name == "" {
		http.Error(w, "missing destination_id or name", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.RenameDestination(r.Context(), input.DestinationID, input.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleDestinationDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input destinationDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.DestinationID == "" {
		http.Error(w, "missing destination_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.DeleteDestination(r.Context(), input.DestinationID, input.Force)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleDestinationRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input destinationRetryRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.DestinationID == "" {
		http.Error(w, "missing destination_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.UpdateDestinationRetry(r.Context(), input.DestinationID, domain.DeliveryRetryConfig{
		MaxAttempts: input.MaxAttempts,
		BackoffMS:   input.BackoffMS,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleDestinationQueuePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input destinationQueuePolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.DestinationID == "" {
		http.Error(w, "missing destination_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.UpdateDestinationQueuePolicy(r.Context(), input.DestinationID, domain.QueuePolicyConfig{
		ReplayCooldownSeconds:    input.ReplayCooldownSeconds,
		MaxReplaysPerHour:        input.MaxReplaysPerHour,
		AlertPendingThreshold:    input.AlertPendingThreshold,
		AlertDeadLetterThreshold: input.AlertDeadLetterThreshold,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Routing.ListRoutes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleRouteCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input routeCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Name == "" || input.DestinationID == "" {
		http.Error(w, "missing name or destination_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.AddRoute(r.Context(), domain.Route{
		Name:             input.Name,
		MatchSource:      input.MatchSource,
		MatchType:        input.MatchType,
		MatchEntityKind:  input.MatchEntityKind,
		MatchEntityValue: input.MatchEntityValue,
		DestinationID:    input.DestinationID,
		AutoRoute:        input.AutoRoute,
		ConfidenceMin:    input.ConfidenceMin,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleRouteRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input routeRenameRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.RouteID == "" || input.Name == "" {
		http.Error(w, "missing route_id or name", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.RenameRoute(r.Context(), input.RouteID, input.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleRoutePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input routePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.RouteID == "" {
		http.Error(w, "missing route_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.PreviewRoute(r.Context(), input.RouteID, input.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleRouteMaterialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input routeMaterializeRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.RouteID == "" {
		http.Error(w, "missing route_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.MaterializeRoute(r.Context(), input.RouteID, input.Limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleRouteDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input routeDeleteRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.RouteID == "" {
		http.Error(w, "missing route_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	item, err := instance.Routing.DeleteRoute(r.Context(), input.RouteID, input.Force)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) handleRouteLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	fragmentID := r.URL.Query().Get("fragment_id")
	if fragmentID == "" {
		http.Error(w, "missing fragment_id", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	items, err := instance.Routing.ListRouteLog(r.Context(), fragmentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type intakeRequest struct {
	Content    string   `json:"content"`
	Title      string   `json:"title"`
	SourceType string   `json:"source_type"`
	Tags       []string `json:"tags"`

	// SourceURL, Description, and Selection support intake of content the
	// caller already fetched/extracted client-side (the web clipper browser
	// extension being the first such caller, see EP-20260816-0005). When
	// SourceURL is set, the server treats Content as final and complete and
	// never re-fetches it -- see service.ManualIntakeEnricher.EnrichIntake's
	// PrefetchedContent handling.
	SourceURL   string `json:"source_url"`
	Description string `json:"description"`
	Selection   string `json:"selection"`
}

func (s *Server) handleIntake(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input intakeRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	if input.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	instance, err := app.Open(r.Context(), cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer instance.Close()
	result, err := instance.Fragments.Intake(r.Context(), service.IntakeRequest{
		Content:     input.Content,
		Title:       input.Title,
		SourceType:  input.SourceType,
		Tags:        input.Tags,
		SourceURL:   input.SourceURL,
		Description: input.Description,
		Selection:   input.Selection,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"result": result})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func defaultFFSDestinationName(sourceType string) (string, error) {
	switch strings.TrimSpace(sourceType) {
	case "pin":
		return "ffs-pins", nil
	case "note":
		return "ffs-notes", nil
	case "quote":
		return "ffs-quotes", nil
	case "report":
		return "ffs-reports", nil
	case "repo", "url", "article", "reference":
		return "ffs-references", nil
	default:
		return "", fmt.Errorf("no default ffs destination for source_type %q", sourceType)
	}
}

// localhostOnly wraps a handler so it only serves requests originating from the
// loopback interface. The API has no auth layer and serve-api binds all
// interfaces, so admin-grade endpoints are gated to localhost as a stopgap.
func localhostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRequest(r) {
			http.Error(w, "forbidden: endpoint restricted to localhost", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// isLoopbackRequest reports whether the request's direct peer is a loopback
// address. It deliberately ignores X-Forwarded-For so a remote client cannot
// spoof a loopback origin through a forwarded header.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
