package service_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
)

func TestFragmentsService_IngestAndSearchClaudeSession(t *testing.T) {
	cfgPath := writeIntegrationConfig(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()

	runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run ingests: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 ingest run, got %d", len(runs))
	}
	if runs[0].Inserted != 1 {
		t.Fatalf("expected 1 insert, got %d", runs[0].Inserted)
	}

	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].Fragment.SourceID != "session-123" {
		t.Fatalf("unexpected source id: %s", results[0].Fragment.SourceID)
	}

	inboxItems, err := instance.Inbox.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inboxItems) != 1 {
		t.Fatalf("expected 1 inbox item, got %d", len(inboxItems))
	}
	if inboxItems[0].Reason != "awaiting routing" {
		t.Fatalf("unexpected inbox reason: %s", inboxItems[0].Reason)
	}

	runs, err = instance.Fragments.RunAllIngests(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run ingests second pass: %v", err)
	}
	if runs[0].Skipped != 1 {
		t.Fatalf("expected 1 skipped fragment on second pass, got %d", runs[0].Skipped)
	}

	detail, err := instance.Fragments.GetDetail(context.Background(), results[0].Fragment.ID, 10)
	if err != nil {
		t.Fatalf("get fragment detail: %v", err)
	}
	if detail.Fragment.Summary == "" {
		t.Fatal("expected fragment summary to be indexed")
	}
	if detail.Fragment.IndexedAt.IsZero() {
		t.Fatal("expected fragment to have indexed_at set")
	}
	if len(detail.Entities) == 0 {
		t.Fatal("expected fragment detail to include extracted entities")
	}
	for _, entity := range detail.Entities {
		if entity.Kind == "model" && entity.Value == "claude" {
			t.Fatal("expected generic claude token to be filtered from model entities")
		}
		if entity.Confidence <= 0 {
			t.Fatal("expected extracted entities to include positive confidence")
		}
	}
	if len(detail.Relations) != 0 {
		t.Fatalf("expected no durable relations for single-fragment ingest, got %d", len(detail.Relations))
	}
	entities, err := instance.Fragments.ListEntities(context.Background(), "", 20)
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if len(entities) == 0 {
		t.Fatal("expected persisted entities")
	}
	inboxGroups, err := instance.Inbox.ListEntityGroups(context.Background(), "", 20)
	if err != nil {
		t.Fatalf("list inbox entity groups: %v", err)
	}
	if len(inboxGroups) == 0 {
		t.Fatal("expected inbox entity groups")
	}
	status := instance.RecallStatus()
	if status.Backend != "sqlite" {
		t.Fatalf("expected sqlite backend, got %s", status.Backend)
	}
	if status.EmbeddingsEnabled {
		t.Fatal("expected sqlite backend to report embeddings disabled")
	}
}

func TestFragmentsService_AutoRouteBySourceAndType(t *testing.T) {
	cfgPath := writeIntegrationConfig(t)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	corpusRoot := filepath.Join(filepath.Dir(cfgPath), "corpus")

	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()

	dest, err := instance.Routing.AddDestination(context.Background(), domain.Destination{
		Name:       "local-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"` + corpusRoot + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	_, err = instance.Routing.AddRoute(context.Background(), domain.Route{
		Name:          "claude-chat-auto",
		MatchSource:   "claude",
		MatchType:     "chat",
		DestinationID: dest.ID,
		AutoRoute:     true,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run ingests: %v", err)
	}
	if runs[0].Inserted != 1 {
		t.Fatalf("expected 1 insert, got %d", runs[0].Inserted)
	}

	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].Fragment.Status != domain.FragmentStatusRouted {
		t.Fatalf("expected routed status, got %s", results[0].Fragment.Status)
	}

	inboxItems, err := instance.Inbox.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(inboxItems) != 0 {
		t.Fatalf("expected empty inbox, got %d items", len(inboxItems))
	}

	logEntries, err := instance.Routing.ListRouteLog(context.Background(), results[0].Fragment.ID)
	if err != nil {
		t.Fatalf("list route log: %v", err)
	}
	if len(logEntries) != 1 {
		t.Fatalf("expected 1 route log entry, got %d", len(logEntries))
	}
	if logEntries[0].Decision != "auto_route" {
		t.Fatalf("unexpected route decision: %s", logEntries[0].Decision)
	}
	if !strings.Contains(logEntries[0].Reason, "matched_auto_route:") {
		t.Fatalf("unexpected route log reason: %s", logEntries[0].Reason)
	}

	writtenPath := filepath.Join(corpusRoot, "fragments", "chats", "claude", "2026-04-25", "session-123", "fragment.md")
	raw, err := os.ReadFile(writtenPath)
	if err != nil {
		t.Fatalf("read written destination file: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "# Claude session: roadmap-review") {
		t.Fatalf("written file missing title: %s", text)
	}
	if !strings.Contains(text, "The roadmap starts with deterministic ingest and search.") {
		t.Fatalf("written file missing assistant content: %s", text)
	}
}

func TestFragmentsService_RelatedFragments(t *testing.T) {
	root := writeClaudeFixture(t, []string{
		`{"type":"user","timestamp":"2026-04-25T10:00:00Z","sessionId":"session-123","slug":"roadmap-review","cwd":"/tmp/sample","message":{"content":"Summarize the roadmap."}}
{"type":"assistant","timestamp":"2026-04-25T10:00:05Z","sessionId":"session-123","slug":"roadmap-review","cwd":"/tmp/sample","message":{"content":[{"type":"text","text":"The roadmap starts with deterministic ingest and search."}]}}`,
		`{"type":"user","timestamp":"2026-04-25T11:00:00Z","sessionId":"session-456","slug":"roadmap-followup","cwd":"/tmp/sample","message":{"content":"What is next for search and recall?"}}
{"type":"assistant","timestamp":"2026-04-25T11:00:05Z","sessionId":"session-456","slug":"roadmap-followup","cwd":"/tmp/sample","message":{"content":[{"type":"text","text":"Next add summaries, relationships, and related-fragment recall."}]}}`,
	})
	cfgPath := writeIntegrationConfigForRoot(t, root)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()
	if _, err := instance.Fragments.RunAllIngests(context.Background(), cfg); err != nil {
		t.Fatalf("run ingests: %v", err)
	}
	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 search results, got %d", len(results))
	}
	related, err := instance.Fragments.Related(context.Background(), results[0].Fragment.ID, 10)
	if err != nil {
		t.Fatalf("related: %v", err)
	}
	if len(related) == 0 {
		t.Fatal("expected related fragments")
	}
	detail, err := instance.Fragments.GetDetail(context.Background(), results[0].Fragment.ID, 10)
	if err != nil {
		t.Fatalf("get detail: %v", err)
	}
	if len(detail.Relations) == 0 {
		t.Fatal("expected durable relations in fragment detail")
	}
	kinds := make([]string, 0, len(detail.Relations))
	for _, item := range detail.Relations {
		kinds = append(kinds, item.Relation.Kind)
	}
	if !slices.Contains(kinds, "shared_source_type") {
		t.Fatalf("expected shared_source_type relation, got %v", kinds)
	}
	if !slices.Contains(kinds, "shared_terms") {
		t.Fatalf("expected shared_terms relation, got %v", kinds)
	}
	if !slices.Contains(kinds, "shared_topic_terms") {
		t.Fatalf("expected shared_topic_terms relation, got %v", kinds)
	}
	if !slices.Contains(kinds, "shared_repo") {
		t.Fatalf("expected shared_repo relation, got %v", kinds)
	}
	entityResults, err := instance.Fragments.FragmentsByEntity(context.Background(), "repo", "sample", 10)
	if err == nil && len(entityResults) > 0 {
		t.Fatal("did not expect sample entity in this fixture")
	}
	if related[0].Trace.Backend != "sqlite" {
		t.Fatalf("expected sqlite related trace, got %s", related[0].Trace.Backend)
	}
	if related[0].Trace.Strategy != "fragment_link" {
		t.Fatalf("expected fragment_link strategy, got %s", related[0].Trace.Strategy)
	}
}

func TestFragmentsService_IngestAndSearchChatGPTExport(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "chatgpt-export"))
	if err != nil {
		t.Fatalf("resolve chatgpt fixture root: %v", err)
	}
	cfgPath := writeChatGPTIntegrationConfig(t, root)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()

	runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
	if err != nil {
		t.Fatalf("run ingests: %v", err)
	}
	if len(runs) != 1 || runs[0].Inserted != 1 {
		t.Fatalf("expected 1 inserted chatgpt fragment, got %+v", runs)
	}

	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].Fragment.Source != "chatgpt" || results[0].Fragment.SourceID != "conv-123" {
		t.Fatalf("unexpected chatgpt result: %+v", results[0].Fragment)
	}
	if !strings.Contains(results[0].Snippet, "roadmap") {
		t.Fatalf("unexpected snippet: %s", results[0].Snippet)
	}
	detail, err := instance.Fragments.GetDetail(context.Background(), results[0].Fragment.ID, 10)
	if err != nil {
		t.Fatalf("get chatgpt detail: %v", err)
	}
	if len(detail.Attachments) != 3 {
		t.Fatalf("expected 3 persisted attachments, got %d", len(detail.Attachments))
	}
	kinds := []string{detail.Attachments[0].Kind, detail.Attachments[1].Kind, detail.Attachments[2].Kind}
	if !slices.Contains(kinds, "image") || !slices.Contains(kinds, "pdf") || !slices.Contains(kinds, "markdown") {
		t.Fatalf("unexpected attachment kinds: %v", kinds)
	}
	if !strings.Contains(detail.Fragment.Content, "This attachment should be searchable through FE recall.") {
		t.Fatalf("expected enriched attachment text in fragment content: %s", detail.Fragment.Content)
	}
	attachmentResults, err := instance.Fragments.Search(context.Background(), "searchable through FE recall", 10)
	if err != nil {
		t.Fatalf("search enriched attachment text: %v", err)
	}
	if len(attachmentResults) == 0 {
		t.Fatal("expected attachment text to be searchable")
	}
	var markdownAttachment *domain.FragmentAttachment
	var imageAttachment *domain.FragmentAttachment
	for i := range detail.Attachments {
		switch detail.Attachments[i].Kind {
		case "markdown":
			markdownAttachment = &detail.Attachments[i]
		case "image":
			imageAttachment = &detail.Attachments[i]
		}
	}
	if markdownAttachment == nil {
		t.Fatal("expected markdown attachment in detail")
	}
	if imageAttachment == nil {
		t.Fatal("expected image attachment in detail")
	}
	if markdownAttachment.Metadata["extractor"] != "markdown_frontmatter" {
		t.Fatalf("expected decoded markdown metadata on attachment detail: %+v", markdownAttachment.Metadata)
	}
	if imageAttachment.AnalysisSummary == "" {
		t.Fatalf("expected structured image analysis summary on attachment detail: %+v", imageAttachment)
	}
	if len(imageAttachment.AnalysisTags) == 0 {
		t.Fatalf("expected structured image analysis tags on attachment detail: %+v", imageAttachment)
	}
	if imageAttachment.VisionSummary != "" || len(imageAttachment.VisionTags) > 0 {
		t.Fatalf("did not expect vision analysis fields without model-backed config: %+v", imageAttachment)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(markdownAttachment.MetadataJSON), &meta); err != nil {
		t.Fatalf("decode markdown attachment metadata: %v", err)
	}
	if meta["extractor"] != "markdown_frontmatter" {
		t.Fatalf("unexpected markdown extractor metadata: %+v", meta)
	}
	if meta["extracted_title"] != "Roadmap Attachment" {
		t.Fatalf("unexpected extracted title metadata: %+v", meta)
	}
	fm, ok := meta["frontmatter"].(map[string]any)
	if !ok || fm["project"] != "fragments-engine" {
		t.Fatalf("unexpected markdown frontmatter metadata: %+v", meta)
	}
}

func TestFragmentsService_ChatGPTDocxAttachmentEnrichment(t *testing.T) {
	fixtureRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "chatgpt-export", "export-001"))
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	root := t.TempDir()
	for _, name := range []string{"diagram.png", "roadmap-note.md", "user.json"} {
		raw, err := os.ReadFile(filepath.Join(fixtureRoot, name))
		if err != nil {
			t.Fatalf("read fixture file %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(root, name), raw, 0o600); err != nil {
			t.Fatalf("write temp fixture file %s: %v", name, err)
		}
	}
	if err := writeDOCXFixture(filepath.Join(root, "roadmap-doc.docx"), "Roadmap Doc", []string{
		"This DOCX attachment should also be searchable.",
		"Deterministic ingest and recall should see this text.",
	}); err != nil {
		t.Fatalf("write docx fixture: %v", err)
	}
	conversationRaw, err := os.ReadFile(filepath.Join(fixtureRoot, "conversations-000.json"))
	if err != nil {
		t.Fatalf("read conversations fixture: %v", err)
	}
	updated := strings.Replace(string(conversationRaw), `"name": "roadmap-note.md"`, `"name": "roadmap-note.md"
              },
              {
                "id": "file-789",
                "mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
                "name": "roadmap-doc.docx"`, 1)
	if err := os.WriteFile(filepath.Join(root, "conversations-000.json"), []byte(updated), 0o600); err != nil {
		t.Fatalf("write updated conversations: %v", err)
	}

	cfgPath := writeChatGPTIntegrationConfig(t, root)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()

	if _, err := instance.Fragments.RunAllIngests(context.Background(), cfg); err != nil {
		t.Fatalf("run ingests: %v", err)
	}
	results, err := instance.Fragments.Search(context.Background(), "DOCX attachment should also be searchable", 10)
	if err != nil {
		t.Fatalf("search docx attachment text: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected DOCX attachment text to be searchable")
	}
	detail, err := instance.Fragments.GetDetail(context.Background(), results[0].Fragment.ID, 10)
	if err != nil {
		t.Fatalf("get docx detail: %v", err)
	}
	var docxAttachment *domain.FragmentAttachment
	for i := range detail.Attachments {
		if detail.Attachments[i].Kind == "docx" {
			docxAttachment = &detail.Attachments[i]
			break
		}
	}
	if docxAttachment == nil {
		t.Fatal("expected docx attachment in detail")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(docxAttachment.MetadataJSON), &meta); err != nil {
		t.Fatalf("decode docx attachment metadata: %v", err)
	}
	if meta["extractor"] != "docx_zip_xml" {
		t.Fatalf("unexpected docx metadata: %+v", meta)
	}
}

func TestFragmentsService_VantaRecallBackend(t *testing.T) {
	root := writeClaudeFixture(t, []string{
		`{"type":"user","timestamp":"2026-04-25T10:00:00Z","sessionId":"session-123","slug":"roadmap-review","cwd":"/tmp/sample","message":{"content":"Summarize the roadmap."}}
{"type":"assistant","timestamp":"2026-04-25T10:00:05Z","sessionId":"session-123","slug":"roadmap-review","cwd":"/tmp/sample","message":{"content":[{"type":"text","text":"The roadmap starts with deterministic ingest and search."}]}}`,
		`{"type":"user","timestamp":"2026-04-25T11:00:00Z","sessionId":"session-456","slug":"roadmap-followup","cwd":"/tmp/sample","message":{"content":"What is next for search and recall?"}}
{"type":"assistant","timestamp":"2026-04-25T11:00:05Z","sessionId":"session-456","slug":"roadmap-followup","cwd":"/tmp/sample","message":{"content":[{"type":"text","text":"Next add summaries, relationships, and related-fragment recall."}]}}`,
	})
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "fragments.db")
	vantaRoot := filepath.Join(tempDir, "vanta")
	cfgPath := writeIntegrationConfigValues(t, dbPath, root, "vanta", vantaRoot)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()

	if _, err := instance.Fragments.RunAllIngests(context.Background(), cfg); err != nil {
		t.Fatalf("run ingests: %v", err)
	}

	results, err := instance.Fragments.Search(context.Background(), "roadmap", 10)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 search results, got %d", len(results))
	}

	related, err := instance.Fragments.Related(context.Background(), results[0].Fragment.ID, 10)
	if err != nil {
		t.Fatalf("related: %v", err)
	}
	if len(related) == 0 {
		t.Fatal("expected related fragments from vanta backend")
	}
	if related[0].Trace.Backend == "" {
		t.Fatal("expected related fragments to include recall trace")
	}
	status := instance.RecallStatus()
	if status.Backend != "vanta" {
		t.Fatalf("expected vanta backend, got %s", status.Backend)
	}
	if status.EmbeddingsEnabled {
		t.Fatal("expected embeddings disabled in test without provider credentials")
	}
	if status.RecallMode != "bm25" {
		t.Fatalf("expected bm25 mode without embeddings, got %s", status.RecallMode)
	}
}

func TestRoutingService_ApplyRouteByEntity(t *testing.T) {
	root := writeClaudeFixture(t, []string{
		`{"type":"user","timestamp":"2026-04-25T10:00:00Z","sessionId":"session-123","slug":"roadmap-review","cwd":"/tmp/sample-project","message":{"content":"Summarize the roadmap with sqlite and llama3.1."}}
{"type":"assistant","timestamp":"2026-04-25T10:00:05Z","sessionId":"session-123","slug":"roadmap-review","cwd":"/tmp/sample-project","message":{"content":[{"type":"text","text":"The roadmap starts with deterministic ingest and search through Vanta and Ollama."}]}}`,
		`{"type":"user","timestamp":"2026-04-25T11:00:00Z","sessionId":"session-456","slug":"roadmap-followup","cwd":"/tmp/sample-project","message":{"content":"What is next for sqlite recall in sample-project?"}}
{"type":"assistant","timestamp":"2026-04-25T11:00:05Z","sessionId":"session-456","slug":"roadmap-followup","cwd":"/tmp/sample-project","message":{"content":[{"type":"text","text":"Next add bulk entity triage for Ollama-backed recall."}]}}`,
	})
	cfgPath := writeIntegrationConfigForRoot(t, root)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	corpusRoot := filepath.Join(filepath.Dir(cfgPath), "corpus")

	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open app: %v", err)
	}
	defer instance.Close()

	if _, err := instance.Fragments.RunAllIngests(context.Background(), cfg); err != nil {
		t.Fatalf("run ingests: %v", err)
	}
	before, err := instance.Inbox.ListByEntity(context.Background(), "repo", "sample-project", 10)
	if err != nil {
		t.Fatalf("list inbox by entity: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("expected 2 staged repo inbox items, got %d", len(before))
	}

	dest, err := instance.Routing.AddDestination(context.Background(), domain.Destination{
		Name:       "manual-corpus",
		Kind:       "file",
		ConfigJSON: `{"root":"` + corpusRoot + `"}`,
	})
	if err != nil {
		t.Fatalf("add destination: %v", err)
	}
	route, err := instance.Routing.AddRoute(context.Background(), domain.Route{
		Name:             "manual-repo-route",
		MatchEntityKind:  "repo",
		MatchEntityValue: "sample-project",
		DestinationID:    dest.ID,
		AutoRoute:        false,
	})
	if err != nil {
		t.Fatalf("add route: %v", err)
	}

	result, err := instance.Routing.ApplyRouteByEntity(context.Background(), route.ID, "repo", "sample-project", 10)
	if err != nil {
		t.Fatalf("apply route by entity: %v", err)
	}
	if result.MatchedCount != 2 {
		t.Fatalf("expected 2 matched items, got %d", result.MatchedCount)
	}
	if result.RoutedCount != 2 {
		t.Fatalf("expected 2 routed items, got %d", result.RoutedCount)
	}
	if result.FailedCount != 0 {
		t.Fatalf("expected 0 failed items, got %d", result.FailedCount)
	}

	after, err := instance.Inbox.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("list inbox: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("expected empty inbox after manual route, got %d", len(after))
	}

	for _, item := range result.Items {
		if item.Status != "routed" {
			t.Fatalf("expected routed item status, got %s", item.Status)
		}
		if item.WrittenPath == "" {
			t.Fatal("expected written path for routed item")
		}
		raw, err := os.ReadFile(item.WrittenPath)
		if err != nil {
			t.Fatalf("read routed file: %v", err)
		}
		if !strings.Contains(string(raw), "status: routed") {
			t.Fatalf("expected routed status in written file: %s", string(raw))
		}
		logEntries, err := instance.Routing.ListRouteLog(context.Background(), item.FragmentID)
		if err != nil {
			t.Fatalf("list route log: %v", err)
		}
		if len(logEntries) == 0 {
			t.Fatal("expected route log entries")
		}
		if logEntries[len(logEntries)-1].Decision != "manual_route" {
			t.Fatalf("expected manual_route decision, got %s", logEntries[len(logEntries)-1].Decision)
		}
		if !strings.Contains(logEntries[len(logEntries)-1].Reason, `manual_route_entity:{"entity_kind":"repo","entity_value":"sample-project"`) {
			t.Fatalf("unexpected manual route reason: %s", logEntries[len(logEntries)-1].Reason)
		}
	}
}

func writeIntegrationConfig(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "fragments.db")
	claudeRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "claude"))
	if err != nil {
		t.Fatalf("resolve testdata path: %v", err)
	}
	return writeIntegrationConfigValues(t, dbPath, claudeRoot, "sqlite", "")
}

func writeIntegrationConfigForRoot(t *testing.T, claudeRoot string) string {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "fragments.db")
	return writeIntegrationConfigValues(t, dbPath, claudeRoot, "sqlite", "")
}

func writeIntegrationConfigValues(t *testing.T, dbPath, claudeRoot, recallBackend, vantaRoot string) string {
	t.Helper()
	cfgPath := filepath.Join(filepath.Dir(dbPath), "fragments.yaml")
	cfgRaw := []byte("database:\n  path: " + dbPath + "\n\nrecall:\n  backend: " + recallBackend + "\n  vanta:\n    root: " + vantaRoot + "\n    embedding_provider: none\n    embedding_model: \"\"\n\ningests:\n  - name: claude-integration\n    kind: claude_code\n    enabled: true\n    source:\n      root: " + claudeRoot + "\n    routing:\n      namespace: fragments/chats/claude\n    rules:\n      max_file_size_mb: 5\n")
	if err := os.WriteFile(cfgPath, cfgRaw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

func writeChatGPTIntegrationConfig(t *testing.T, chatGPTRoot string) string {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "fragments.db")
	cfgPath := filepath.Join(tempDir, "fragments.yaml")
	cfgRaw := []byte("database:\n  path: " + dbPath + "\n\nrecall:\n  backend: sqlite\n  vanta:\n    root: \"\"\n    embedding_provider: none\n    embedding_model: \"\"\n\ningests:\n  - name: chatgpt-integration\n    kind: chatgpt_export\n    enabled: true\n    source:\n      root: " + chatGPTRoot + "\n    routing:\n      namespace: fragments/chats/chatgpt\n    rules:\n      max_file_size_mb: 5\n")
	if err := os.WriteFile(cfgPath, cfgRaw, 0o600); err != nil {
		t.Fatalf("write chatgpt config: %v", err)
	}
	return cfgPath
}

func writeDOCXFixture(path, title string, paragraphs []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	zw := zip.NewWriter(file)
	doc, err := zw.Create("word/document.xml")
	if err != nil {
		return err
	}
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><document><body>`)
	for _, p := range paragraphs {
		body.WriteString(`<p><r><t>`)
		body.WriteString(p)
		body.WriteString(`</t></r></p>`)
	}
	body.WriteString(`</body></document>`)
	if _, err := doc.Write(body.Bytes()); err != nil {
		return err
	}
	core, err := zw.Create("docProps/core.xml")
	if err != nil {
		return err
	}
	if _, err := core.Write([]byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><coreProperties><title>` + title + `</title></coreProperties>`)); err != nil {
		return err
	}
	return zw.Close()
}

func writeClaudeFixture(t *testing.T, sessions []string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "claude")
	projectDir := filepath.Join(root, "projects", "sample-project")
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	for i, session := range sessions {
		name := fmt.Sprintf("session-%d.jsonl", i+1)
		if err := os.WriteFile(filepath.Join(projectDir, name), []byte(session), 0o600); err != nil {
			t.Fatalf("write fixture file: %v", err)
		}
	}
	return root
}
