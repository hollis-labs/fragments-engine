package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestExecuteDestinationWithRetry_APIEventuallySucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	destination := domain.Destination{
		Name: "nanite-api",
		Kind: "api",
		ConfigJSON: fmt.Sprintf(`{
			"base_url": %q,
			"provider": "nanite_messaging",
			"retry": {"max_attempts": 3, "backoff_ms": 1},
			"nanite_messaging": {
				"from_session_id": "sess-fe",
				"from_agent_id": "fragments-engine",
				"to_session_id": "sess-nanite",
				"to_agent_id": "user"
			}
		}`, srv.URL),
	}

	result, err := ExecuteDestinationWithRetry(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute with retry: %v", err)
	}
	if result.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", result.Attempts)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 http calls, got %d", calls.Load())
	}
}

// TestExecuteDestinationWithRetry_CallbackEventuallySucceeds proves callback
// destinations participate in the exact same retry mechanism as the other
// four kinds: retryConfigForDestination decodes CallbackDestinationConfig.
// Retry, and ExecuteDestinationWithRetry (used by the queue drainer's
// processJob, not by the routing hot path) retries on transient failures the
// same way it does for "api".
func TestExecuteDestinationWithRetry_CallbackEventuallySucceeds(t *testing.T) {
	var calls atomic.Int32
	var lastGenerator any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		lastGenerator = body["generator"]
		if n < 3 {
			http.Error(w, "temporary", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	destination := domain.Destination{
		Name: "curator-wake",
		Kind: "callback",
		ConfigJSON: fmt.Sprintf(`{
			"target": %q,
			"generator": "wiki_page",
			"retry": {"max_attempts": 3, "backoff_ms": 1}
		}`, srv.URL),
	}

	result, err := ExecuteDestinationWithRetry(context.Background(), destination, testFragment(), nil)
	if err != nil {
		t.Fatalf("execute callback with retry: %v", err)
	}
	if result.Attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", result.Attempts)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 http calls, got %d", calls.Load())
	}
	if lastGenerator != "wiki_page" {
		t.Fatalf("expected generator forwarded unmodified on final attempt, got %#v", lastGenerator)
	}
}

func TestRetryConfigForDestination_CallbackDecodesAndDefaults(t *testing.T) {
	withExplicitRetry := domain.Destination{
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake","generator":"wiki_page","retry":{"max_attempts":7,"backoff_ms":250}}`,
	}
	got := retryConfigForDestination(withExplicitRetry)
	if got.MaxAttempts != 7 || got.BackoffMS != 250 {
		t.Fatalf("expected decoded callback retry config, got %+v", got)
	}

	withoutRetry := domain.Destination{
		Kind:       "callback",
		ConfigJSON: `{"target":"https://curator.example.com/nanite/wake","generator":"wiki_page"}`,
	}
	got = retryConfigForDestination(withoutRetry)
	if got.MaxAttempts != 3 || got.BackoffMS != 500 {
		t.Fatalf("expected callback retry defaults (3 attempts, 500ms), got %+v", got)
	}
}
