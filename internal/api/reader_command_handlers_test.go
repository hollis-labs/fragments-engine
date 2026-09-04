package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capturecontract "github.com/hollis-labs/fragments-engine/contracts/browser-capture-reader/v1"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type fakeReaderCommandExecutor struct {
	item        capturecontract.ReaderItem
	err         error
	principalID string
	fragmentID  string
	command     capturecontract.ReaderCommand
}

func (f *fakeReaderCommandExecutor) Execute(_ context.Context, principalID, fragmentID string, command capturecontract.ReaderCommand) (service.ReaderCommandExecution, error) {
	f.principalID, f.fragmentID, f.command = principalID, fragmentID, command
	return service.ReaderCommandExecution{Item: f.item}, f.err
}

func TestReaderCommandHTTPValidatesFrozenContractAndReturnsSchemaValidProjection(t *testing.T) {
	executor := &fakeReaderCommandExecutor{item: validReaderCommandProjection(t)}
	body := `{"schema_version":"fe.reader.command.v1","command":"add_tag","command_id":"command-api","idempotency_key":"key-api","expected_revision":0,"tag":"reader"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/reader/items/fragment-api/commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	executeReaderCommandHTTP(response, req, "fragment-api", executor)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if executor.principalID != service.LocalReaderPrincipal || executor.fragmentID != "fragment-api" || executor.command.Command != "add_tag" {
		t.Fatalf("edge mapping = principal=%q fragment=%q command=%+v", executor.principalID, executor.fragmentID, executor.command)
	}
	if err := capturecontract.ValidateJSON(capturecontract.SchemaReaderItem, response.Body.Bytes()); err != nil {
		t.Fatalf("response is not a ReaderItem: %v\n%s", err, response.Body.String())
	}
}

func TestReaderCommandHTTPProblemsAndBodyLimit(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		exec   *fakeReaderCommandExecutor
		status int
	}{
		{name: "closed union", body: `{"schema_version":"fe.reader.command.v1","command":"patch_metadata","command_id":"bad","idempotency_key":"bad","expected_revision":0}`, exec: &fakeReaderCommandExecutor{}, status: http.StatusBadRequest},
		{name: "invalid discriminated position", body: `{"schema_version":"fe.reader.command.v1","command":"set_reading_progress","command_id":"bad-position","idempotency_key":"bad-position","expected_revision":0,"position":{"kind":"none","page":1}}`, exec: &fakeReaderCommandExecutor{}, status: http.StatusBadRequest},
		{name: "not found", body: `{"schema_version":"fe.reader.command.v1","command":"mark_read","command_id":"missing","idempotency_key":"missing","expected_revision":0}`, exec: &fakeReaderCommandExecutor{err: sql.ErrNoRows}, status: http.StatusNotFound},
		{name: "conflict", body: `{"schema_version":"fe.reader.command.v1","command":"mark_read","command_id":"conflict","idempotency_key":"conflict","expected_revision":0}`, exec: &fakeReaderCommandExecutor{err: &repository.ReaderRevisionConflictError{Entity: "reading state", Expected: 0, Current: 1}}, status: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/reader/items/fragment/commands", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			executeReaderCommandHTTP(response, req, "fragment", test.exec)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if err := capturecontract.ValidateJSON(capturecontract.SchemaAPIProblem, response.Body.Bytes()); err != nil {
				t.Fatalf("problem response invalid: %v\n%s", err, response.Body.String())
			}
		})
	}

	oversize := bytes.Repeat([]byte("x"), int(maxReaderCommandBytes)+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/reader/items/fragment/commands", bytes.NewReader(oversize))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	executeReaderCommandHTTP(response, req, "fragment", &fakeReaderCommandExecutor{})
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d body=%s", response.Code, response.Body.String())
	}
	if err := capturecontract.ValidateJSON(capturecontract.SchemaAPIProblem, response.Body.Bytes()); err != nil {
		t.Fatalf("oversize problem invalid: %v", err)
	}
}

func TestReaderCommandRouteRemainsUnregisteredUntilProjectorIntegration(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/reader/items/fragment/commands", nil)
	response := httptest.NewRecorder()
	NewServer("unused").Handler().ServeHTTP(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("unintegrated route status=%d", response.Code)
	}
}

type apiReaderProjector struct {
	item capturecontract.ReaderItem
	repo *repository.ReaderCommandRepository
}

func (p apiReaderProjector) ProjectReaderItem(ctx context.Context, principalID, fragmentID string) (capturecontract.ReaderItem, error) {
	item := p.item
	item.FragmentID = fragmentID
	item.ReadingState.FragmentID = fragmentID
	item.ReadingState.PrincipalID = principalID
	revision, err := p.repo.GetAggregateRevision(ctx, principalID, fragmentID)
	item.Revision = revision
	return item, err
}

func TestReaderCommandHTTPSemanticIdentityConflictsAre409(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "reader-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 9, 3, 22, 0, 0, 0, time.UTC)
	built, err := repository.BuildFragment(domain.PipelineFragment{Source: "manual", SourceType: "text",
		SourceID: "reader-api", Title: "Reader API", Content: "body", CreatedAt: now}, "reader-api", now)
	if err != nil {
		t.Fatal(err)
	}
	fragment, _, err := repository.NewFragmentRepository(st.DB).UpsertResolved(context.Background(), built)
	if err != nil {
		t.Fatal(err)
	}
	repo := repository.NewReaderCommandRepository(st.DB)
	serviceUnderTest := service.NewReaderCommandService(repo, nil, nil, apiReaderProjector{item: validReaderCommandProjection(t), repo: repo})
	request := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/reader/items/"+fragment.ID+"/commands", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		executeReaderCommandHTTP(response, req, fragment.ID, serviceUnderTest)
		return response
	}
	first := `{"schema_version":"fe.reader.command.v1","command":"add_tag","command_id":"same-id","idempotency_key":"same-key","expected_revision":0,"tag":"one"}`
	if response := request(first); response.Code != http.StatusOK {
		t.Fatalf("first status=%d body=%s", response.Code, response.Body.String())
	}
	changedID := `{"schema_version":"fe.reader.command.v1","command":"add_tag","command_id":"same-id","idempotency_key":"same-key","expected_revision":0,"tag":"two"}`
	if response := request(changedID); response.Code != http.StatusConflict {
		t.Fatalf("same ID changed payload status=%d body=%s", response.Code, response.Body.String())
	}
	seedKey := `{"schema_version":"fe.reader.command.v1","command":"add_tag","command_id":"key-owner","idempotency_key":"reused-key","expected_revision":0,"tag":"three"}`
	if response := request(seedKey); response.Code != http.StatusOK {
		t.Fatalf("key seed status=%d body=%s", response.Code, response.Body.String())
	}
	changedKey := `{"schema_version":"fe.reader.command.v1","command":"add_tag","command_id":"different-id","idempotency_key":"reused-key","expected_revision":0,"tag":"four"}`
	if response := request(changedKey); response.Code != http.StatusConflict {
		t.Fatalf("same key changed payload status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestReaderCommandPathParser(t *testing.T) {
	fragmentID, err := readerCommandPathFragment("/v1/reader/items/legacy%3Aid/commands")
	if err != nil || fragmentID != "legacy:id" {
		t.Fatalf("parsed fragment=%q err=%v", fragmentID, err)
	}
	for _, path := range []string{
		"/v1/reader/items//commands",
		"/v1/reader/items/a/b/commands",
		"/v1/reader/items/a%2Fb/commands",
		"/v1/reader/items/a%5Cb/commands",
		"/v1/reader/items/a\\b/commands",
		"/v1/reader/items/a%0Ab/commands",
		"/v1/reader/items/" + strings.Repeat("a", 256) + "/commands",
		"/v1/reader/items/a",
	} {
		if _, err := readerCommandPathFragment(path); err == nil {
			t.Fatalf("invalid path accepted: %s", path)
		}
	}
}

func validReaderCommandProjection(t *testing.T) capturecontract.ReaderItem {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/browser-capture-reader/v1/fixtures/valid-reader-non-web.json")
	if err != nil {
		t.Fatal(err)
	}
	var item capturecontract.ReaderItem
	if err := json.Unmarshal(raw, &item); err != nil {
		t.Fatal(err)
	}
	return item
}
