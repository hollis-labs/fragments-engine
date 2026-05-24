package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hollis-labs/fragments-engine/internal/api"
	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	mcpserver "github.com/hollis-labs/fragments-engine/internal/mcp"
	"github.com/hollis-labs/fragments-engine/internal/service"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "ingest":
		return runIngest(args[1:])
	case "search":
		return runSearch(args[1:])
	case "fragment":
		return runFragment(args[1:])
	case "entity":
		return runEntity(args[1:])
	case "inbox":
		return runInbox(args[1:])
	case "route":
		return runRoute(args[1:])
	case "queue":
		return runQueue(args[1:])
	case "recall":
		return runRecall(args[1:])
	case "serve-api":
		return runServeAPI(args[1:])
	case "serve-mcp":
		return runServeMCP(args[1:])
	default:
		return usageError()
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	return app.InitDB(cfg)
}

func runIngest(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fragments-engine ingest <run|list|validate|preview|archive-policy-set> ...")
	}
	switch args[0] {
	case "run":
		return runIngestRun(args[1:])
	case "list":
		return runIngestList(args[1:])
	case "validate":
		return runIngestValidate(args[1:])
	case "preview":
		return runIngestPreview(args[1:])
	case "archive-policy-set":
		return runIngestArchivePolicySet(args[1:])
	default:
		return fmt.Errorf("usage: fragments-engine ingest <run|list|validate|preview|archive-policy-set> ...")
	}
}

func runIngestRun(args []string) error {
	fs := flag.NewFlagSet("ingest run", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	runs, err := instance.Fragments.RunAllIngests(context.Background(), cfg)
	if err != nil {
		return err
	}
	for _, run := range runs {
		fmt.Printf("%s (%s): inserted=%d skipped=%d started=%s finished=%s\n",
			run.Name, run.Kind, run.Inserted, run.Skipped,
			run.StartedAt.Format("2006-01-02T15:04:05Z07:00"),
			run.FinishedAt.Format("2006-01-02T15:04:05Z07:00"),
		)
	}
	return nil
}

func runIngestList(args []string) error {
	fs := flag.NewFlagSet("ingest list", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	items, err := service.NewIngestAdminService(*configPath).List(context.Background())
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s kind=%s enabled=%t root=%s namespace=%s\n", item.Name, item.Kind, item.Enabled, item.SourceRoot, item.Namespace)
		if item.ArchiveRoot != "" || item.CopyTextExports || item.DeleteCopiedSource {
			fmt.Printf("  archive_root=%s copy_text_exports=%t delete_copied_source=%t\n", item.ArchiveRoot, item.CopyTextExports, item.DeleteCopiedSource)
		}
	}
	return nil
}

func runIngestValidate(args []string) error {
	fs := flag.NewFlagSet("ingest validate", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	name := fs.String("name", "", "ingest name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("ingest validate requires -name")
	}
	result, err := service.NewIngestAdminService(*configPath).Validate(context.Background(), *name)
	if err != nil {
		return err
	}
	fmt.Printf("name=%s kind=%s enabled=%t valid=%t root=%s\n", result.Name, result.Kind, result.Enabled, result.Valid, result.SourceRoot)
	if result.ArchiveRoot != "" {
		fmt.Printf("archive_root=%s\n", result.ArchiveRoot)
	}
	for _, item := range result.Errors {
		fmt.Printf("error=%s\n", item)
	}
	for _, item := range result.Warnings {
		fmt.Printf("warning=%s\n", item)
	}
	return nil
}

func runIngestPreview(args []string) error {
	fs := flag.NewFlagSet("ingest preview", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	name := fs.String("name", "", "ingest name")
	limit := fs.Int("limit", 10, "maximum preview items to print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("ingest preview requires -name")
	}
	result, err := service.NewIngestAdminService(*configPath).Preview(context.Background(), *name, *limit)
	if err != nil {
		return err
	}
	earliest := ""
	if result.EarliestAt != nil {
		earliest = result.EarliestAt.Format(time.RFC3339)
	}
	latest := ""
	if result.LatestAt != nil {
		latest = result.LatestAt.Format(time.RFC3339)
	}
	fmt.Printf("name=%s kind=%s enabled=%t count=%d earliest=%s latest=%s\n", result.Name, result.Kind, result.Enabled, result.PreviewCount, earliest, latest)
	for _, item := range result.Items {
		fmt.Printf("%s %s created_at=%s bytes=%d path=%s\n", item.SourceID, item.Title, item.CreatedAt.Format(time.RFC3339), item.ContentBytes, item.CanonicalPath)
	}
	return nil
}

func runIngestArchivePolicySet(args []string) error {
	fs := flag.NewFlagSet("ingest archive-policy-set", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	name := fs.String("name", "", "ingest name")
	archiveRoot := fs.String("archive-root", "~/Documents/corpus/ai-chat-logs/chatgpt/logs", "archive root for copied text exports")
	copyTextExports := fs.Bool("copy-text-exports", true, "copy text export files into archive_root before parsing")
	deleteCopiedSource := fs.Bool("delete-copied-source", false, "delete copied source text files after archive copy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("ingest archive-policy-set requires -name")
	}
	result, err := service.NewIngestAdminService(*configPath).UpdateArchivePolicy(context.Background(), *name, *archiveRoot, *copyTextExports, *deleteCopiedSource)
	if err != nil {
		return err
	}
	fmt.Printf("name=%s kind=%s archive_root=%s copy_text_exports=%t delete_copied_source=%t\n",
		result.Name, result.Kind, result.ArchiveRoot, result.CopyTextExports, result.DeleteCopiedSource)
	return nil
}

func runSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	query := fs.String("q", "", "full-text query")
	entityKind := fs.String("entity-kind", "", "entity kind filter")
	entityValue := fs.String("entity-value", "", "entity value filter")
	limit := fs.Int("limit", 10, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*query) == "" && (strings.TrimSpace(*entityKind) == "" || strings.TrimSpace(*entityValue) == "") {
		return fmt.Errorf("search requires -q or both -entity-kind and -entity-value")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	results, err := instance.Fragments.SearchFiltered(context.Background(), *query, *entityKind, *entityValue, *limit)
	if err != nil {
		return err
	}
	for _, result := range results {
		fmt.Printf("[%0.3f] %s (%s)\n%s\n\n", result.Score, result.Fragment.Title, result.Fragment.SourceID, result.Snippet)
	}
	return nil
}

func runFragment(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fragments-engine fragment <get|related|reanalyze-attachments|backfill-pinterest-corpus> ...")
	}
	switch args[0] {
	case "get":
		return runFragmentGet(args[1:])
	case "related":
		return runFragmentRelated(args[1:])
	case "reanalyze-attachments":
		return runFragmentReanalyzeAttachments(args[1:])
	case "backfill-pinterest-corpus":
		return runFragmentBackfillPinterestCorpus(args[1:])
	default:
		return fmt.Errorf("usage: fragments-engine fragment <get|related|reanalyze-attachments|backfill-pinterest-corpus> ...")
	}
}

func runFragmentGet(args []string) error {
	fs := flag.NewFlagSet("fragment get", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	fragmentID := fs.String("fragment-id", "", "fragment id")
	relatedLimit := fs.Int("related-limit", 10, "related fragment limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*fragmentID) == "" {
		return fmt.Errorf("fragment get requires -fragment-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	detail, err := instance.Fragments.GetDetail(context.Background(), *fragmentID, *relatedLimit)
	if err != nil {
		return err
	}
	fmt.Printf("id=%s\nsource=%s type=%s source_id=%s status=%s ingest=%s\n", detail.Fragment.ID, detail.Fragment.Source, detail.Fragment.SourceType, detail.Fragment.SourceID, detail.Fragment.Status, detail.Fragment.IngestName)
	fmt.Printf("title=%s\ncanonical_path=%s\n", detail.Fragment.Title, detail.Fragment.CanonicalPath)
	fmt.Printf("created_at=%s ingested_at=%s indexed_at=%s\n", detail.Fragment.CreatedAt.Format(time.RFC3339), detail.Fragment.IngestedAt.Format(time.RFC3339), detail.Fragment.IndexedAt.Format(time.RFC3339))
	fmt.Printf("summary=%s\n\n", detail.Fragment.Summary)
	printFragmentMetadata(detail.Fragment.MetadataJSON)
	if len(detail.Entities) > 0 {
		fmt.Println("entities:")
		for _, item := range detail.Entities {
			fmt.Printf("  %s=%s source=%s confidence=%.2f\n", item.Kind, item.Value, item.Source, item.Confidence)
		}
		fmt.Println()
	}
	if len(detail.Attachments) > 0 {
		fmt.Println("attachments:")
		for _, item := range detail.Attachments {
			fmt.Printf("  %s role=%s name=%s mime=%s source=%s\n", item.Kind, item.Role, item.Name, item.MIMEType, item.Source)
			if item.ExternalURL != "" {
				fmt.Printf("    url=%s\n", item.ExternalURL)
			}
			if item.SourcePath != "" {
				fmt.Printf("    source_path=%s\n", item.SourcePath)
			}
			if item.StoragePath != "" {
				fmt.Printf("    storage_path=%s\n", item.StoragePath)
			}
			if item.PreviewStoragePath != "" {
				fmt.Printf("    preview_storage_path=%s\n", item.PreviewStoragePath)
			}
			printAttachmentMetadata(item.MetadataJSON, item.Metadata)
		}
		fmt.Println()
	}
	if len(detail.RouteLog) > 0 {
		fmt.Println("route_log:")
		for _, item := range detail.RouteLog {
			fmt.Printf("  %d %s decision=%s route=%s destination=%s reason=%s\n",
				item.ID,
				item.CreatedAt.Format(time.RFC3339),
				item.Decision,
				item.RouteID,
				item.DestinationID,
				item.Reason,
			)
		}
		fmt.Println()
	}
	if len(detail.Relations) > 0 {
		fmt.Println("relations:")
		for _, item := range detail.Relations {
			fmt.Printf("  [%0.3f] %s %s kind=%s\n",
				item.Relation.Score,
				item.Related.ID,
				item.Related.Title,
				item.Relation.Kind,
			)
			if item.Relation.MetadataJSON != "" && item.Relation.MetadataJSON != "{}" {
				fmt.Printf("    metadata=%s\n", item.Relation.MetadataJSON)
			}
		}
		fmt.Println()
	}
	if len(detail.Related) > 0 {
		fmt.Println("related:")
		for _, item := range detail.Related {
			fmt.Printf("  [%0.3f] %s %s backend=%s strategy=%s kind=%s reason=%s\n",
				item.Score,
				item.Fragment.ID,
				item.Fragment.Title,
				item.Trace.Backend,
				item.Trace.Strategy,
				item.Trace.RelationKind,
				item.Trace.Reason,
			)
			if item.Trace.MetadataJSON != "" && item.Trace.MetadataJSON != "{}" {
				fmt.Printf("    metadata=%s\n", item.Trace.MetadataJSON)
			}
		}
	}
	return nil
}

func printFragmentMetadata(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return
	}
	if pageCount, ok := meta["page_count"]; ok {
		fmt.Printf("page_count=%v\n", pageCount)
	}
	if textPageCount, ok := meta["text_page_count"]; ok {
		fmt.Printf("text_page_count=%v\n", textPageCount)
	}
	if snippets, ok := meta["page_snippets"].([]any); ok && len(snippets) > 0 {
		fmt.Println("page_snippets:")
		for _, rawSnippet := range snippets {
			item, ok := rawSnippet.(map[string]any)
			if !ok {
				continue
			}
			fmt.Printf("  page=%v bytes=%v snippet=%v\n", item["page"], item["text_bytes"], item["snippet"])
		}
	}
	if pageCount, ok := meta["page_count"]; ok || meta["text_page_count"] != nil || meta["page_snippets"] != nil {
		_ = pageCount
		fmt.Println()
	}
}

func runFragmentRelated(args []string) error {
	fs := flag.NewFlagSet("fragment related", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	fragmentID := fs.String("fragment-id", "", "fragment id")
	limit := fs.Int("limit", 10, "related fragment limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*fragmentID) == "" {
		return fmt.Errorf("fragment related requires -fragment-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	results, err := instance.Fragments.Related(context.Background(), *fragmentID, *limit)
	if err != nil {
		return err
	}
	for _, item := range results {
		fmt.Printf("[%0.3f] %s (%s) backend=%s strategy=%s kind=%s reason=%s\n%s\n\n",
			item.Score,
			item.Fragment.Title,
			item.Fragment.SourceID,
			item.Trace.Backend,
			item.Trace.Strategy,
			item.Trace.RelationKind,
			item.Trace.Reason,
			item.Snippet,
		)
	}
	return nil
}

func runFragmentReanalyzeAttachments(args []string) error {
	fs := flag.NewFlagSet("fragment reanalyze-attachments", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	fragmentID := fs.String("fragment-id", "", "fragment id")
	attachmentID := fs.String("attachment-id", "", "optional attachment id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*fragmentID) == "" {
		return fmt.Errorf("fragment reanalyze-attachments requires -fragment-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	result, err := instance.Fragments.ReanalyzeAttachments(context.Background(), *fragmentID, *attachmentID)
	if err != nil {
		return err
	}
	fmt.Printf("fragment_id=%s provider=%s updated=%d skipped=%d\n", result.FragmentID, result.ProviderBackend, result.UpdatedCount, result.SkippedCount)
	for _, id := range result.AttachmentIDs {
		fmt.Printf("attachment_id=%s\n", id)
	}
	return nil
}

func runFragmentBackfillPinterestCorpus(args []string) error {
	fs := flag.NewFlagSet("fragment backfill-pinterest-corpus", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	limit := fs.Int("limit", 0, "maximum pinterest pin fragments to backfill; 0 = all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	result, err := instance.Fragments.BackfillPinterestCorpus(context.Background(), *limit)
	if err != nil {
		return err
	}
	fmt.Printf("scanned=%d candidates=%d written=%d\n", result.ScannedCount, result.CandidateCount, result.WrittenCount)
	for _, path := range result.WrittenPaths {
		fmt.Printf("path=%s\n", path)
	}
	return nil
}

func printAttachmentMetadata(raw string, decoded map[string]any) {
	meta := decoded
	if len(meta) == 0 {
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "{}" {
			return
		}
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			fmt.Printf("    metadata_json=%s\n", raw)
			return
		}
	}
	if extractor := stringMeta(meta, "extractor"); extractor != "" {
		fmt.Printf("    extractor=%s\n", extractor)
	}
	if title := stringMeta(meta, "extracted_title"); title != "" {
		fmt.Printf("    extracted_title=%s\n", title)
	}
	if preview := stringMeta(meta, "extracted_text_preview"); preview != "" {
		fmt.Printf("    extracted_preview=%s\n", preview)
	}
	if keys := stringSliceMeta(meta, "frontmatter_keys"); len(keys) > 0 {
		fmt.Printf("    frontmatter_keys=%s\n", strings.Join(keys, ", "))
		if frontmatter, ok := meta["frontmatter"].(map[string]any); ok {
			for _, key := range keys {
				fmt.Printf("      %s=%v\n", key, frontmatter[key])
			}
		}
	}
	if analysis, ok := meta["analysis"].(map[string]any); ok {
		if summary := stringMeta(analysis, "summary"); summary != "" {
			fmt.Printf("    analysis_summary=%s\n", summary)
		}
		if tags := stringSliceMeta(analysis, "tags"); len(tags) > 0 {
			fmt.Printf("    analysis_tags=%s\n", strings.Join(tags, ", "))
		}
	}
	if vision, ok := meta["vision_analysis"].(map[string]any); ok {
		if summary := stringMeta(vision, "summary"); summary != "" {
			fmt.Printf("    vision_summary=%s\n", summary)
		}
		if tags := stringSliceMeta(vision, "tags"); len(tags) > 0 {
			fmt.Printf("    vision_tags=%s\n", strings.Join(tags, ", "))
		}
		if entities := stringSliceMeta(vision, "entities"); len(entities) > 0 {
			fmt.Printf("    vision_entities=%s\n", strings.Join(entities, ", "))
		}
		if confidence, ok := vision["confidence"]; ok {
			fmt.Printf("    vision_confidence=%v\n", confidence)
		}
	}
	if visionErr := stringMeta(meta, "vision_analysis_error"); visionErr != "" {
		fmt.Printf("    vision_error=%s\n", visionErr)
	}
	exclude := map[string]struct{}{
		"extractor":              {},
		"extracted_title":        {},
		"extracted_text_preview": {},
		"frontmatter_keys":       {},
		"frontmatter":            {},
		"analysis":               {},
		"vision_analysis":        {},
		"vision_analysis_error":  {},
	}
	remaining := make([]string, 0, len(meta))
	for key := range meta {
		if _, skip := exclude[key]; skip {
			continue
		}
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		fmt.Printf("    %s=%v\n", key, meta[key])
	}
}

func stringMeta(meta map[string]any, key string) string {
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func stringSliceMeta(meta map[string]any, key string) []string {
	raw, ok := meta[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}

func runEntity(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fragments-engine entity <list|fragments> ...")
	}
	switch args[0] {
	case "list":
		return runEntityList(args[1:])
	case "fragments":
		return runEntityFragments(args[1:])
	default:
		return fmt.Errorf("usage: fragments-engine entity <list|fragments> ...")
	}
}

func runEntityList(args []string) error {
	fs := flag.NewFlagSet("entity list", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	kind := fs.String("kind", "", "entity kind filter")
	limit := fs.Int("limit", 50, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Fragments.ListEntities(context.Background(), *kind, *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s fragments=%d\n", item.Kind, item.Value, item.FragmentCount)
	}
	return nil
}

func runEntityFragments(args []string) error {
	fs := flag.NewFlagSet("entity fragments", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	kind := fs.String("kind", "", "entity kind")
	value := fs.String("value", "", "entity value")
	limit := fs.Int("limit", 20, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*kind) == "" || strings.TrimSpace(*value) == "" {
		return fmt.Errorf("entity fragments requires -kind and -value")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	results, err := instance.Fragments.FragmentsByEntity(context.Background(), *kind, *value, *limit)
	if err != nil {
		return err
	}
	for _, item := range results {
		fmt.Printf("[%0.2f] %s (%s)\n%s\n\n", item.Score, item.Fragment.Title, item.Fragment.SourceID, item.Snippet)
	}
	return nil
}

func runServeAPI(args []string) error {
	fs := flag.NewFlagSet("serve-api", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	addr := fs.String("addr", ":8091", "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return api.NewServer(*configPath).ListenAndServe(*addr)
}

func runRecall(args []string) error {
	if len(args) == 0 || args[0] != "status" {
		return fmt.Errorf("usage: fragments-engine recall status -config <path>")
	}
	fs := flag.NewFlagSet("recall status", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	status := instance.RecallStatus()
	fmt.Printf("backend=%s mode=%s embeddings=%t provider=%s model=%s root=%s fallback=%s\n",
		status.Backend,
		status.RecallMode,
		status.EmbeddingsEnabled,
		status.EmbeddingProvider,
		status.EmbeddingModel,
		status.VantaRoot,
		status.FallbackBackend,
	)
	return nil
}

func runInbox(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fragments-engine inbox <list|entities|entity-items|review> ...")
	}
	switch args[0] {
	case "list":
		return runInboxList(args[1:])
	case "entities":
		return runInboxEntities(args[1:])
	case "entity-items":
		return runInboxEntityItems(args[1:])
	case "review":
		return runInboxReview(args[1:])
	default:
		return fmt.Errorf("usage: fragments-engine inbox <list|entities|entity-items|review> ...")
	}
}

func runInboxList(args []string) error {
	fs := flag.NewFlagSet("inbox list", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	limit := fs.Int("limit", 20, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Inbox.List(context.Background(), *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s %s\n", item.StagedAt.Format("2006-01-02T15:04:05Z07:00"), item.FragmentID, item.Reason)
	}
	return nil
}

func runInboxEntities(args []string) error {
	fs := flag.NewFlagSet("inbox entities", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	kind := fs.String("kind", "", "entity kind filter")
	limit := fs.Int("limit", 50, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Inbox.ListEntityGroups(context.Background(), *kind, *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s fragments=%d\n", item.Kind, item.Value, item.FragmentCount)
	}
	return nil
}

func runInboxEntityItems(args []string) error {
	fs := flag.NewFlagSet("inbox entity-items", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	kind := fs.String("kind", "", "entity kind")
	value := fs.String("value", "", "entity value")
	limit := fs.Int("limit", 50, "result limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*kind) == "" || strings.TrimSpace(*value) == "" {
		return fmt.Errorf("inbox entity-items requires -kind and -value")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Inbox.ListByEntity(context.Background(), *kind, *value, *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s %s\n", item.StagedAt.Format("2006-01-02T15:04:05Z07:00"), item.FragmentID, item.Reason)
	}
	return nil
}

func runInboxReview(args []string) error {
	fs := flag.NewFlagSet("inbox review", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	limit := fs.Int("limit", 10, "maximum staged inbox items to review this run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	result, err := instance.InboxReviewer.ReviewOnce(context.Background(), *limit)
	if err != nil {
		return err
	}
	fmt.Printf("reviewed=%d updated=%d skipped=%d\n", result.ReviewedCount, result.UpdatedCount, result.SkippedCount)
	for _, item := range result.Items {
		fmt.Printf("%s updated=%t action=%s", item.FragmentID, item.Updated, item.Action)
		if item.Detail != "" {
			fmt.Printf(" detail=%s", item.Detail)
		}
		fmt.Println()
	}
	return nil
}

func runRoute(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fragments-engine route <destination-add|destination-list|destination-status|destination-rename|destination-validate|destination-delete|destination-retry-set|destination-queue-policy-set|add|rename|list|log|preview|delete|apply-entity> ...")
	}
	switch args[0] {
	case "destination-add":
		return runRouteDestinationAdd(args[1:])
	case "destination-list":
		return runRouteDestinationList(args[1:])
	case "destination-status":
		return runRouteDestinationStatus(args[1:])
	case "destination-rename":
		return runRouteDestinationRename(args[1:])
	case "destination-validate":
		return runRouteDestinationValidate(args[1:])
	case "destination-delete":
		return runRouteDestinationDelete(args[1:])
	case "destination-retry-set":
		return runRouteDestinationRetrySet(args[1:])
	case "destination-queue-policy-set":
		return runRouteDestinationQueuePolicySet(args[1:])
	case "add":
		return runRouteAdd(args[1:])
	case "rename":
		return runRouteRename(args[1:])
	case "list":
		return runRouteList(args[1:])
	case "log":
		return runRouteLog(args[1:])
	case "preview":
		return runRoutePreview(args[1:])
	case "delete":
		return runRouteDelete(args[1:])
	case "apply-entity":
		return runRouteApplyEntity(args[1:])
	default:
		return fmt.Errorf("usage: fragments-engine route <destination-add|destination-list|destination-status|destination-rename|destination-validate|destination-delete|destination-retry-set|destination-queue-policy-set|add|rename|list|log|preview|delete|apply-entity> ...")
	}
}

func runRouteDestinationAdd(args []string) error {
	fs := flag.NewFlagSet("route destination-add", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	name := fs.String("name", "", "destination name")
	kind := fs.String("kind", "", "destination kind")
	configJSON := fs.String("config-json", "{}", "destination config json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *kind == "" {
		return fmt.Errorf("route destination-add requires -name and -kind")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.AddDestination(context.Background(), domain.Destination{
		Name:       *name,
		Kind:       *kind,
		ConfigJSON: *configJSON,
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", item.ID, item.Name, item.Kind)
	return nil
}

func runRouteDestinationList(args []string) error {
	fs := flag.NewFlagSet("route destination-list", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Routing.ListDestinations(context.Background())
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s %s\n", item.ID, item.Name, item.Kind)
	}
	return nil
}

func runRouteDestinationStatus(args []string) error {
	fs := flag.NewFlagSet("route destination-status", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "optional destination id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()

	if strings.TrimSpace(*destinationID) != "" {
		item, err := instance.Routing.GetDestinationStatus(context.Background(), *destinationID)
		if err != nil {
			return err
		}
		printDestinationStatus(item)
		return nil
	}

	items, err := instance.Routing.ListDestinationStatus(context.Background())
	if err != nil {
		return err
	}
	for _, item := range items {
		printDestinationStatus(item)
		fmt.Println()
	}
	return nil
}

func runRouteDestinationRename(args []string) error {
	fs := flag.NewFlagSet("route destination-rename", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "destination id")
	name := fs.String("name", "", "new destination name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*destinationID) == "" || strings.TrimSpace(*name) == "" {
		return fmt.Errorf("route destination-rename requires -destination-id and -name")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.RenameDestination(context.Background(), *destinationID, *name)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", item.ID, item.Name, item.Kind)
	return nil
}

func runRouteDestinationValidate(args []string) error {
	fs := flag.NewFlagSet("route destination-validate", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	name := fs.String("name", "validation-target", "destination name")
	kind := fs.String("kind", "", "destination kind")
	configJSON := fs.String("config-json", "{}", "destination config json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*kind) == "" {
		return fmt.Errorf("route destination-validate requires -kind")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.ValidateDestination(context.Background(), domain.Destination{
		Name:       *name,
		Kind:       *kind,
		ConfigJSON: *configJSON,
	})
	if err != nil {
		return err
	}
	fmt.Printf("name=%s kind=%s provider=%s valid=%t reachable=%t\n", item.Destination.Name, item.Destination.Kind, item.Provider, item.ConfigValid, item.Reachable)
	if item.ConfigError != "" {
		fmt.Printf("config_error=%s\n", item.ConfigError)
	}
	if item.Reachability != "" {
		fmt.Printf("reachability=%s\n", item.Reachability)
	}
	return nil
}

func runRouteDestinationDelete(args []string) error {
	fs := flag.NewFlagSet("route destination-delete", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "destination id")
	force := fs.Bool("force", false, "delete even if routes still reference the destination")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*destinationID) == "" {
		return fmt.Errorf("route destination-delete requires -destination-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	result, err := instance.Routing.DeleteDestination(context.Background(), *destinationID, *force)
	if err != nil {
		return err
	}
	fmt.Printf("destination=%s deleted=%t force=%t routes=%s\n", result.DestinationID, result.Deleted, result.Force, strings.Join(result.RouteIDs, ","))
	return nil
}

func runRouteDestinationRetrySet(args []string) error {
	fs := flag.NewFlagSet("route destination-retry-set", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "destination id")
	maxAttempts := fs.Int("max-attempts", 0, "retry max attempts")
	backoffMS := fs.Int("backoff-ms", 0, "retry backoff in ms")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*destinationID) == "" {
		return fmt.Errorf("route destination-retry-set requires -destination-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.UpdateDestinationRetry(context.Background(), *destinationID, domain.DeliveryRetryConfig{
		MaxAttempts: *maxAttempts,
		BackoffMS:   *backoffMS,
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", item.ID, item.Name, item.Kind)
	return nil
}

func runRouteDestinationQueuePolicySet(args []string) error {
	fs := flag.NewFlagSet("route destination-queue-policy-set", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "destination id")
	replayCooldown := fs.Int("replay-cooldown-seconds", 0, "replay cooldown in seconds")
	maxReplays := fs.Int("max-replays-per-hour", 0, "max replays per hour")
	alertPending := fs.Int("alert-pending-threshold", 0, "alert threshold for pending jobs")
	alertDeadLetters := fs.Int("alert-dead-letter-threshold", 0, "alert threshold for dead letters")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*destinationID) == "" {
		return fmt.Errorf("route destination-queue-policy-set requires -destination-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.UpdateDestinationQueuePolicy(context.Background(), *destinationID, domain.QueuePolicyConfig{
		ReplayCooldownSeconds:    *replayCooldown,
		MaxReplaysPerHour:        *maxReplays,
		AlertPendingThreshold:    *alertPending,
		AlertDeadLetterThreshold: *alertDeadLetters,
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s %s %s\n", item.ID, item.Name, item.Kind)
	return nil
}

func runRouteAdd(args []string) error {
	fs := flag.NewFlagSet("route add", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	name := fs.String("name", "", "route name")
	matchSource := fs.String("match-source", "", "source match")
	matchType := fs.String("match-type", "", "source type match")
	matchEntityKind := fs.String("match-entity-kind", "", "entity kind match")
	matchEntityValue := fs.String("match-entity-value", "", "entity value match")
	destinationID := fs.String("destination-id", "", "destination id")
	autoRoute := fs.Bool("auto-route", false, "auto route when matched")
	confidenceMin := fs.Float64("confidence-min", 0, "minimum confidence")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *destinationID == "" {
		return fmt.Errorf("route add requires -name and -destination-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.AddRoute(context.Background(), domain.Route{
		Name:             *name,
		MatchSource:      *matchSource,
		MatchType:        *matchType,
		MatchEntityKind:  *matchEntityKind,
		MatchEntityValue: *matchEntityValue,
		DestinationID:    *destinationID,
		AutoRoute:        *autoRoute,
		ConfidenceMin:    *confidenceMin,
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s %s -> %s\n", item.ID, item.Name, item.DestinationID)
	return nil
}

func runRouteRename(args []string) error {
	fs := flag.NewFlagSet("route rename", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	routeID := fs.String("route-id", "", "route id")
	name := fs.String("name", "", "new route name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *routeID == "" || *name == "" {
		return fmt.Errorf("route rename requires -route-id and -name")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	item, err := instance.Routing.RenameRoute(context.Background(), *routeID, *name)
	if err != nil {
		return err
	}
	fmt.Printf("%s %s -> %s\n", item.ID, item.Name, item.DestinationID)
	return nil
}

func runRouteList(args []string) error {
	fs := flag.NewFlagSet("route list", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Routing.ListRoutes(context.Background())
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s source=%s type=%s entity_kind=%s entity_value=%s destination=%s auto=%t confidence=%.2f\n",
			item.ID, item.Name, item.MatchSource, item.MatchType, item.MatchEntityKind, item.MatchEntityValue, item.DestinationID, item.AutoRoute, item.ConfidenceMin)
	}
	return nil
}

func runRouteLog(args []string) error {
	fs := flag.NewFlagSet("route log", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	fragmentID := fs.String("fragment-id", "", "fragment id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fragmentID == "" {
		return fmt.Errorf("route log requires -fragment-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Routing.ListRouteLog(context.Background(), *fragmentID)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%d %s decision=%s route=%s destination=%s reason=%s\n",
			item.ID,
			item.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
			item.Decision,
			item.RouteID,
			item.DestinationID,
			item.Reason,
		)
	}
	return nil
}

func runRoutePreview(args []string) error {
	fs := flag.NewFlagSet("route preview", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	routeID := fs.String("route-id", "", "route id")
	limit := fs.Int("limit", 50, "maximum staged items to preview")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *routeID == "" {
		return fmt.Errorf("route preview requires -route-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	result, err := instance.Routing.PreviewRoute(context.Background(), *routeID, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("route=%s matched=%d\n", result.RouteID, result.MatchedCount)
	for _, item := range result.PreviewItems {
		fmt.Printf("%s %s source=%s type=%s reason=%s\n", item.FragmentID, item.Title, item.Source, item.SourceType, item.Reason)
	}
	return nil
}

func runRouteDelete(args []string) error {
	fs := flag.NewFlagSet("route delete", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	routeID := fs.String("route-id", "", "route id")
	force := fs.Bool("force", false, "delete even if staged or route-log references still exist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *routeID == "" {
		return fmt.Errorf("route delete requires -route-id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	result, err := instance.Routing.DeleteRoute(context.Background(), *routeID, *force)
	if err != nil {
		return err
	}
	fmt.Printf("route=%s deleted=%t force=%t staged_refs=%d route_log_refs=%d\n", result.RouteID, result.Deleted, result.Force, result.StagedRefs, result.RouteLogRefs)
	return nil
}

func runRouteApplyEntity(args []string) error {
	fs := flag.NewFlagSet("route apply-entity", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	routeID := fs.String("route-id", "", "route id")
	kind := fs.String("kind", "", "entity kind")
	value := fs.String("value", "", "entity value")
	limit := fs.Int("limit", 50, "maximum staged items to route")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *routeID == "" || *kind == "" || *value == "" {
		return fmt.Errorf("route apply-entity requires -route-id, -kind, and -value")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	result, err := instance.Routing.ApplyRouteByEntity(context.Background(), *routeID, *kind, *value, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("route=%s entity=%s:%s matched=%d routed=%d failed=%d\n",
		result.RouteID, result.EntityKind, result.EntityValue, result.MatchedCount, result.RoutedCount, result.FailedCount)
	for _, item := range result.Items {
		fmt.Printf("%s %s %s %s\n", item.FragmentID, item.Status, item.WrittenPath, item.Error)
	}
	return nil
}

func runServeMCP(args []string) error {
	fs := flag.NewFlagSet("serve-mcp", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return mcpserver.Serve(context.Background(), *configPath)
}

func runQueue(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: fragments-engine queue <status|drain|destinations|events|pending|failed|replay|purge> ...")
	}
	switch args[0] {
	case "status":
		return runQueueStatus(args[1:])
	case "drain":
		return runQueueDrain(args[1:])
	case "destinations":
		return runQueueDestinations(args[1:])
	case "events":
		return runQueueEvents(args[1:])
	case "pending":
		return runQueuePending(args[1:])
	case "failed":
		return runQueueFailed(args[1:])
	case "replay":
		return runQueueReplay(args[1:])
	case "purge":
		return runQueuePurge(args[1:])
	default:
		return fmt.Errorf("usage: fragments-engine queue <status|drain|destinations|events|pending|failed|replay|purge> ...")
	}
}

func runQueueStatus(args []string) error {
	fs := flag.NewFlagSet("queue status", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	stats, err := instance.Queue.Stats(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("pending=%d failed=%d\n", stats.Pending, stats.Failed)
	return nil
}

func runQueueDrain(args []string) error {
	fs := flag.NewFlagSet("queue drain", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	limit := fs.Int("limit", 100, "maximum queued jobs to process")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	processed, err := instance.Queue.Drain(context.Background(), *limit)
	if err != nil {
		return err
	}
	stats, err := instance.Queue.Stats(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("processed=%d pending=%d failed=%d\n", processed, stats.Pending, stats.Failed)
	return nil
}

func runQueueDestinations(args []string) error {
	fs := flag.NewFlagSet("queue destinations", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	limit := fs.Int("limit", 100, "maximum destination summaries to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Queue.ListDestinationSummaries(context.Background(), *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%s %s provider=%s valid=%t reachable=%t alert=%t reason=%s pending=%d failed=%d dead_letters=%d replays=%d purges=%d\n",
			item.DestinationID, item.DestinationName, item.Provider, item.ConfigValid, item.Reachable, item.Alert, item.AlertReason, item.PendingCount, item.FailedCount, item.DeadLetterCount, item.ReplayCount, item.PurgeCount)
		if item.LastFailureAt != nil {
			fmt.Printf("  last_failure_at=%s error=%s\n", item.LastFailureAt.Format(time.RFC3339), item.LastFailureError)
		}
	}
	return nil
}

func runQueueEvents(args []string) error {
	fs := flag.NewFlagSet("queue events", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "optional destination id filter")
	limit := fs.Int("limit", 50, "maximum queue events to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Queue.ListEvents(context.Background(), *destinationID, *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		failedJob := int64(0)
		if item.FailedJobID != nil {
			failedJob = *item.FailedJobID
		}
		fmt.Printf("%d failed_job=%d fragment=%s route=%s destination=%s event=%s created_at=%s detail=%s\n",
			item.ID, failedJob, item.FragmentID, item.RouteID, item.DestinationID, item.EventType, item.CreatedAt.Format(time.RFC3339), item.DetailJSON)
	}
	return nil
}

func runQueuePending(args []string) error {
	fs := flag.NewFlagSet("queue pending", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "optional destination id filter")
	limit := fs.Int("limit", 50, "maximum pending jobs to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Queue.ListPending(context.Background(), *destinationID, *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		reserved := ""
		if item.ReservedAt != nil {
			reserved = item.ReservedAt.Format(time.RFC3339)
		}
		fmt.Printf("%d queue=%s type=%s fragment=%s route=%s destination=%s attempts=%d max_tries=%d available_at=%s reserved_at=%s\n",
			item.ID, item.Queue, item.Type, item.FragmentID, item.RouteID, item.DestinationID, item.Attempts, item.MaxTries, item.AvailableAt.Format(time.RFC3339), reserved)
	}
	return nil
}

func runQueueFailed(args []string) error {
	fs := flag.NewFlagSet("queue failed", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	destinationID := fs.String("destination-id", "", "optional destination id filter")
	limit := fs.Int("limit", 50, "maximum failed jobs to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	items, err := instance.Queue.ListFailed(context.Background(), *destinationID, *limit)
	if err != nil {
		return err
	}
	for _, item := range items {
		fmt.Printf("%d queue=%s type=%s fragment=%s route=%s destination=%s attempts=%d failed_at=%s error=%s\n",
			item.ID, item.Queue, item.Type, item.FragmentID, item.RouteID, item.DestinationID, item.Attempts, item.FailedAt.Format(time.RFC3339), item.Error)
	}
	return nil
}

func runQueueReplay(args []string) error {
	fs := flag.NewFlagSet("queue replay", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	id := fs.Int64("id", 0, "failed queue job id")
	force := fs.Bool("force", false, "replay even if destination status is unhealthy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return fmt.Errorf("queue replay requires -id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := instance.Queue.ReplayFailed(context.Background(), *id, *force); err != nil {
		return err
	}
	stats, err := instance.Queue.Stats(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("replayed=%d pending=%d failed=%d\n", *id, stats.Pending, stats.Failed)
	return nil
}

func runQueuePurge(args []string) error {
	fs := flag.NewFlagSet("queue purge", flag.ContinueOnError)
	configPath := fs.String("config", "fragments.example.yaml", "path to config file")
	id := fs.Int64("id", 0, "failed queue job id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return fmt.Errorf("queue purge requires -id")
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	instance, err := app.Open(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer instance.Close()
	if err := instance.Queue.PurgeFailed(context.Background(), *id); err != nil {
		return err
	}
	stats, err := instance.Queue.Stats(context.Background())
	if err != nil {
		return err
	}
	fmt.Printf("purged=%d pending=%d failed=%d\n", *id, stats.Pending, stats.Failed)
	return nil
}

func usageError() error {
	return fmt.Errorf("usage: fragments-engine <init|ingest|search|fragment|entity|inbox|route|queue|recall|serve-api|serve-mcp>")
}

func printDestinationStatus(item domain.DestinationStatus) {
	fmt.Printf("%s %s %s provider=%s valid=%t reachable=%t\n",
		item.Destination.ID,
		item.Destination.Name,
		item.Destination.Kind,
		item.Provider,
		item.ConfigValid,
		item.Reachable,
	)
	if item.ConfigError != "" {
		fmt.Printf("  config_error=%s\n", item.ConfigError)
	}
	if item.Reachability != "" {
		fmt.Printf("  reachability=%s\n", item.Reachability)
	}
	fmt.Printf("  retry max_attempts=%d backoff_ms=%d\n",
		item.EffectiveRetry.MaxAttempts,
		item.EffectiveRetry.BackoffMS,
	)
	fmt.Printf("  queue_policy replay_cooldown_seconds=%d max_replays_per_hour=%d alert_pending_threshold=%d alert_dead_letter_threshold=%d\n",
		item.EffectiveQueuePolicy.ReplayCooldownSeconds,
		item.EffectiveQueuePolicy.MaxReplaysPerHour,
		item.EffectiveQueuePolicy.AlertPendingThreshold,
		item.EffectiveQueuePolicy.AlertDeadLetterThreshold,
	)
	fmt.Printf("  metrics attempts=%d success=%d failure=%d\n",
		item.Metrics.TotalAttempts,
		item.Metrics.SuccessCount,
		item.Metrics.FailureCount,
	)
	if item.LastAttempt != nil {
		fmt.Printf("  last_attempt=%s success=%t attempts=%d fragment=%s route=%s decision=%s\n",
			item.LastAttempt.CreatedAt.Format(time.RFC3339),
			item.LastAttempt.Success,
			item.LastAttempt.Attempts,
			item.LastAttempt.FragmentID,
			item.LastAttempt.RouteID,
			item.LastAttempt.Decision,
		)
		if item.LastAttempt.Ref != "" {
			fmt.Printf("  last_attempt_ref=%s\n", truncateForCLI(item.LastAttempt.Ref, 180))
		}
		if item.LastAttempt.Error != "" {
			fmt.Printf("  last_attempt_error=%s\n", item.LastAttempt.Error)
		}
	}
	if item.LastSuccess != nil {
		fmt.Printf("  last_success=%s attempts=%d fragment=%s route=%s\n",
			item.LastSuccess.CreatedAt.Format(time.RFC3339),
			item.LastSuccess.Attempts,
			item.LastSuccess.FragmentID,
			item.LastSuccess.RouteID,
		)
	}
	if item.LastFailure != nil {
		fmt.Printf("  last_failure=%s attempts=%d fragment=%s route=%s error=%s\n",
			item.LastFailure.CreatedAt.Format(time.RFC3339),
			item.LastFailure.Attempts,
			item.LastFailure.FragmentID,
			item.LastFailure.RouteID,
			item.LastFailure.Error,
		)
	}
}

func truncateForCLI(v string, limit int) string {
	if limit <= 0 || len(v) <= limit {
		return v
	}
	return v[:limit] + "..."
}
