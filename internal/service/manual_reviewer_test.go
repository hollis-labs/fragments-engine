package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/ingest"
	"github.com/hollis-labs/fragments-engine/internal/recall"
	"github.com/hollis-labs/fragments-engine/internal/repository"
	"github.com/hollis-labs/fragments-engine/internal/store"
)

type manualTestServices struct {
	fragments  *FragmentService
	reviewer   *InboxReviewerService
	inbox      *InboxService
	corpusRoot string
	close      func()
}

func setupManualTestServices(t *testing.T) manualTestServices {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "fragments.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	fragmentRepo := repository.NewFragmentRepository(st.DB)
	entityRepo := repository.NewEntityRepository(st.DB)
	attachmentRepo := repository.NewAttachmentRepository(st.DB)
	inboxRepo := repository.NewInboxRepository(st.DB)
	routingRepo := repository.NewRoutingRepository(st.DB)
	recallIndex := recall.NewSQLiteIndexer(fragmentRepo, entityRepo)
	enricher := NewManualIntakeEnricher(nil, filepath.Join(t.TempDir(), "reviewer"))
	corpusRoot := filepath.Join(t.TempDir(), "corpus")
	corpusWriter := NewPinterestCorpusWriter(corpusRoot)
	pipeline := ingest.NewPipeline(fragmentRepo, nil, []ingest.Stage{
		ingest.NewAttachmentStage(attachmentRepo),
		ingest.NewRouteStage(fragmentRepo, attachmentRepo, routingRepo, inboxRepo, nil),
		ingest.NewInboxStage(inboxRepo),
		ingest.NewRecallStage(recallIndex),
	})
	return manualTestServices{
		fragments:  NewFragmentService(fragmentRepo, entityRepo, attachmentRepo, routingRepo, recallIndex, pipeline, nil, enricher, corpusWriter),
		reviewer:   NewInboxReviewerService(fragmentRepo, entityRepo, attachmentRepo, inboxRepo, enricher, corpusWriter, nil, ""),
		inbox:      NewInboxService(inboxRepo),
		corpusRoot: corpusRoot,
		close: func() {
			_ = recallIndex.Close()
			_ = st.Close()
		},
	}
}

func TestFragmentServiceIntakeEnrichesGitHubRepoURL(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	result, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://github.com/openai/openai-go",
		Tags:    []string{"golang"},
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), result.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Fragment.SourceType != "repo" {
		t.Fatalf("expected repo source type, got %s", detail.Fragment.SourceType)
	}
	if detail.Fragment.Title != "openai/openai-go" {
		t.Fatalf("unexpected title: %s", detail.Fragment.Title)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"suggested_destination":"stack_explorer"`) {
		t.Fatalf("expected stack explorer hint in metadata: %s", detail.Fragment.MetadataJSON)
	}
	if len(detail.Attachments) != 1 || detail.Attachments[0].ExternalURL != "https://github.com/openai/openai-go" {
		t.Fatalf("expected github url reference attachment, got %+v", detail.Attachments)
	}
	assertEntityPresent(t, detail.Entities, "repo", "openai/openai-go")
	assertEntityPresent(t, detail.Entities, "repo_owner", "openai")
	assertEntityPresent(t, detail.Entities, "platform", "github")
	assertEntityPresent(t, detail.Entities, "tag", "golang")
}

func TestInboxReviewerReviewsPinterestPinAndDownloadsImage(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	imageBytes := mustPNG(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pin/123456/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><html><head>
<meta content="Warm minimal office desk" data-app="true" name="og:title" property="og:title">
<meta content="A workspace inspiration pin with wood tones and simple shelving." data-app="true" name="og:description" property="og:description">
<meta content="` + server.URL + `/img.png" data-app="true" name="og:image" property="og:image">
</head><body>pin</body></html>`))
		case "/img.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(imageBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	svcs.reviewer.enricher.pinterestBase = server.URL
	svcs.reviewer.enricher.client = server.Client()

	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://www.pinterest.com/pin/123456/",
	})
	if err != nil {
		t.Fatalf("intake pin: %v", err)
	}
	review, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("review once: %v", err)
	}
	if review.ReviewedCount != 1 || review.UpdatedCount != 1 {
		t.Fatalf("unexpected review result: %+v", review)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail after review: %v", err)
	}
	if detail.Fragment.Title != "Warm minimal office desk" {
		t.Fatalf("unexpected reviewed title: %s", detail.Fragment.Title)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"pin_image_url":"`+server.URL+`/img.png"`) {
		t.Fatalf("expected pin image metadata: %s", detail.Fragment.MetadataJSON)
	}
	if len(detail.Attachments) < 2 {
		t.Fatalf("expected url reference + downloaded image attachments, got %+v", detail.Attachments)
	}
	var foundImage bool
	for _, item := range detail.Attachments {
		if item.Kind != "image" {
			continue
		}
		foundImage = true
		if item.SourcePath == "" {
			t.Fatalf("expected local image source path: %+v", item)
		}
		if _, err := os.Stat(item.SourcePath); err != nil {
			t.Fatalf("expected downloaded image to exist: %v", err)
		}
		if item.Metadata["image_format"] == nil {
			t.Fatalf("expected extracted image metadata: %+v", item.Metadata)
		}
	}
	if !foundImage {
		t.Fatalf("expected downloaded image attachment in %+v", detail.Attachments)
	}
	items, err := svcs.inbox.ListDetailed(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].Reason, "reviewed pinterest pin") {
		t.Fatalf("unexpected inbox reason after review: %+v", items)
	}
	if items[0].PreviewAttachmentID == "" {
		t.Fatalf("expected pinterest inbox row to surface a preview attachment id: %+v", items[0])
	}
	corpusPath := filepath.Join(svcs.corpusRoot, "pinterest", "123456", "fragment.md")
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read pinterest corpus doc: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "# Warm minimal office desk") {
		t.Fatalf("expected corpus doc title, got: %s", body)
	}
	if !strings.Contains(body, "## Description") || !strings.Contains(body, "workspace inspiration pin") {
		t.Fatalf("expected pin description in corpus doc: %s", body)
	}
	if !strings.Contains(body, "attachments/previews/") {
		t.Fatalf("expected preview image reference in corpus doc: %s", body)
	}
	results, err := svcs.fragments.Search(context.Background(), "Warm minimal office desk", 10)
	if err != nil {
		t.Fatalf("search pinterest fragment: %v", err)
	}
	if len(results) == 0 || results[0].PreviewAttachmentID == "" {
		t.Fatalf("expected search results to include preview attachment id: %+v", results)
	}
}

func TestFragmentServiceUpdateManualFragment(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://www.pinterest.com/pin/123456/",
		Tags:    []string{"seed"},
	})
	if err != nil {
		t.Fatalf("intake: %v", err)
	}

	detail, err := svcs.fragments.UpdateManualFragment(context.Background(), UpdateFragmentRequest{
		FragmentID: intake.FragmentID,
		Title:      "Desk moodboard",
		Summary:    "Warm wood, shelves, and compact workspace ideas.",
		Notes:      "Focus on references that can become corpus docs later.",
		Tags:       []string{"pinterest", "workspace", "pinterest"},
	})
	if err != nil {
		t.Fatalf("update manual fragment: %v", err)
	}
	if detail.Fragment.Title != "Desk moodboard" {
		t.Fatalf("unexpected title: %s", detail.Fragment.Title)
	}
	if detail.Fragment.Summary != "Warm wood, shelves, and compact workspace ideas." {
		t.Fatalf("unexpected summary: %s", detail.Fragment.Summary)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"user_notes":"Focus on references that can become corpus docs later."`) {
		t.Fatalf("expected notes in metadata: %s", detail.Fragment.MetadataJSON)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"user_tags":["pinterest","workspace"]`) {
		t.Fatalf("expected tags in metadata: %s", detail.Fragment.MetadataJSON)
	}
	assertEntityPresent(t, detail.Entities, "tag", "pinterest")
	assertEntityPresent(t, detail.Entities, "tag", "workspace")
	for _, entity := range detail.Entities {
		if entity.Kind == "tag" && entity.Value == "seed" {
			t.Fatalf("expected old tag to be replaced, got %+v", detail.Entities)
		}
	}

	refetched, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("refetch detail: %v", err)
	}
	if refetched.Fragment.Title != "Desk moodboard" || refetched.Fragment.Summary != "Warm wood, shelves, and compact workspace ideas." {
		t.Fatalf("expected updated fragment in recall-backed detail: %+v", refetched.Fragment)
	}
	corpusPath := filepath.Join(svcs.corpusRoot, "pinterest", "123456", "fragment.md")
	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read updated pinterest corpus doc: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "## Notes") || !strings.Contains(body, "Focus on references that can become corpus docs later.") {
		t.Fatalf("expected notes in corpus doc: %s", body)
	}
	if !strings.Contains(body, "- pinterest") || !strings.Contains(body, "- workspace") {
		t.Fatalf("expected tags in corpus doc: %s", body)
	}
}

func TestFragmentServiceBackfillPinterestCorpus(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	pin, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://www.pinterest.com/pin/123456/",
	})
	if err != nil {
		t.Fatalf("intake pin: %v", err)
	}
	if _, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "plain text note",
	}); err != nil {
		t.Fatalf("intake non-pin: %v", err)
	}

	corpusPath := filepath.Join(svcs.corpusRoot, "pinterest", "123456", "fragment.md")
	if _, err := os.Stat(corpusPath); !os.IsNotExist(err) {
		t.Fatalf("expected no corpus doc before backfill, stat err=%v", err)
	}

	result, err := svcs.fragments.BackfillPinterestCorpus(context.Background(), 0)
	if err != nil {
		t.Fatalf("backfill pinterest corpus: %v", err)
	}
	if result.ScannedCount != 1 || result.CandidateCount != 1 || result.WrittenCount != 1 {
		t.Fatalf("unexpected backfill result: %+v", result)
	}
	if len(result.WrittenPaths) != 1 || result.WrittenPaths[0] != corpusPath {
		t.Fatalf("unexpected written paths: %+v", result.WrittenPaths)
	}

	raw, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatalf("read backfilled corpus doc: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "# 123456") && !strings.Contains(body, "# Pinterest pin") {
		t.Fatalf("expected corpus markdown title, got: %s", body)
	}

	detail, err := svcs.fragments.GetDetail(context.Background(), pin.FragmentID, 5)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if detail.Fragment.SourceType != "pin" {
		t.Fatalf("expected pin source type after backfill candidate selection, got %s", detail.Fragment.SourceType)
	}
}

func assertEntityPresent(t *testing.T, entities []domain.FragmentEntity, kind, value string) {
	t.Helper()
	for _, entity := range entities {
		if entity.Kind == kind && entity.Value == value {
			return
		}
	}
	t.Fatalf("expected entity %s=%s in %+v", kind, value, entities)
}

func mustPNG(t *testing.T) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO7Z0xoAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return raw
}

func TestReviewerConfigDefaults(t *testing.T) {
	cfg := config.Config{Database: config.DatabaseConfig{Path: ":memory:"}, Ingests: []config.IngestConfig{{Name: "x", Kind: "claude_code", Source: config.IngestSource{Root: "."}}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
	if cfg.Reviewer.PollIntervalSeconds <= 0 || cfg.Reviewer.BatchSize <= 0 || cfg.Reviewer.DownloadRoot == "" {
		t.Fatalf("expected reviewer defaults to be set: %+v", cfg.Reviewer)
	}
}

func TestNeedsReReviewForPinterestPinWithoutImage(t *testing.T) {
	svc := &InboxReviewerService{}
	if !svc.needsReReview(domain.Fragment{SourceType: "pin"}, map[string]any{"review_version": inboxReviewerVersion}) {
		t.Fatal("expected pinterest pin without pin_image_url to require re-review")
	}
	if svc.needsReReview(domain.Fragment{SourceType: "pin"}, map[string]any{"review_version": inboxReviewerVersion, "pin_image_url": "https://i.pinimg.com/x.jpg"}) {
		t.Fatal("expected pinterest pin with downloaded image metadata to skip re-review")
	}
	if svc.needsReReview(domain.Fragment{SourceType: "repo"}, map[string]any{"review_version": inboxReviewerVersion}) {
		t.Fatal("expected non-pin fragments to skip re-review")
	}
}

func TestInboxReviewerSyncsGitHubRepoToStackExplorer(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	var importHits, updateHits, tagSyncHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/repos/github-openai-openai-go/":
			http.Error(w, `{"error":"repo not found"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/repos/import":
			importHits++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode import request: %v", err)
			}
			repos, ok := body["repos"].([]any)
			if !ok || len(repos) != 1 {
				t.Fatalf("expected one repo in import request: %+v", body)
			}
			repo, ok := repos[0].(map[string]any)
			if !ok {
				t.Fatalf("unexpected repo manifest shape: %+v", repos[0])
			}
			if repo["id"] != "github-openai-openai-go" {
				t.Fatalf("unexpected repo id: %+v", repo)
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"added": 1, "skipped": 0})
		case r.Method == http.MethodPost && r.URL.Path == "/api/repos/github-openai-openai-go/tags":
			tagSyncHits++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode tag sync request: %v", err)
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"data": map[string]any{"repo_id": "github-openai-openai-go", "tags": body["tags"]}})
		case r.Method == http.MethodPut && r.URL.Path == "/api/repos/github-openai-openai-go/":
			updateHits++
			writeJSONResponse(w, http.StatusOK, map[string]any{"data": map[string]any{"id": "github-openai-openai-go"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	svcs.reviewer.stackClient = NewStackExplorerClient(server.URL)

	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://github.com/openai/openai-go",
		Tags:    []string{"sdk"},
	})
	if err != nil {
		t.Fatalf("intake github repo: %v", err)
	}
	review, err := svcs.reviewer.ReviewOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("review once: %v", err)
	}
	if review.ReviewedCount != 1 || review.UpdatedCount != 1 {
		t.Fatalf("unexpected review result: %+v", review)
	}
	if importHits != 1 || updateHits != 0 || tagSyncHits != 1 {
		t.Fatalf("expected one import, no update, one tag sync; got import=%d update=%d tags=%d", importHits, updateHits, tagSyncHits)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail after review: %v", err)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"stack_explorer_repo_id":"github-openai-openai-go"`) {
		t.Fatalf("expected stack explorer repo id in metadata: %s", detail.Fragment.MetadataJSON)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"stack_explorer_sync_status":"created"`) {
		t.Fatalf("expected stack explorer created status in metadata: %s", detail.Fragment.MetadataJSON)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"stack_explorer_tag_sync_status":"synced"`) {
		t.Fatalf("expected stack explorer tag sync status in metadata: %s", detail.Fragment.MetadataJSON)
	}
}

func TestInboxReviewerQueuesStackExplorerScan(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	var importHits, tagSyncHits, scanListHits, scanCreateHits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/repos/github-openai-openai-go/":
			http.Error(w, `{"error":"repo not found"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/api/repos/import":
			importHits++
			writeJSONResponse(w, http.StatusOK, map[string]any{"added": 1, "skipped": 0})
		case r.Method == http.MethodPost && r.URL.Path == "/api/repos/github-openai-openai-go/tags":
			tagSyncHits++
			writeJSONResponse(w, http.StatusOK, map[string]any{"data": map[string]any{"repo_id": "github-openai-openai-go", "tags": []string{"github"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/scans/":
			scanListHits++
			writeJSONResponse(w, http.StatusOK, map[string]any{"data": []any{}, "meta": map[string]any{"total": 0}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/scans/":
			scanCreateHits++
			writeJSONResponse(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": "77", "status": "pending"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	svcs.reviewer.stackClient = NewStackExplorerClient(server.URL)
	svcs.reviewer.stackScan = "se-repo-scan"

	intake, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://github.com/openai/openai-go",
	})
	if err != nil {
		t.Fatalf("intake github repo: %v", err)
	}
	if _, err := svcs.reviewer.ReviewOnce(context.Background(), 10); err != nil {
		t.Fatalf("review once: %v", err)
	}
	if importHits != 1 || tagSyncHits != 1 || scanListHits != 0 || scanCreateHits != 1 {
		t.Fatalf("unexpected stack explorer activity import=%d tags=%d scanList=%d scanCreate=%d", importHits, tagSyncHits, scanListHits, scanCreateHits)
	}
	detail, err := svcs.fragments.GetDetail(context.Background(), intake.FragmentID, 5)
	if err != nil {
		t.Fatalf("detail after review: %v", err)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"stack_explorer_scan_id":"77"`) {
		t.Fatalf("expected stack explorer scan id in metadata: %s", detail.Fragment.MetadataJSON)
	}
	if !strings.Contains(detail.Fragment.MetadataJSON, `"stack_explorer_scan_blueprint":"se-repo-scan"`) {
		t.Fatalf("expected stack explorer scan blueprint in metadata: %s", detail.Fragment.MetadataJSON)
	}
}

func TestInboxReviewerUsesGitHubTokenForRepoReview(t *testing.T) {
	svcs := setupManualTestServices(t)
	defer svcs.close()

	var gotAuth, gotUserAgent, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotUserAgent = r.Header.Get("User-Agent")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		writeJSONResponse(w, http.StatusOK, map[string]any{
			"full_name":        "openai/openai-go",
			"description":      "Official Go SDK",
			"html_url":         "https://github.com/openai/openai-go",
			"language":         "Go",
			"stargazers_count": 123,
			"topics":           []string{"sdk", "go"},
		})
	}))
	defer server.Close()

	svcs.reviewer.enricher.githubAPIBase = server.URL
	svcs.reviewer.enricher.client = server.Client()
	svcs.reviewer.enricher.SetGitHubToken("test-token")

	if _, err := svcs.fragments.Intake(context.Background(), IntakeRequest{
		Content: "https://github.com/openai/openai-go",
	}); err != nil {
		t.Fatalf("intake github repo: %v", err)
	}
	if _, err := svcs.reviewer.ReviewOnce(context.Background(), 10); err != nil {
		t.Fatalf("review once: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("expected bearer token header, got %q", gotAuth)
	}
	if gotUserAgent != "FragmentsEngine/0.1 (+manual-reviewer)" {
		t.Fatalf("unexpected user-agent header: %q", gotUserAgent)
	}
	if gotVersion != "2022-11-28" {
		t.Fatalf("unexpected github api version header: %q", gotVersion)
	}
}

func TestStackExplorerTagsIncludesStringTopicSlices(t *testing.T) {
	tags := stackExplorerTags(map[string]any{
		"repo_topics": []string{"memory", "agents"},
		"input_tags":  []string{"research"},
	})
	joined := strings.Join(tags, ",")
	for _, want := range []string{"github", "memory", "agents", "research"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in tags %v", want, tags)
		}
	}
}

func TestShouldCreateStackExplorerScanSkipsMatchingExistingScan(t *testing.T) {
	if shouldCreateStackExplorerScan(map[string]any{
		"stack_explorer_scan_id":        "123",
		"stack_explorer_scan_repo_id":   "github-openai-openai-go",
		"stack_explorer_scan_blueprint": "se-repo-scan",
		"stack_explorer_scan_status":    "pending",
	}, "github-openai-openai-go", "se-repo-scan") {
		t.Fatal("expected existing matching pending scan to be reused")
	}
	if !shouldCreateStackExplorerScan(map[string]any{
		"stack_explorer_scan_id":        "123",
		"stack_explorer_scan_repo_id":   "other-repo",
		"stack_explorer_scan_blueprint": "se-repo-scan",
		"stack_explorer_scan_status":    "pending",
	}, "github-openai-openai-go", "se-repo-scan") {
		t.Fatal("expected mismatched repo scan metadata to trigger a new scan")
	}
}

func writeJSONResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		panic(err)
	}
}
