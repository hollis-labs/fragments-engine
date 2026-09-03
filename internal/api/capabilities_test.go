package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
)

func TestCapabilitiesAdvertisesContractsSeparatelyFromOperationReadiness(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	res := httptest.NewRecorder()

	NewServer("unused.yaml").Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", res.Code, http.StatusOK, res.Body.String())
	}
	if got := res.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", got)
	}
	raw := res.Body.Bytes()
	if err := capturecontract.ValidateJSON(capturecontract.SchemaCapabilities, raw); err != nil {
		t.Fatalf("capability response does not satisfy canonical schema: %v\n%s", err, raw)
	}
	var got capturecontract.CapabilityDiscovery
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Contracts) == 0 || got.Contracts[0].PreferredVersion != capturecontract.CaptureVersion {
		t.Fatalf("capture contract was not advertised: %#v", got.Contracts)
	}
	if got.Operations.CaptureManifest || got.Operations.AssetUpload || got.Operations.CaptureCompletion || got.Operations.ReaderQuery || got.Operations.ReaderCommands || got.Operations.ReaderContext || got.Operations.ConversationReference {
		t.Fatalf("later-wave operations must not be advertised ready: %#v", got.Operations)
	}
}

func TestCapabilitiesRejectsNonGET(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/capabilities", nil)
	res := httptest.NewRecorder()

	NewServer("unused.yaml").Handler().ServeHTTP(res, req)

	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusMethodNotAllowed)
	}
}
