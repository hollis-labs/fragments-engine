package api

import "github.com/hollis-labs/libs/security/auth"

// Every protected operation is deliberately mapped. No default or prefix
// fallback: a new endpoint must choose its authority before it becomes usable.
func apiScopes() *auth.ScopeTable {
	t := auth.NewScopeTable()
	t.Route("POST", "/v1/captures", "fragments-engine.capture")
	t.Route("GET", "/v1/captures/{id}", "fragments-engine.capture")
	t.Route("POST", "/v1/captures/{id}/complete", "fragments-engine.capture")
	t.Route("PUT", "/v1/captures/{id}/assets/{variant}/content", "fragments-engine.capture")
	for _, path := range []string{
		"/v1/capabilities", "/v1/search", "/v1/fragments", "/v1/fragments/browse", "/v1/fragments/get", "/v1/fragments/related", "/v1/fragments/attachment",
		"/v1/entities", "/v1/entities/fragments", "/v1/recall/status", "/v1/inbox", "/v1/inbox/entities", "/v1/inbox/entity-items",
		"/v1/reader/items", "/v1/reader/items/{id}", "/v1/reader/items/{id}/content", "/v1/media/variants/{id}/content",
	} {
		t.Route("GET", path, "fragments-engine.read")
	}
	for _, path := range []string{
		"/v1/intake", "/v1/fragments/update", "/v1/fragments/materialize-ffs", "/v1/fragments/reanalyze-attachments", "/v1/reader/items/{id}/commands",
	} {
		t.Route("POST", path, "fragments-engine.write")
	}
	for _, path := range []string{
		"/v1/auth/session", "/v1/ingests", "/v1/ingests/get", "/v1/ingests/runs", "/v1/ingests/schedules",
		"/v1/queue/status", "/v1/queue/destinations", "/v1/queue/events", "/v1/queue/pending", "/v1/queue/failed",
		"/v1/destinations", "/v1/destinations/status", "/v1/routes", "/v1/route-log", "/v1/jobs/ingest", "/v1/workers/status", "/v1/config",
		"/v1/transcripts/status", "/v1/transcripts/sessions", "/v1/transcripts/sessions/{id}", "/v1/transcripts/sessions/{id}/find", "/v1/transcripts/sessions/{id}/errors", "/v1/transcripts/facets", "/v1/transcripts/projects",
	} {
		t.Route("GET", path, "fragments-engine.admin")
	}
	for _, path := range []string{
		"/v1/auth/session", "/v1/ingests/run", "/v1/ingests/validate", "/v1/ingests/preview", "/v1/ingests/archive-policy", "/v1/ingests/create", "/v1/ingests/update", "/v1/ingests/delete", "/v1/ingests/set-enabled", "/v1/ingests/run-ingest", "/v1/ingests/schedules", "/v1/ingests/schedules/update", "/v1/ingests/schedules/delete",
		"/v1/queue/drain", "/v1/queue/replay", "/v1/queue/purge", "/v1/destinations/create", "/v1/destinations/validate", "/v1/destinations/rename", "/v1/destinations/delete", "/v1/destinations/retry", "/v1/destinations/queue-policy",
		"/v1/routes/create", "/v1/routes/rename", "/v1/routes/preview", "/v1/routes/materialize", "/v1/routes/delete", "/v1/routes/apply-entity", "/v1/config/update",
	} {
		t.Route("POST", path, "fragments-engine.admin")
	}
	t.Route("DELETE", "/v1/auth/session", "fragments-engine.admin")
	return t
}
