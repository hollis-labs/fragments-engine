package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/service"
	gomcpserver "github.com/hollis-labs/go-mcp/server"
)

func Serve(ctx context.Context, cfgPath string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go app.RunQueueDrainer(ctx, cfgPath)

	s := gomcpserver.NewServer("fragments-engine", "0.1.0")

	s.RegisterTool(gomcpserver.Tool{
		Name:         "list_ingests",
		Description:  "List configured FE ingest definitions and chat-archive policies.",
		InputSchema:  gomcpserver.EmptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			return service.NewIngestAdminService(cfgPath).List(ctx)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "run_ingests",
		Description: "Run enabled ingest pipelines from the Fragments Engine config.",
		InputSchema: gomcpserver.EmptyObjectSchema(),
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.RunAllIngests(ctx, cfg)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "validate_ingest",
		Description: "Validate a configured FE ingest without writing to the database.",
		InputSchema: inputSchema(
			strProp("name", "Ingest name.", true),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := requiredString(args, "name")
			if err != nil {
				return nil, err
			}
			return service.NewIngestAdminService(cfgPath).Validate(ctx, name)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "preview_ingest",
		Description: "Preview FE ingest items without writing to the database or copying source files.",
		InputSchema: inputSchema(
			strProp("name", "Ingest name.", true),
			numProp("limit", "Maximum preview items to include. Defaults to 10.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := requiredString(args, "name")
			if err != nil {
				return nil, err
			}
			limit := int(argFloat(args, "limit", 10))
			if limit <= 0 {
				limit = 10
			}
			return service.NewIngestAdminService(cfgPath).Preview(ctx, name, limit)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "set_ingest_archive_policy",
		Description: "Update the chat-history archive/copy policy for a configured ingest.",
		InputSchema: inputSchema(
			strProp("name", "Ingest name.", true),
			strProp("archive_root", "Archive root for copied text exports.", true),
			boolProp("copy_text_exports", "Copy text export files into archive_root before parsing.", false),
			boolProp("delete_copied_source", "Delete copied source text files after archive copy.", false),
		),
		IdempotentHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			name, err := requiredString(args, "name")
			if err != nil {
				return nil, err
			}
			archiveRoot, err := requiredString(args, "archive_root")
			if err != nil {
				return nil, err
			}
			return service.NewIngestAdminService(cfgPath).UpdateArchivePolicy(
				ctx,
				name,
				archiveRoot,
				argBool(args, "copy_text_exports", true),
				argBool(args, "delete_copied_source", false),
			)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "write_doc",
		Description: "Write a finished document — an ADR, report, procedure, architecture note, or similar — into Fragments Engine for Chrispian to triage: keep, move, edit, delete, or route further, on his own schedule. Use once a document like that is done, not for code or task-tracking work, and not for durable factual/decision knowledge, which belongs in Tesseract's capture-* skills instead.",
		InputSchema: inputSchema(
			strProp("file_path", "Path to a file already written to disk (preferred). FE reads its content for review; the file itself is left exactly where it is. Use this or content, not both.", false),
			strProp("content", "The document content directly, when there's no local file yet. A leading YAML frontmatter block (title/tags/doc_type/publication_path/notify_now) is parsed server-side and merged into the fields below.", false),
			strProp("title", "Optional title override; otherwise taken from frontmatter or derived from content.", false),
			strProp("doc_type", "What kind of document this is, e.g. \"adr\", \"report\", \"procedure\", \"architecture\", \"investigation\", \"note\". Free text, not a fixed list -- becomes part of the corpus path and is available to route matching.", false),
			arrProp("tags", "Tags this document should carry, merged with any frontmatter tags and inline #hashtags. These decide routing: a tag with no configured route just sits in FE's own review queue, unnotified -- check `list_routes` before inventing a new one; see the write-doc skill for how to observe real conventions.", false),
			strProp("publication_path", "Where this should ultimately live once Chrispian processes it -- a repo path, a docs/ location. Not auto-written there; visible to him at review time.", false),
			boolProp("notify_now", "Force this into Chrispian's Tangent review right now, regardless of what routes its tags/doc_type would otherwise match. Offer this explicitly when producing a document he'd want to see immediately, rather than defaulting it either way.", false),
			strProp("source", "Fragment source. Defaults to \"agent\" -- this tool exists for agent callers.", false),
		),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			filePath := strings.TrimSpace(argString(args, "file_path", ""))
			content := argString(args, "content", "")
			if filePath == "" && strings.TrimSpace(content) == "" {
				return nil, fmt.Errorf("write_doc: one of file_path or content is required")
			}
			if filePath != "" {
				raw, err := os.ReadFile(filePath)
				if err != nil {
					return nil, fmt.Errorf("write_doc: read file_path: %w", err)
				}
				content = string(raw)
			}
			source := argString(args, "source", "agent")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			result, err := instance.Fragments.Intake(ctx, service.IntakeRequest{
				Content:         content,
				Title:           argString(args, "title", ""),
				SourceType:      argString(args, "doc_type", ""),
				Tags:            argStringSlice(args, "tags"),
				Source:          source,
				SourceFilePath:  filePath,
				PublicationPath: argString(args, "publication_path", ""),
				NotifyNow:       argBool(args, "notify_now", false),
			})
			if err != nil {
				return nil, fmt.Errorf("submit fragment: %w", err)
			}
			return result, nil
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "search_fragments",
		Description: "Search stored fragments with the active FE recall backend.",
		InputSchema: inputSchema(
			strProp("query", "Optional full-text query.", false),
			strProp("entity_kind", "Optional entity kind filter.", false),
			strProp("entity_value", "Optional entity value filter.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			query := argString(args, "query", "")
			entityKind := argString(args, "entity_kind", "")
			entityValue := argString(args, "entity_value", "")
			if query == "" && (entityKind == "" || entityValue == "") {
				return nil, fmt.Errorf("search requires query or both entity_kind and entity_value")
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.SearchFiltered(ctx, query, entityKind, entityValue, 10)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "get_fragment_detail",
		Description: "Get FE fragment detail including route log and related fragments.",
		InputSchema: inputSchema(
			strProp("fragment_id", "Fragment id to inspect.", true),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			fragmentID, err := requiredString(args, "fragment_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.GetDetail(ctx, fragmentID, 10)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "reanalyze_fragment_attachments",
		Description: "Rerun FE attachment analysis for a stored fragment without full reingest.",
		InputSchema: inputSchema(
			strProp("fragment_id", "Fragment id to reanalyze.", true),
			strProp("attachment_id", "Optional specific attachment id.", false),
		),
		IdempotentHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			fragmentID, err := requiredString(args, "fragment_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.ReanalyzeAttachments(ctx, fragmentID, argString(args, "attachment_id", ""))
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_related_fragments",
		Description: "List related fragments for a fragment id.",
		InputSchema: inputSchema(
			strProp("fragment_id", "Fragment id to inspect.", true),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			fragmentID, err := requiredString(args, "fragment_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.Related(ctx, fragmentID, 10)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_entities",
		Description: "List persisted FE entities.",
		InputSchema: inputSchema(
			strProp("kind", "Optional entity kind filter.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.ListEntities(ctx, argString(args, "kind", ""), 50)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_entity_fragments",
		Description: "List fragments tagged with a persisted FE entity.",
		InputSchema: inputSchema(
			strProp("kind", "Entity kind.", true),
			strProp("value", "Entity value.", true),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			kind, err := requiredString(args, "kind")
			if err != nil {
				return nil, err
			}
			value, err := requiredString(args, "value")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Fragments.FragmentsByEntity(ctx, kind, value, 20)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:         "get_recall_status",
		Description:  "Get the active FE recall backend and embedding status.",
		InputSchema:  gomcpserver.EmptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.RecallStatus(), nil
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:         "list_inbox",
		Description:  "List staged inbox items awaiting manual routing.",
		InputSchema:  gomcpserver.EmptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Inbox.List(ctx, 50)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:         "get_queue_status",
		Description:  "Get FE external delivery queue status.",
		InputSchema:  gomcpserver.EmptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Queue.Stats(ctx)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_queue_destinations",
		Description: "List queue health and counters grouped by external destination.",
		InputSchema: inputSchema(
			numProp("limit", "Maximum destination summaries to list. Defaults to 100.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			limit := 100
			if rawLimit := argFloat(args, "limit", 100); rawLimit > 0 {
				limit = int(rawLimit)
			}
			return instance.Queue.ListDestinationSummaries(ctx, limit)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_queue_events",
		Description: "List FE queue audit events, optionally filtered by destination.",
		InputSchema: inputSchema(
			strProp("destination_id", "Optional destination id filter.", false),
			numProp("limit", "Maximum queue events to list. Defaults to 50.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			limit := 50
			if rawLimit := argFloat(args, "limit", 50); rawLimit > 0 {
				limit = int(rawLimit)
			}
			return instance.Queue.ListEvents(ctx, argString(args, "destination_id", ""), limit)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_pending_queue_jobs",
		Description: "List pending FE external delivery queue jobs.",
		InputSchema: inputSchema(
			strProp("destination_id", "Optional destination id filter.", false),
			numProp("limit", "Maximum pending jobs to list. Defaults to 50.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			limit := 50
			if rawLimit := argFloat(args, "limit", 50); rawLimit > 0 {
				limit = int(rawLimit)
			}
			return instance.Queue.ListPending(ctx, argString(args, "destination_id", ""), limit)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "drain_queue",
		Description: "Drain FE external delivery queue jobs.",
		InputSchema: inputSchema(
			numProp("limit", "Maximum queued jobs to process. Defaults to 100.", false),
		),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			limit := 100
			if rawLimit := argFloat(args, "limit", 100); rawLimit > 0 {
				limit = int(rawLimit)
			}
			processed, err := instance.Queue.Drain(ctx, limit)
			if err != nil {
				return nil, fmt.Errorf("drain queue: %w", err)
			}
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return nil, fmt.Errorf("queue status: %w", err)
			}
			return map[string]any{"processed": processed, "stats": stats}, nil
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_failed_queue_jobs",
		Description: "List failed FE external delivery queue jobs.",
		InputSchema: inputSchema(
			strProp("destination_id", "Optional destination id filter.", false),
			numProp("limit", "Maximum failed jobs to list. Defaults to 50.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			limit := 50
			if rawLimit := argFloat(args, "limit", 50); rawLimit > 0 {
				limit = int(rawLimit)
			}
			return instance.Queue.ListFailed(ctx, argString(args, "destination_id", ""), limit)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "replay_failed_queue_job",
		Description: "Replay a failed FE external delivery queue job back into the live queue.",
		InputSchema: inputSchema(
			numProp("id", "Failed queue job id.", true),
			boolProp("force", "Replay even if destination status is unhealthy.", false),
		),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			id := int64(argFloat(args, "id", 0))
			if id <= 0 {
				return nil, fmt.Errorf("missing id")
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			if err := instance.Queue.ReplayFailed(ctx, id, argBool(args, "force", false)); err != nil {
				return nil, fmt.Errorf("replay failed queue job: %w", err)
			}
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return nil, fmt.Errorf("queue status: %w", err)
			}
			return map[string]any{"replayed": id, "stats": stats}, nil
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "purge_failed_queue_job",
		Description: "Purge a failed FE external delivery queue job without replaying it.",
		InputSchema: inputSchema(
			numProp("id", "Failed queue job id.", true),
		),
		DestructiveHint: true,
		IdempotentHint:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			id := int64(argFloat(args, "id", 0))
			if id <= 0 {
				return nil, fmt.Errorf("missing id")
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			if err := instance.Queue.PurgeFailed(ctx, id); err != nil {
				return nil, fmt.Errorf("purge failed queue job: %w", err)
			}
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return nil, fmt.Errorf("queue status: %w", err)
			}
			return map[string]any{"purged": id, "stats": stats}, nil
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_inbox_entities",
		Description: "List persisted entity groups across staged inbox items.",
		InputSchema: inputSchema(
			strProp("kind", "Optional entity kind filter.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Inbox.ListEntityGroups(ctx, argString(args, "kind", ""), 50)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "list_inbox_items_by_entity",
		Description: "List staged inbox items linked to a persisted entity.",
		InputSchema: inputSchema(
			strProp("kind", "Entity kind.", true),
			strProp("value", "Entity value.", true),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			kind, err := requiredString(args, "kind")
			if err != nil {
				return nil, err
			}
			value, err := requiredString(args, "value")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Inbox.ListByEntity(ctx, kind, value, 50)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:         "list_destinations",
		Description:  "List configured routing destinations.",
		InputSchema:  gomcpserver.EmptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.ListDestinations(ctx)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "get_destination_status",
		Description: "Get FE destination status including config validity, reachability, and last delivery state.",
		InputSchema: inputSchema(
			strProp("destination_id", "Optional destination id. Omit to list all destination statuses.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			destinationID := argString(args, "destination_id", "")
			if destinationID != "" {
				return instance.Routing.GetDestinationStatus(ctx, destinationID)
			}
			return instance.Routing.ListDestinationStatus(ctx)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "validate_destination",
		Description: "Validate a destination config without persisting it.",
		InputSchema: inputSchema(
			strProp("name", "Optional destination name.", false),
			strProp("kind", "Destination kind.", true),
			strProp("config_json", "Destination config JSON.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			kind, err := requiredString(args, "kind")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.ValidateDestination(ctx, domain.Destination{
				Name:       argString(args, "name", "validation-target"),
				Kind:       kind,
				ConfigJSON: argString(args, "config_json", "{}"),
			})
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "rename_destination",
		Description: "Rename a persisted destination.",
		InputSchema: inputSchema(
			strProp("destination_id", "Destination id.", true),
			strProp("name", "New destination name.", true),
		),
		IdempotentHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			destinationID, err := requiredString(args, "destination_id")
			if err != nil {
				return nil, err
			}
			name, err := requiredString(args, "name")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.RenameDestination(ctx, destinationID, name)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "delete_destination",
		Description: "Delete a persisted destination, optionally forcing route removal.",
		InputSchema: inputSchema(
			strProp("destination_id", "Destination id.", true),
			boolProp("force", "Delete even if routes still reference the destination.", false),
		),
		DestructiveHint: true,
		IdempotentHint:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			destinationID, err := requiredString(args, "destination_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.DeleteDestination(ctx, destinationID, argBool(args, "force", false))
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "set_destination_retry_policy",
		Description: "Set the stored retry policy override for a destination.",
		InputSchema: inputSchema(
			strProp("destination_id", "Destination id.", true),
			numProp("max_attempts", "Retry max attempts.", false),
			numProp("backoff_ms", "Retry backoff in milliseconds.", false),
		),
		IdempotentHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			destinationID, err := requiredString(args, "destination_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.UpdateDestinationRetry(ctx, destinationID, domain.DeliveryRetryConfig{
				MaxAttempts: int(argFloat(args, "max_attempts", 0)),
				BackoffMS:   int(argFloat(args, "backoff_ms", 0)),
			})
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "set_destination_queue_policy",
		Description: "Set the stored queue-policy override for a destination.",
		InputSchema: inputSchema(
			strProp("destination_id", "Destination id.", true),
			numProp("replay_cooldown_seconds", "Replay cooldown in seconds.", false),
			numProp("max_replays_per_hour", "Maximum replays per hour.", false),
			numProp("alert_pending_threshold", "Pending-job alert threshold.", false),
			numProp("alert_dead_letter_threshold", "Dead-letter alert threshold.", false),
		),
		IdempotentHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			destinationID, err := requiredString(args, "destination_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.UpdateDestinationQueuePolicy(ctx, destinationID, domain.QueuePolicyConfig{
				ReplayCooldownSeconds:    int(argFloat(args, "replay_cooldown_seconds", 0)),
				MaxReplaysPerHour:        int(argFloat(args, "max_replays_per_hour", 0)),
				AlertPendingThreshold:    int(argFloat(args, "alert_pending_threshold", 0)),
				AlertDeadLetterThreshold: int(argFloat(args, "alert_dead_letter_threshold", 0)),
			})
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:         "list_routes",
		Description:  "List configured deterministic routes.",
		InputSchema:  gomcpserver.EmptyObjectSchema(),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, _ map[string]any) (any, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.ListRoutes(ctx)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "rename_route",
		Description: "Rename a configured route.",
		InputSchema: inputSchema(
			strProp("route_id", "Route id.", true),
			strProp("name", "New route name.", true),
		),
		IdempotentHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			routeID, err := requiredString(args, "route_id")
			if err != nil {
				return nil, err
			}
			name, err := requiredString(args, "name")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.RenameRoute(ctx, routeID, name)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "preview_route",
		Description: "Preview which staged inbox items a route would currently match.",
		InputSchema: inputSchema(
			strProp("route_id", "Route id.", true),
			numProp("limit", "Maximum staged items to inspect.", false),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			routeID, err := requiredString(args, "route_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.PreviewRoute(ctx, routeID, int(argFloat(args, "limit", 50)))
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "delete_route",
		Description: "Delete a route, optionally forcing reference cleanup.",
		InputSchema: inputSchema(
			strProp("route_id", "Route id.", true),
			boolProp("force", "Delete even if staged or route-log references still exist.", false),
		),
		DestructiveHint: true,
		IdempotentHint:  true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			routeID, err := requiredString(args, "route_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.DeleteRoute(ctx, routeID, argBool(args, "force", false))
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "get_route_log",
		Description: "Get route-log entries for a fragment id.",
		InputSchema: inputSchema(
			strProp("fragment_id", "Fragment id to inspect.", true),
		),
		ReadOnlyHint: true,
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			fragmentID, err := requiredString(args, "fragment_id")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.ListRouteLog(ctx, fragmentID)
		},
	})

	s.RegisterTool(gomcpserver.Tool{
		Name:        "apply_route_by_entity",
		Description: "Apply a route to staged inbox items matching a persisted entity.",
		InputSchema: inputSchema(
			strProp("route_id", "Route id to apply.", true),
			strProp("kind", "Entity kind.", true),
			strProp("value", "Entity value.", true),
		),
		Handler: func(ctx context.Context, args map[string]any) (any, error) {
			routeID, err := requiredString(args, "route_id")
			if err != nil {
				return nil, err
			}
			kind, err := requiredString(args, "kind")
			if err != nil {
				return nil, err
			}
			value, err := requiredString(args, "value")
			if err != nil {
				return nil, err
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return nil, err
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return nil, err
			}
			defer instance.Close()
			return instance.Routing.ApplyRouteByEntity(ctx, routeID, kind, value, 50)
		},
	})

	return s.Run(ctx)
}
