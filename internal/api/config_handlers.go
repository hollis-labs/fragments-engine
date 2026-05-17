package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/hollis-labs/fragments-engine/internal/config"
)

// configUpdateRequest is the POST /v1/config/update body.
type configUpdateRequest struct {
	Config config.Config `json:"config"`
}

// handleConfigGet returns the full parsed config and its on-disk path.
//
// Redaction: the config schema stores only an env-var NAME for API keys
// (analysis.attachments.openai.api_key_env) and credential env-var names
// elsewhere — never resolved secret values. Env-var names are safe to emit,
// so the parsed Config carries no secret material and is returned verbatim.
// redactConfig is the explicit enforcement point if that ever changes.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load(s.cfgPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config": redactConfig(cfg),
		"path":   s.cfgPath,
	})
}

// handleConfigUpdate validates and persists a replacement config.
func (s *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var input configUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil && err != io.EOF {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	cfg := input.Config
	// config.Validate normalizes defaults and rejects bad input; surface its
	// error as HTTP 400 so the caller sees the specific validation failure.
	if err := cfg.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := config.Save(s.cfgPath, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"restart_required": true,
	})
}

// redactConfig returns a copy of cfg safe to emit over the API. The config
// schema holds only env-var names for credentials, so no value is stripped
// today; this is the single place to add stripping if a resolved-secret field
// is ever introduced.
func redactConfig(cfg config.Config) config.Config {
	return cfg
}
