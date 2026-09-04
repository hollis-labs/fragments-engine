package api

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/store"
)

func TestIntakeHTTPForwardsAdditiveLegacyCaptureContext(t *testing.T) {
	configPath := writeCaptureAPIConfig(t)
	server := NewServer(configPath).Handler()
	firstBody := []byte(`{
  "content":"prefetched source body",
  "title":"Source title",
  "source_url":"https://example.test/prefetched",
  "tags":["explicit-user-tag"],
  "selection":"selection alias",
  "highlights":["highlight one","highlight two"],
  "notes":["note one"]
}`)
	first := serveCaptureRequest(t, server, http.MethodPost, "/v1/intake", "application/json", firstBody, "")
	if first.Code != http.StatusCreated {
		t.Fatalf("first intake status=%d body=%s", first.Code, first.Body.String())
	}
	var response struct {
		Result struct {
			FragmentID string `json:"fragment_id"`
			Outcome    string `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Result.FragmentID == "" || response.Result.Outcome != "inserted" {
		t.Fatalf("first result=%+v", response.Result)
	}
	replay := serveCaptureRequest(t, server, http.MethodPost, "/v1/intake", "application/json", firstBody, "")
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayResponse struct {
		Result struct {
			FragmentID string `json:"fragment_id"`
			Outcome    string `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResponse); err != nil {
		t.Fatal(err)
	}
	if replayResponse.Result.FragmentID != response.Result.FragmentID || replayResponse.Result.Outcome != "skipped" {
		t.Fatalf("replay result=%+v first=%+v", replayResponse.Result, response.Result)
	}

	additiveBody := []byte(`{
  "content":"prefetched source body",
  "title":"Source title",
  "source_url":"https://example.test/prefetched",
  "tags":["second-user-tag"],
  "highlights":["highlight three"],
  "notes":["note two"]
}`)
	additive := serveCaptureRequest(t, server, http.MethodPost, "/v1/intake", "application/json", additiveBody, "")
	if additive.Code != http.StatusCreated {
		t.Fatalf("additive status=%d body=%s", additive.Code, additive.Body.String())
	}

	st, err := store.Open(filepath.Join(filepath.Dir(configPath), "data", "fragments.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM fragments`:                                         1,
		`SELECT COUNT(*) FROM fragment_revisions`:                                1,
		`SELECT COUNT(*) FROM capture_attempts`:                                  2,
		`SELECT COUNT(*) FROM capture_annotations`:                               6,
		`SELECT COUNT(*) FROM fragment_tag_observations`:                         2,
		`SELECT COUNT(*) FROM fragment_capability_coverage`:                      12,
		`SELECT COUNT(*) FROM enrichment_observations WHERE capability = 'tags'`: 2,
	} {
		var got int
		if err := st.DB.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: got %d want %d", query, got, want)
		}
	}
}
