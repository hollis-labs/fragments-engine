package api

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/transcript"
)

// These routes are the one deliberate, narrow exception to the rule that the
// API never serves transcript content. They are owner-only, read-only, off by
// default (ui.transcripts.enabled), loopback-only and audited per read. They
// return redacted text and metadata only: no source paths, native ids, raw
// archives or attachment bytes. Agents never get this surface: there is no MCP
// tool for it, and the CLI/API/MCP parity rule is deferred for these routes.

const transcriptsPrefix = "/v1/transcripts"

func (s *Server) registerTranscriptRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+transcriptsPrefix+"/status", s.transcriptsGuard(s.handleTranscriptStatus))
	mux.HandleFunc("GET "+transcriptsPrefix+"/sessions", s.transcriptsGuard(s.handleTranscriptSessions))
	mux.HandleFunc("GET "+transcriptsPrefix+"/sessions/{id}", s.transcriptsGuard(s.handleTranscriptSession))
	mux.HandleFunc("GET "+transcriptsPrefix+"/facets", s.transcriptsGuard(s.handleTranscriptFacets))
	mux.HandleFunc("GET "+transcriptsPrefix+"/projects", s.transcriptsGuard(s.handleTranscriptProjects))
	// Any other method on the prefix must look like an unknown route when the
	// feature is off, and be refused when it is on.
	mux.HandleFunc(transcriptsPrefix+"/", s.transcriptsGuard(func(w http.ResponseWriter, _ *http.Request, _ *service.TranscriptBrowseService) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}))
}

// isLoopbackAddr reports whether a listen address binds only loopback. An empty
// or wildcard host binds every interface and is not loopback.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackHost checks the Host header so a DNS-rebound name cannot reach the
// routes from a browser page on another origin.
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// checkTranscriptListen refuses a non-loopback listen address when the
// transcript routes are enabled. A config that cannot be read is not an error
// here: the server's other handlers will report it.
func checkTranscriptListen(cfgPath, addr string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil || !cfg.UI.Transcripts.Enabled {
		return nil
	}
	if !isLoopbackAddr(addr) {
		return errors.New("ui.transcripts.enabled requires a loopback listen address (for example 127.0.0.1:8091); refusing to serve " + addr)
	}
	return nil
}

func (s *Server) transcriptsGuard(next func(http.ResponseWriter, *http.Request, *service.TranscriptBrowseService)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(s.cfgPath)
		if err != nil || !cfg.UI.Transcripts.Enabled {
			http.NotFound(w, r)
			return
		}
		if !isLoopbackRequest(r) || !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden: endpoint restricted to localhost", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		var store *transcript.Store
		if root := strings.TrimSpace(cfg.Transcripts.PrivateRoot); root != "" {
			store, err = transcript.Open(config.ExpandHome(root))
			if err != nil {
				http.Error(w, "transcript store unavailable", http.StatusServiceUnavailable)
				return
			}
			defer store.Close()
		}
		svc := service.NewTranscriptBrowseService(store)
		svc.SetEmbedder(service.NewLocalQueryEmbedder(cfg.Transcripts.Enrichment.Embedding))
		if r.URL.Path == transcriptsPrefix+"/status" {
			next(w, r, svc)
			return
		}
		if !svc.Available() {
			http.Error(w, "transcript store is not configured", http.StatusServiceUnavailable)
			return
		}
		next(w, r, svc)
	}
}

// semanticProbeTTL is how long a reachability answer is reused.
const semanticProbeTTL = 15 * time.Second

// semanticNotice returns "" when semantic search is usable, else a notice code.
// Stored-vector checks are cheap and always fresh; the embedding server is
// probed at most once per semanticProbeTTL, with a fixed probe text.
func (s *Server) semanticNotice(r *http.Request, svc *service.TranscriptBrowseService) string {
	if !svc.Available() {
		return transcript.NoticeSemanticUnavailable
	}
	if code := svc.SemanticReadiness(r.Context()); code != "" {
		return code
	}
	s.semProbe.mu.Lock()
	defer s.semProbe.mu.Unlock()
	if !s.semProbe.at.IsZero() && time.Since(s.semProbe.at) < semanticProbeTTL {
		return s.semProbe.notice
	}
	s.semProbe.notice = svc.ProbeEmbedder(r.Context())
	s.semProbe.at = time.Now()
	return s.semProbe.notice
}

func (s *Server) handleTranscriptStatus(w http.ResponseWriter, r *http.Request, svc *service.TranscriptBrowseService) {
	notice := s.semanticNotice(r, svc)
	out := map[string]any{
		"enabled":   true,
		"available": svc.Available(),
		"semantic":  notice == "",
		"auth":      s.authMode(),
		"modes":     []string{transcript.ModeText, transcript.ModeSummary, transcript.ModeSemantic, transcript.ModeHybrid},
	}
	if notice != "" {
		out["semantic_notice"] = notice
	}
	writeJSON(w, http.StatusOK, out)
}

func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func transcriptFilter(r *http.Request) transcript.SessionFilter {
	q := r.URL.Query()
	f := transcript.SessionFilter{
		Tool: q.Get("tool"), Project: q.Get("project"), Model: q.Get("model"), Kind: q.Get("kind"),
		Since: parseTime(q.Get("since")), Until: parseTime(q.Get("until")),
		Sort: q.Get("sort"), Desc: q.Get("order") != "asc", Cursor: q.Get("cursor"),
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = n
	}
	switch q.Get("has_error") {
	case "true":
		v := true
		f.HasError = &v
	case "false":
		v := false
		f.HasError = &v
	}
	switch q.Get("has_enrichment") {
	case "true":
		v := true
		f.HasEnrichment = &v
	case "false":
		v := false
		f.HasEnrichment = &v
	}
	f.Tag = q.Get("tag")
	if f.Kind != "main" && f.Kind != "subagent" {
		f.Kind = ""
	}
	return f
}

func (s *Server) handleTranscriptSessions(w http.ResponseWriter, r *http.Request, svc *service.TranscriptBrowseService) {
	f := transcriptFilter(r)
	var page transcript.SessionPage
	var err error
	if text := strings.TrimSpace(r.URL.Query().Get("q")); text != "" {
		mode := r.URL.Query().Get("mode")
		if !transcript.ValidMode(mode) {
			mode = transcript.ModeText
		}
		page, err = svc.SearchMode(r.Context(), mode, text, f)
	} else {
		page, err = svc.List(r.Context(), f)
	}
	if err != nil {
		http.Error(w, "cannot read sessions", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func validSessionID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *Server) handleTranscriptSession(w http.ResponseWriter, r *http.Request, svc *service.TranscriptBrowseService) {
	id := r.PathValue("id")
	if !validSessionID(id) {
		http.NotFound(w, r)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("turn_offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("turn_limit"))
	d, err := svc.Detail(r.Context(), id, offset, limit)
	if errors.Is(err, transcript.ErrSessionNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "cannot read session", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleTranscriptFacets(w http.ResponseWriter, r *http.Request, svc *service.TranscriptBrowseService) {
	f, err := svc.Facets(r.Context())
	if err != nil {
		http.Error(w, "cannot read facets", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (s *Server) handleTranscriptProjects(w http.ResponseWriter, r *http.Request, svc *service.TranscriptBrowseService) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := svc.Projects(r.Context(), strings.TrimSpace(r.URL.Query().Get("q")), limit)
	if err != nil {
		http.Error(w, "cannot read projects", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) authMode() string {
	if s.security != nil {
		return "bearer"
	}
	return "none"
}
