package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

type FragmentStatus string

const (
	FragmentStatusInbox   FragmentStatus = "inbox"
	FragmentStatusRouted  FragmentStatus = "routed"
	FragmentStatusIndexed FragmentStatus = "indexed"
)

type Fragment struct {
	ID                 string
	Source             string
	SourceType         string
	SourceID           string
	SourceIdentity     SourceIdentity
	Title              string
	Content            string
	ContentHash        string
	AcceptedRevisionID string
	CurrentRevisionID  string
	// Revision carries the normalized candidate into the repository write.
	// Reads expose the selected immutable current revision here as well.
	Revision      FragmentRevision
	CreatedAt     time.Time
	IngestedAt    time.Time
	Status        FragmentStatus
	Summary       string
	IndexedAt     time.Time
	MetadataJSON  string
	IngestName    string
	CanonicalPath string
}

// AdapterVersion names the implementation and version responsible for a
// canonicalization or normalization decision. Persisting both makes stable
// identity and revision digests reproducible across adapter upgrades.
type AdapterVersion struct {
	Adapter string `json:"adapter"`
	Version string `json:"version"`
}

// SourceIdentity is the transport-independent identity relation shared by all
// ingests. SourceRegistrationID + SourceItemKey + SegmentKey resolves exactly
// one stable Fragment. URLs are optional because chat, git, filesystem, Nil,
// and manual fragments have useful source-native locators instead.
type SourceIdentity struct {
	SourceRegistrationID string         `json:"source_registration_id"`
	Provider             string         `json:"provider"`
	ProviderItemID       string         `json:"provider_item_id,omitempty"`
	SourceItemKey        string         `json:"source_item_key"`
	SourceLocator        string         `json:"source_locator,omitempty"`
	SegmentKey           string         `json:"segment_key"`
	SubmittedURL         string         `json:"submitted_url,omitempty"`
	CanonicalURL         string         `json:"canonical_url,omitempty"`
	SourceAdapter        AdapterVersion `json:"source_adapter"`
	Canonicalizer        AdapterVersion `json:"canonicalizer"`
}

// FragmentRevision is an immutable observation of source-owned material. User
// state (tags, notes, routing, triage, and reading state) is deliberately absent
// from MaterialDigest and remains attached to the stable Fragment.
type FragmentRevision struct {
	ID                 string         `json:"id"`
	FragmentID         string         `json:"fragment_id"`
	Ordinal            int            `json:"ordinal"`
	MaterialDigest     string         `json:"material_digest"`
	ContentDigest      string         `json:"content_digest"`
	Title              string         `json:"title"`
	Description        string         `json:"description,omitempty"`
	Content            string         `json:"content"`
	ContentFormat      string         `json:"content_format"`
	OrderedMediaDigest string         `json:"ordered_media_digest"`
	MetadataJSON       string         `json:"metadata_json"`
	Normalizer         AdapterVersion `json:"normalizer"`
	ObservedAt         time.Time      `json:"observed_at"`
	CommittedAt        time.Time      `json:"committed_at"`
	LegacyFragmentID   string         `json:"legacy_fragment_id,omitempty"`
}

type SearchResult struct {
	Fragment            Fragment    `json:"fragment"`
	Score               float64     `json:"score"`
	Snippet             string      `json:"snippet"`
	Trace               RecallTrace `json:"trace"`
	PreviewAttachmentID string      `json:"preview_attachment_id,omitempty"`
}

type RecallTrace struct {
	Backend          string `json:"backend"`
	Strategy         string `json:"strategy"`
	RelationKind     string `json:"relation_kind,omitempty"`
	Reason           string `json:"reason,omitempty"`
	MetadataJSON     string `json:"metadata_json,omitempty"`
	MemoryKey        string `json:"memory_key,omitempty"`
	EmbeddingEnabled bool   `json:"embedding_enabled,omitempty"`
}

type FragmentRelation struct {
	FragmentID        string
	RelatedFragmentID string
	Kind              string
	Score             float64
	MetadataJSON      string
	CreatedAt         time.Time
}

type FragmentRelationDetail struct {
	Relation FragmentRelation
	Related  Fragment
}

type FragmentEntity struct {
	Kind       string  `json:"kind"`
	Value      string  `json:"value"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
}

type FragmentAttachment struct {
	ID                 string         `json:"id"`
	Kind               string         `json:"kind"`
	Role               string         `json:"role"`
	Name               string         `json:"name"`
	MIMEType           string         `json:"mime_type"`
	SourcePath         string         `json:"source_path,omitempty"`
	ExternalURL        string         `json:"external_url,omitempty"`
	StoragePath        string         `json:"storage_path,omitempty"`
	PreviewStoragePath string         `json:"preview_storage_path,omitempty"`
	SizeBytes          int64          `json:"size_bytes,omitempty"`
	Source             string         `json:"source"`
	SourceItemID       string         `json:"source_item_id,omitempty"`
	MetadataJSON       string         `json:"metadata_json,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	AnalysisSummary    string         `json:"analysis_summary,omitempty"`
	AnalysisTags       []string       `json:"analysis_tags,omitempty"`
	VisionBackend      string         `json:"vision_backend,omitempty"`
	// VisionAnalysisBackend is the canonical name for the vision backend
	// (alias of VisionBackend; both are emitted for FE compatibility).
	VisionAnalysisBackend string `json:"vision_analysis_backend,omitempty"`
	VisionSummary         string `json:"vision_summary,omitempty"`
	// VisionAnalysis is the human-readable vision summary under the canonical
	// field name expected by the fragment-detail consumers (alias of
	// VisionSummary).
	VisionAnalysis    string   `json:"vision_analysis,omitempty"`
	VisionTags        []string `json:"vision_tags,omitempty"`
	VisionEntities    []string `json:"vision_entities,omitempty"`
	VisionTextPresent *bool    `json:"vision_text_present,omitempty"`
	VisionConfidence  *float64 `json:"vision_confidence,omitempty"`
	// Extracted-text (OCR) fields, surfaced from attachment metadata.
	ExtractedTextPreview string    `json:"extracted_text_preview,omitempty"`
	ExtractedTextBytes   int       `json:"extracted_text_bytes,omitempty"`
	OCRStatus            string    `json:"ocr_status,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type FragmentDetail struct {
	Fragment    Fragment
	Entities    []FragmentEntity
	Attachments []FragmentAttachment
	RouteLog    []RouteLogEntry
	Relations   []FragmentRelationDetail
	Related     []SearchResult
}

type FragmentBrowseItem struct {
	FragmentID          string    `json:"fragment_id"`
	Title               string    `json:"title"`
	Source              string    `json:"source"`
	SourceType          string    `json:"source_type"`
	Status              string    `json:"status"`
	Summary             string    `json:"summary"`
	CanonicalPath       string    `json:"canonical_path"`
	SourceID            string    `json:"source_id"`
	CreatedAt           time.Time `json:"created_at"`
	ModifiedAt          time.Time `json:"modified_at"`
	PreviewAttachmentID string    `json:"preview_attachment_id,omitempty"`
	Tags                []string  `json:"tags,omitempty"`
	Materialized        bool      `json:"materialized"`
}

type AttachmentReanalysisResult struct {
	FragmentID      string   `json:"fragment_id"`
	AttachmentIDs   []string `json:"attachment_ids"`
	UpdatedCount    int      `json:"updated_count"`
	SkippedCount    int      `json:"skipped_count"`
	ProviderBackend string   `json:"provider_backend,omitempty"`
}

type PinterestCorpusBackfillResult struct {
	ScannedCount   int      `json:"scanned_count"`
	CandidateCount int      `json:"candidate_count"`
	WrittenCount   int      `json:"written_count"`
	WrittenPaths   []string `json:"written_paths,omitempty"`
}

type PublishedAttachmentInfo struct {
	StoragePath        string `json:"storage_path"`
	PreviewStoragePath string `json:"preview_storage_path,omitempty"`
}

type InboxItem struct {
	FragmentID string
	Reason     string
	StagedAt   time.Time
	RouteID    string
}

// InboxItemDetail is an inbox row joined with its fragment so a UI can render
// a meaningful table (title, source, status) without an extra round-trip per
// row. JSON tags are snake_case so the sysop frontend consumes it directly.
type InboxItemDetail struct {
	FragmentID          string    `json:"fragment_id"`
	Reason              string    `json:"reason"`
	StagedAt            time.Time `json:"staged_at"`
	RouteID             string    `json:"route_id"`
	Title               string    `json:"title"`
	Source              string    `json:"source"`
	SourceType          string    `json:"source_type"`
	Status              string    `json:"status"`
	CreatedAt           time.Time `json:"created_at"`
	PreviewAttachmentID string    `json:"preview_attachment_id,omitempty"`
}

type InboxEntityGroup struct {
	Kind          string `json:"kind"`
	Value         string `json:"value"`
	FragmentCount int    `json:"fragment_count"`
}

type InboxReviewItemResult struct {
	FragmentID string `json:"fragment_id"`
	Title      string `json:"title"`
	Action     string `json:"action"`
	Updated    bool   `json:"updated"`
	Detail     string `json:"detail,omitempty"`
}

type InboxReviewResult struct {
	ReviewedCount int                     `json:"reviewed_count"`
	UpdatedCount  int                     `json:"updated_count"`
	SkippedCount  int                     `json:"skipped_count"`
	Items         []InboxReviewItemResult `json:"items,omitempty"`
}

type Destination struct {
	ID         string
	Name       string
	Kind       string
	ConfigJSON string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type DestinationDeliveryStatus struct {
	FragmentID string    `json:"fragment_id"`
	RouteID    string    `json:"route_id"`
	Decision   string    `json:"decision"`
	Success    bool      `json:"success"`
	Ref        string    `json:"ref,omitempty"`
	Error      string    `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	CreatedAt  time.Time `json:"created_at"`
}

type DestinationDeliveryMetrics struct {
	TotalAttempts int        `json:"total_attempts"`
	SuccessCount  int        `json:"success_count"`
	FailureCount  int        `json:"failure_count"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt *time.Time `json:"last_failure_at,omitempty"`
}

type FailedDeliveryJob struct {
	ID            int64     `json:"id"`
	Queue         string    `json:"queue"`
	Type          string    `json:"type"`
	FragmentID    string    `json:"fragment_id,omitempty"`
	RouteID       string    `json:"route_id,omitempty"`
	DestinationID string    `json:"destination_id,omitempty"`
	Attempts      int       `json:"attempts"`
	Error         string    `json:"error"`
	FailedAt      time.Time `json:"failed_at"`
}

type PendingDeliveryJob struct {
	ID            int64      `json:"id"`
	Queue         string     `json:"queue"`
	Type          string     `json:"type"`
	FragmentID    string     `json:"fragment_id,omitempty"`
	RouteID       string     `json:"route_id,omitempty"`
	DestinationID string     `json:"destination_id,omitempty"`
	Attempts      int        `json:"attempts"`
	MaxTries      int        `json:"max_tries"`
	AvailableAt   time.Time  `json:"available_at"`
	ReservedAt    *time.Time `json:"reserved_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type QueueJobEvent struct {
	ID            int64     `json:"id"`
	FailedJobID   *int64    `json:"failed_job_id,omitempty"`
	FragmentID    string    `json:"fragment_id,omitempty"`
	RouteID       string    `json:"route_id,omitempty"`
	DestinationID string    `json:"destination_id,omitempty"`
	EventType     string    `json:"event_type"`
	DetailJSON    string    `json:"detail_json,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type QueueDestinationSummary struct {
	DestinationID    string     `json:"destination_id"`
	DestinationName  string     `json:"destination_name"`
	Provider         string     `json:"provider"`
	ConfigValid      bool       `json:"config_valid"`
	Reachable        bool       `json:"reachable"`
	Alert            bool       `json:"alert"`
	AlertReason      string     `json:"alert_reason,omitempty"`
	PendingCount     int        `json:"pending_count"`
	FailedCount      int        `json:"failed_count"`
	ReplayCount      int        `json:"replay_count"`
	PurgeCount       int        `json:"purge_count"`
	DeadLetterCount  int        `json:"dead_letter_count"`
	LastEventAt      *time.Time `json:"last_event_at,omitempty"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	LastFailureError string     `json:"last_failure_error,omitempty"`
}

type DestinationStatus struct {
	Destination          Destination                `json:"destination"`
	Provider             string                     `json:"provider"`
	ConfigValid          bool                       `json:"config_valid"`
	ConfigError          string                     `json:"config_error,omitempty"`
	Reachable            bool                       `json:"reachable"`
	Reachability         string                     `json:"reachability,omitempty"`
	EffectiveRetry       DeliveryRetryConfig        `json:"effective_retry"`
	EffectiveQueuePolicy QueuePolicyConfig          `json:"effective_queue_policy"`
	LastAttempt          *DestinationDeliveryStatus `json:"last_attempt,omitempty"`
	LastSuccess          *DestinationDeliveryStatus `json:"last_success,omitempty"`
	LastFailure          *DestinationDeliveryStatus `json:"last_failure,omitempty"`
	Metrics              DestinationDeliveryMetrics `json:"metrics"`
}

type DestinationValidation struct {
	Destination  Destination `json:"destination"`
	Provider     string      `json:"provider"`
	ConfigValid  bool        `json:"config_valid"`
	ConfigError  string      `json:"config_error,omitempty"`
	Reachable    bool        `json:"reachable"`
	Reachability string      `json:"reachability,omitempty"`
}

type DestinationDeleteResult struct {
	DestinationID string   `json:"destination_id"`
	Deleted       bool     `json:"deleted"`
	Force         bool     `json:"force"`
	RouteIDs      []string `json:"route_ids,omitempty"`
}

type Route struct {
	ID               string
	Name             string
	MatchSource      string
	MatchType        string
	MatchEntityKind  string
	MatchEntityValue string
	DestinationID    string
	AutoRoute        bool
	ConfidenceMin    float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type RouteLogEntry struct {
	ID            int64
	FragmentID    string
	RouteID       string
	DestinationID string
	Decision      string
	Reason        string
	CreatedAt     time.Time
}

type RouteApplyItem struct {
	FragmentID  string `json:"fragment_id"`
	Status      string `json:"status"`
	WrittenPath string `json:"written_path,omitempty"`
	Error       string `json:"error,omitempty"`
}

type RouteApplyResult struct {
	RouteID      string           `json:"route_id"`
	EntityKind   string           `json:"entity_kind"`
	EntityValue  string           `json:"entity_value"`
	MatchedCount int              `json:"matched_count"`
	RoutedCount  int              `json:"routed_count"`
	FailedCount  int              `json:"failed_count"`
	Items        []RouteApplyItem `json:"items"`
}

type RoutePreviewItem struct {
	FragmentID string `json:"fragment_id"`
	Title      string `json:"title"`
	Source     string `json:"source"`
	SourceType string `json:"source_type"`
	Reason     string `json:"reason"`
}

type RoutePreviewResult struct {
	RouteID      string             `json:"route_id"`
	MatchedCount int                `json:"matched_count"`
	PreviewItems []RoutePreviewItem `json:"preview_items"`
}

type RouteMaterializeItem struct {
	FragmentID  string `json:"fragment_id"`
	Status      string `json:"status"`
	WrittenPath string `json:"written_path,omitempty"`
	Error       string `json:"error,omitempty"`
}

type RouteMaterializeResult struct {
	RouteID           string                 `json:"route_id"`
	MatchedCount      int                    `json:"matched_count"`
	MaterializedCount int                    `json:"materialized_count"`
	FailedCount       int                    `json:"failed_count"`
	Items             []RouteMaterializeItem `json:"items"`
}

type FragmentMaterializeResult struct {
	FragmentID      string `json:"fragment_id"`
	DestinationID   string `json:"destination_id"`
	DestinationName string `json:"destination_name"`
	WrittenPath     string `json:"written_path,omitempty"`
}

type RouteDeleteResult struct {
	RouteID      string `json:"route_id"`
	Deleted      bool   `json:"deleted"`
	Force        bool   `json:"force"`
	StagedRefs   int    `json:"staged_refs"`
	RouteLogRefs int    `json:"route_log_refs"`
}

type FileDestinationConfig struct {
	Root         string              `json:"root"`
	Provider     string              `json:"provider,omitempty"`
	PathTemplate string              `json:"path_template,omitempty"`
	Retry        DeliveryRetryConfig `json:"retry"`
	QueuePolicy  *QueuePolicyConfig  `json:"queue_policy,omitempty"`
}

type MCPDestinationConfig struct {
	// Transport selects the MCP client transport: "stdio" (default, spawns
	// Command as a subprocess) or "http" (Streamable HTTP against BaseURL,
	// e.g. a long-running peer like Tangent).
	Transport      string               `json:"transport"`
	Command        string               `json:"command"`
	Args           []string             `json:"args"`
	Env            []string             `json:"env"`
	BaseURL        string               `json:"base_url"`
	Tool           string               `json:"tool"`
	TimeoutSeconds int                  `json:"timeout_seconds"`
	Provider       string               `json:"provider"`
	NilInbox       MCPNilInboxConfig    `json:"nil_inbox"`
	TangentHITL    MCPTangentHITLConfig `json:"tangent_hitl"`
	Arguments      map[string]any       `json:"arguments"`
	Retry          DeliveryRetryConfig  `json:"retry"`
	QueuePolicy    *QueuePolicyConfig   `json:"queue_policy,omitempty"`
}

type MCPNilInboxConfig struct {
	TitlePrefix string   `json:"title_prefix"`
	ItemType    string   `json:"item_type"`
	Tags        []string `json:"tags"`
	Contexts    []string `json:"contexts"`
	Projects    []string `json:"projects"`
}

// MCPTangentHITLConfig configures the "tangent_hitl" MCP provider, which
// calls Tangent's tangent.hitl_enqueue tool (kind "attention") to place a
// fragment in Tangent's durable /hitl operator inbox. See Tangent's own
// .agents/skills/tangent-hitl-inbox/SKILL.md and
// docs/contracts/hitl-inbox-v1.md for the contract.
type MCPTangentHITLConfig struct {
	ApplicationID string            `json:"application_id"`
	AgentID       string            `json:"agent_id"`
	Summary       string            `json:"summary"`
	Request       string            `json:"request"`
	ActionLabels  map[string]string `json:"action_labels"`
}

type APIDestinationConfig struct {
	BaseURL              string                   `json:"base_url"`
	Path                 string                   `json:"path"`
	Method               string                   `json:"method"`
	Headers              map[string]string        `json:"headers"`
	TimeoutSeconds       int                      `json:"timeout_seconds"`
	Provider             string                   `json:"provider"`
	BasicAuthUsername    string                   `json:"basic_auth_username"`
	BasicAuthPasswordEnv string                   `json:"basic_auth_password_env"`
	NaniteMessaging      APINaniteMessagingConfig `json:"nanite_messaging"`
	Body                 map[string]any           `json:"body"`
	Retry                DeliveryRetryConfig      `json:"retry"`
	QueuePolicy          *QueuePolicyConfig       `json:"queue_policy,omitempty"`
}

type CLIDestinationConfig struct {
	Command        string              `json:"command"`
	Args           []string            `json:"args"`
	Env            []string            `json:"env"`
	WorkingDir     string              `json:"working_dir"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	Provider       string              `json:"provider"`
	Retry          DeliveryRetryConfig `json:"retry"`
	QueuePolicy    *QueuePolicyConfig  `json:"queue_policy,omitempty"`
}

// CallbackDestinationConfig is the fifth FE destination kind (loom-architecture.md
// §3-§4). It never fires synchronously: delivery is always deferred to the
// existing async delivery queue (see internal/service/delivery_queue.go and
// internal/app/queue_runtime.go), which is dispatch-wiring's job, not this
// package's. FE treats Generator as an opaque tag and never interprets it —
// it is only forwarded to Target, Curator's Nanite durable-agent wake endpoint.
type CallbackDestinationConfig struct {
	Target      string              `json:"target" yaml:"target"`
	Generator   string              `json:"generator" yaml:"generator"`
	Retry       DeliveryRetryConfig `json:"retry" yaml:"retry"`
	QueuePolicy *QueuePolicyConfig  `json:"queue_policy,omitempty" yaml:"queue_policy,omitempty"`
}

type APINaniteMessagingConfig struct {
	Endpoint      string `json:"endpoint"`
	FromSessionID string `json:"from_session_id"`
	FromAgentID   string `json:"from_agent_id"`
	ToSessionID   string `json:"to_session_id"`
	ToAgentID     string `json:"to_agent_id"`
	Channel       string `json:"channel"`
	Kind          string `json:"kind"`
	Type          string `json:"type"`
	RegisterAs    string `json:"register_as"`
	SubjectPrefix string `json:"subject_prefix"`
}

type DeliveryRetryConfig struct {
	MaxAttempts int `json:"max_attempts"`
	BackoffMS   int `json:"backoff_ms"`
}

type QueuePolicyConfig struct {
	ReplayCooldownSeconds    int `json:"replay_cooldown_seconds"`
	MaxReplaysPerHour        int `json:"max_replays_per_hour"`
	AlertPendingThreshold    int `json:"alert_pending_threshold"`
	AlertDeadLetterThreshold int `json:"alert_dead_letter_threshold"`
}

func DecodeDestinationConfig[T any](destination Destination) (T, error) {
	var out T
	if destination.ConfigJSON == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(destination.ConfigJSON), &out); err != nil {
		return out, fmt.Errorf("decode destination config: %w", err)
	}
	return out, nil
}
