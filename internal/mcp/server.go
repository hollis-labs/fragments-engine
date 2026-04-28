package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hollis-labs/fragments-engine/internal/app"
	"github.com/hollis-labs/fragments-engine/internal/config"
	"github.com/hollis-labs/fragments-engine/internal/domain"
	"github.com/hollis-labs/fragments-engine/internal/service"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func Serve(ctx context.Context, cfgPath string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go app.RunQueueDrainer(ctx, cfgPath)

	s := server.NewMCPServer("fragments-engine", "0.1.0")

	s.AddTool(
		mcp.NewTool("list_ingests",
			mcp.WithDescription("List configured FE ingest definitions and chat-archive policies."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			items, err := service.NewIngestAdminService(cfgPath).List(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("run_ingests",
			mcp.WithDescription("Run enabled ingest pipelines from the Fragments Engine config."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()

			runs, err := instance.Fragments.RunAllIngests(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(runs, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("validate_ingest",
			mcp.WithDescription("Validate a configured FE ingest without writing to the database."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Ingest name.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, err := service.NewIngestAdminService(cfgPath).Validate(ctx, name)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("preview_ingest",
			mcp.WithDescription("Preview FE ingest items without writing to the database or copying source files."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Ingest name.")),
			mcp.WithNumber("limit", mcp.Description("Maximum preview items to include. Defaults to 10.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := int(req.GetFloat("limit", 10))
			if limit <= 0 {
				limit = 10
			}
			result, err := service.NewIngestAdminService(cfgPath).Preview(ctx, name, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("set_ingest_archive_policy",
			mcp.WithDescription("Update the chat-history archive/copy policy for a configured ingest."),
			mcp.WithString("name", mcp.Required(), mcp.Description("Ingest name.")),
			mcp.WithString("archive_root", mcp.Required(), mcp.Description("Archive root for copied text exports.")),
			mcp.WithBoolean("copy_text_exports", mcp.Description("Copy text export files into archive_root before parsing.")),
			mcp.WithBoolean("delete_copied_source", mcp.Description("Delete copied source text files after archive copy.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			archiveRoot, err := req.RequireString("archive_root")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, err := service.NewIngestAdminService(cfgPath).UpdateArchivePolicy(
				ctx,
				name,
				archiveRoot,
				req.GetBool("copy_text_exports", true),
				req.GetBool("delete_copied_source", false),
			)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("search_fragments",
			mcp.WithDescription("Search stored fragments with the active FE recall backend."),
			mcp.WithString("query", mcp.Description("Optional full-text query.")),
			mcp.WithString("entity_kind", mcp.Description("Optional entity kind filter.")),
			mcp.WithString("entity_value", mcp.Description("Optional entity value filter.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query := req.GetString("query", "")
			entityKind := req.GetString("entity_kind", "")
			entityValue := req.GetString("entity_value", "")
			if query == "" && (entityKind == "" || entityValue == "") {
				return mcp.NewToolResultError("search requires query or both entity_kind and entity_value"), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			results, err := instance.Fragments.SearchFiltered(ctx, query, entityKind, entityValue, 10)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("search: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(results, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_fragment_detail",
			mcp.WithDescription("Get FE fragment detail including route log and related fragments."),
			mcp.WithString("fragment_id", mcp.Required(), mcp.Description("Fragment id to inspect.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fragmentID, err := req.RequireString("fragment_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			detail, err := instance.Fragments.GetDetail(ctx, fragmentID, 10)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("fragment detail: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(detail, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("reanalyze_fragment_attachments",
			mcp.WithDescription("Rerun FE attachment analysis for a stored fragment without full reingest."),
			mcp.WithString("fragment_id", mcp.Required(), mcp.Description("Fragment id to reanalyze.")),
			mcp.WithString("attachment_id", mcp.Description("Optional specific attachment id.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fragmentID, err := req.RequireString("fragment_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			result, err := instance.Fragments.ReanalyzeAttachments(ctx, fragmentID, req.GetString("attachment_id", ""))
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("reanalyze attachments: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_related_fragments",
			mcp.WithDescription("List related fragments for a fragment id."),
			mcp.WithString("fragment_id", mcp.Required(), mcp.Description("Fragment id to inspect.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fragmentID, err := req.RequireString("fragment_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			results, err := instance.Fragments.Related(ctx, fragmentID, 10)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("related fragments: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(results, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_entities",
			mcp.WithDescription("List persisted FE entities."),
			mcp.WithString("kind", mcp.Description("Optional entity kind filter.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind := req.GetString("kind", "")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Fragments.ListEntities(ctx, kind, 50)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list entities: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_entity_fragments",
			mcp.WithDescription("List fragments tagged with a persisted FE entity."),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Entity kind.")),
			mcp.WithString("value", mcp.Required(), mcp.Description("Entity value.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind, err := req.RequireString("kind")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			value, err := req.RequireString("value")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			results, err := instance.Fragments.FragmentsByEntity(ctx, kind, value, 20)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("entity fragments: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(results, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_recall_status",
			mcp.WithDescription("Get the active FE recall backend and embedding status."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			raw, _ := json.MarshalIndent(instance.RecallStatus(), "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_inbox",
			mcp.WithDescription("List staged inbox items awaiting manual routing."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Inbox.List(ctx, 50)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_queue_status",
			mcp.WithDescription("Get FE external delivery queue status."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("queue status: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(stats, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_queue_destinations",
			mcp.WithDescription("List queue health and counters grouped by external destination."),
			mcp.WithNumber("limit", mcp.Description("Maximum destination summaries to list. Defaults to 100.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			limit := 100
			if rawLimit := req.GetFloat("limit", 100); rawLimit > 0 {
				limit = int(rawLimit)
			}
			items, err := instance.Queue.ListDestinationSummaries(ctx, limit)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list queue destinations: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_queue_events",
			mcp.WithDescription("List FE queue audit events, optionally filtered by destination."),
			mcp.WithString("destination_id", mcp.Description("Optional destination id filter.")),
			mcp.WithNumber("limit", mcp.Description("Maximum queue events to list. Defaults to 50.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			limit := 50
			if rawLimit := req.GetFloat("limit", 50); rawLimit > 0 {
				limit = int(rawLimit)
			}
			items, err := instance.Queue.ListEvents(ctx, req.GetString("destination_id", ""), limit)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list queue events: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_pending_queue_jobs",
			mcp.WithDescription("List pending FE external delivery queue jobs."),
			mcp.WithString("destination_id", mcp.Description("Optional destination id filter.")),
			mcp.WithNumber("limit", mcp.Description("Maximum pending jobs to list. Defaults to 50.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			limit := 50
			if rawLimit := req.GetFloat("limit", 50); rawLimit > 0 {
				limit = int(rawLimit)
			}
			items, err := instance.Queue.ListPending(ctx, req.GetString("destination_id", ""), limit)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list pending queue jobs: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("drain_queue",
			mcp.WithDescription("Drain FE external delivery queue jobs."),
			mcp.WithNumber("limit", mcp.Description("Maximum queued jobs to process. Defaults to 100.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			limit := 100
			if rawLimit := req.GetFloat("limit", 100); rawLimit > 0 {
				limit = int(rawLimit)
			}
			processed, err := instance.Queue.Drain(ctx, limit)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("drain queue: %v", err)), nil
			}
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("queue status: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(map[string]any{
				"processed": processed,
				"stats":     stats,
			}, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_failed_queue_jobs",
			mcp.WithDescription("List failed FE external delivery queue jobs."),
			mcp.WithString("destination_id", mcp.Description("Optional destination id filter.")),
			mcp.WithNumber("limit", mcp.Description("Maximum failed jobs to list. Defaults to 50.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			limit := 50
			if rawLimit := req.GetFloat("limit", 50); rawLimit > 0 {
				limit = int(rawLimit)
			}
			items, err := instance.Queue.ListFailed(ctx, req.GetString("destination_id", ""), limit)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("list failed queue jobs: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("replay_failed_queue_job",
			mcp.WithDescription("Replay a failed FE external delivery queue job back into the live queue."),
			mcp.WithNumber("id", mcp.Required(), mcp.Description("Failed queue job id.")),
			mcp.WithBoolean("force", mcp.Description("Replay even if destination status is unhealthy.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := int64(req.GetFloat("id", 0))
			if id <= 0 {
				return mcp.NewToolResultError("missing id"), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			if err := instance.Queue.ReplayFailed(ctx, id, req.GetBool("force", false)); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("replay failed queue job: %v", err)), nil
			}
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("queue status: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(map[string]any{"replayed": id, "stats": stats}, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("purge_failed_queue_job",
			mcp.WithDescription("Purge a failed FE external delivery queue job without replaying it."),
			mcp.WithNumber("id", mcp.Required(), mcp.Description("Failed queue job id.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id := int64(req.GetFloat("id", 0))
			if id <= 0 {
				return mcp.NewToolResultError("missing id"), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			if err := instance.Queue.PurgeFailed(ctx, id); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("purge failed queue job: %v", err)), nil
			}
			stats, err := instance.Queue.Stats(ctx)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("queue status: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(map[string]any{"purged": id, "stats": stats}, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_inbox_entities",
			mcp.WithDescription("List persisted entity groups across staged inbox items."),
			mcp.WithString("kind", mcp.Description("Optional entity kind filter.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind := req.GetString("kind", "")
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Inbox.ListEntityGroups(ctx, kind, 50)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("inbox entities: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_inbox_items_by_entity",
			mcp.WithDescription("List staged inbox items linked to a persisted entity."),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Entity kind.")),
			mcp.WithString("value", mcp.Required(), mcp.Description("Entity value.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind, err := req.RequireString("kind")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			value, err := req.RequireString("value")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Inbox.ListByEntity(ctx, kind, value, 50)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("inbox items by entity: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_destinations",
			mcp.WithDescription("List configured routing destinations."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Routing.ListDestinations(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_destination_status",
			mcp.WithDescription("Get FE destination status including config validity, reachability, and last delivery state."),
			mcp.WithString("destination_id", mcp.Description("Optional destination id. Omit to list all destination statuses.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			destinationID := req.GetString("destination_id", "")
			if destinationID != "" {
				item, err := instance.Routing.GetDestinationStatus(ctx, destinationID)
				if err != nil {
					return mcp.NewToolResultError(fmt.Sprintf("destination status: %v", err)), nil
				}
				raw, _ := json.MarshalIndent(item, "", "  ")
				return mcp.NewToolResultText(string(raw)), nil
			}
			items, err := instance.Routing.ListDestinationStatus(ctx)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("destination status: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("validate_destination",
			mcp.WithDescription("Validate a destination config without persisting it."),
			mcp.WithString("name", mcp.Description("Optional destination name.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Destination kind.")),
			mcp.WithString("config_json", mcp.Description("Destination config JSON.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			kind, err := req.RequireString("kind")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.ValidateDestination(ctx, domain.Destination{
				Name:       req.GetString("name", "validation-target"),
				Kind:       kind,
				ConfigJSON: req.GetString("config_json", "{}"),
			})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("validate destination: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("rename_destination",
			mcp.WithDescription("Rename a persisted destination."),
			mcp.WithString("destination_id", mcp.Required(), mcp.Description("Destination id.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("New destination name.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			destinationID, err := req.RequireString("destination_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.RenameDestination(ctx, destinationID, name)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("rename destination: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("delete_destination",
			mcp.WithDescription("Delete a persisted destination, optionally forcing route removal."),
			mcp.WithString("destination_id", mcp.Required(), mcp.Description("Destination id.")),
			mcp.WithBoolean("force", mcp.Description("Delete even if routes still reference the destination.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			destinationID, err := req.RequireString("destination_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.DeleteDestination(ctx, destinationID, req.GetBool("force", false))
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("delete destination: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("set_destination_retry_policy",
			mcp.WithDescription("Set the stored retry policy override for a destination."),
			mcp.WithString("destination_id", mcp.Required(), mcp.Description("Destination id.")),
			mcp.WithNumber("max_attempts", mcp.Description("Retry max attempts.")),
			mcp.WithNumber("backoff_ms", mcp.Description("Retry backoff in milliseconds.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			destinationID, err := req.RequireString("destination_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.UpdateDestinationRetry(ctx, destinationID, domain.DeliveryRetryConfig{
				MaxAttempts: int(req.GetFloat("max_attempts", 0)),
				BackoffMS:   int(req.GetFloat("backoff_ms", 0)),
			})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("set destination retry policy: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("set_destination_queue_policy",
			mcp.WithDescription("Set the stored queue-policy override for a destination."),
			mcp.WithString("destination_id", mcp.Required(), mcp.Description("Destination id.")),
			mcp.WithNumber("replay_cooldown_seconds", mcp.Description("Replay cooldown in seconds.")),
			mcp.WithNumber("max_replays_per_hour", mcp.Description("Maximum replays per hour.")),
			mcp.WithNumber("alert_pending_threshold", mcp.Description("Pending-job alert threshold.")),
			mcp.WithNumber("alert_dead_letter_threshold", mcp.Description("Dead-letter alert threshold.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			destinationID, err := req.RequireString("destination_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.UpdateDestinationQueuePolicy(ctx, destinationID, domain.QueuePolicyConfig{
				ReplayCooldownSeconds:    int(req.GetFloat("replay_cooldown_seconds", 0)),
				MaxReplaysPerHour:        int(req.GetFloat("max_replays_per_hour", 0)),
				AlertPendingThreshold:    int(req.GetFloat("alert_pending_threshold", 0)),
				AlertDeadLetterThreshold: int(req.GetFloat("alert_dead_letter_threshold", 0)),
			})
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("set destination queue policy: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("list_routes",
			mcp.WithDescription("List configured deterministic routes."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Routing.ListRoutes(ctx)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("rename_route",
			mcp.WithDescription("Rename a configured route."),
			mcp.WithString("route_id", mcp.Required(), mcp.Description("Route id.")),
			mcp.WithString("name", mcp.Required(), mcp.Description("New route name.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			routeID, err := req.RequireString("route_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			name, err := req.RequireString("name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.RenameRoute(ctx, routeID, name)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("rename route: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("preview_route",
			mcp.WithDescription("Preview which staged inbox items a route would currently match."),
			mcp.WithString("route_id", mcp.Required(), mcp.Description("Route id.")),
			mcp.WithNumber("limit", mcp.Description("Maximum staged items to inspect.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			routeID, err := req.RequireString("route_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.PreviewRoute(ctx, routeID, int(req.GetFloat("limit", 50)))
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("preview route: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("delete_route",
			mcp.WithDescription("Delete a route, optionally forcing reference cleanup."),
			mcp.WithString("route_id", mcp.Required(), mcp.Description("Route id.")),
			mcp.WithBoolean("force", mcp.Description("Delete even if staged or route-log references still exist.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			routeID, err := req.RequireString("route_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			item, err := instance.Routing.DeleteRoute(ctx, routeID, req.GetBool("force", false))
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("delete route: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(item, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_route_log",
			mcp.WithDescription("Get route-log entries for a fragment id."),
			mcp.WithString("fragment_id", mcp.Required(), mcp.Description("Fragment id to inspect.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fragmentID, err := req.RequireString("fragment_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			items, err := instance.Routing.ListRouteLog(ctx, fragmentID)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("route log: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(items, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("apply_route_by_entity",
			mcp.WithDescription("Apply a route to staged inbox items matching a persisted entity."),
			mcp.WithString("route_id", mcp.Required(), mcp.Description("Route id to apply.")),
			mcp.WithString("kind", mcp.Required(), mcp.Description("Entity kind.")),
			mcp.WithString("value", mcp.Required(), mcp.Description("Entity value.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			routeID, err := req.RequireString("route_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			kind, err := req.RequireString("kind")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			value, err := req.RequireString("value")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			instance, err := app.Open(ctx, cfg)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			defer instance.Close()
			result, err := instance.Routing.ApplyRouteByEntity(ctx, routeID, kind, value, 50)
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("apply route by entity: %v", err)), nil
			}
			raw, _ := json.MarshalIndent(result, "", "  ")
			return mcp.NewToolResultText(string(raw)), nil
		},
	)

	return server.ServeStdio(s)
}
