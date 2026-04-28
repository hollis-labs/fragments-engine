package ingest

import (
	"context"
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
