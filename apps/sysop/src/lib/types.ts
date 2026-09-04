export type ISODateString = string

export type JsonPrimitive = string | number | boolean | null

export type JsonValue = JsonPrimitive | JsonObject | JsonValue[]

export interface JsonObject {
  [key: string]: JsonValue
}

export type FragmentStatus = 'inbox' | 'routed' | 'indexed'

export interface Fragment {
  id: string
  source: string
  source_type: string
  source_id: string
  title: string
  content: string
  content_hash: string
  created_at: ISODateString
  ingested_at: ISODateString
  status: FragmentStatus
  summary: string
  indexed_at?: ISODateString
  metadata_json: string
  metadata: JsonObject
  ingest_name: string
  canonical_path: string
}

export interface FragmentAttachment {
  id: string
  kind: string
  role: string
  name: string
  mime_type: string
  source_path?: string
  external_url?: string
  storage_path?: string
  preview_storage_path?: string
  size_bytes?: number
  source: string
  source_item_id?: string
  metadata_json?: string
  metadata?: JsonObject
  analysis_summary?: string
  analysis_tags?: string[]
  vision_backend?: string
  vision_summary?: string
  vision_tags?: string[]
  vision_entities?: string[]
  vision_text_present?: boolean
  vision_confidence?: number
  vision_analysis_metadata?: JsonObject
  extracted_text_preview?: string
  extracted_text_bytes?: number
  ocr_status?: string
  created_at: ISODateString
}

export interface FragmentEntity {
  kind: string
  value: string
  source: string
  confidence: number
}

export interface FragmentRelation {
  fragment_id?: string
  target_id: string
  kind: string
  score?: number
  metadata_json?: string
  created_at?: ISODateString
}

export interface RecallTrace {
  backend: string
  strategy: string
  relation_kind?: string
  reason?: string
  metadata_json?: string
  memory_key?: string
  embedding_enabled?: boolean
}

export interface SearchResult {
  fragment: Fragment
  score: number
  snippet: string
  recall_trace: RecallTrace
  preview_attachment_id?: string
}

/**
 * A row from GET /v1/inbox — an inbox entry joined with its fragment
 * (domain.InboxItemDetail on the Go side), so the table can render
 * title/source/status without a per-row fragment lookup.
 */
export interface InboxItem {
  fragment_id: string
  reason: string
  staged_at: ISODateString
  route_id: string
  title: string
  source: string
  source_type: string
  status: string
  created_at: ISODateString
  preview_attachment_id?: string
}

export interface FragmentBrowseItem {
  fragment_id: string
  title: string
  source: string
  source_type: string
  status: string
  summary: string
  canonical_path: string
  source_id: string
  created_at: ISODateString
  modified_at: ISODateString
  preview_attachment_id?: string
  tags?: string[]
  materialized: boolean
}

export interface InboxEntityGroup {
  kind: string
  value: string
  fragment_count: number
}

export interface DeliveryRetryConfig {
  max_attempts: number
  backoff_ms: number
}

export interface QueuePolicyConfig {
  replay_cooldown_seconds: number
  max_replays_per_hour: number
  alert_pending_threshold: number
  alert_dead_letter_threshold: number
}

export interface DestinationDeliveryStatus {
  fragment_id: string
  route_id: string
  decision: string
  success: boolean
  ref?: string
  error?: string
  attempts: number
  created_at: ISODateString
}

export interface DestinationDeliveryMetrics {
  total_attempts: number
  success_count: number
  failure_count: number
  last_attempt_at?: ISODateString
  last_success_at?: ISODateString
  last_failure_at?: ISODateString
}

export interface DestinationStatusSummary {
  provider?: string
  config_valid?: boolean
  config_error?: string
  reachable?: boolean
  reachability?: string
  effective_retry?: DeliveryRetryConfig
  effective_queue_policy?: QueuePolicyConfig
  last_attempt?: DestinationDeliveryStatus
  last_success?: DestinationDeliveryStatus
  last_failure?: DestinationDeliveryStatus
  metrics?: DestinationDeliveryMetrics
}

export interface DestinationAlertSummary {
  alert: boolean
  alert_reason?: string
  pending_count?: number
  failed_count?: number
  replay_count?: number
  purge_count?: number
  dead_letter_count?: number
  last_event_at?: ISODateString
  last_failure_at?: ISODateString
  last_failure_error?: string
}

export interface Destination {
  id: string
  name: string
  kind: string
  config: JsonObject
  queue_policy?: QueuePolicyConfig
  status?: DestinationStatusSummary
  alerts?: DestinationAlertSummary
}

export interface Route {
  id: string
  name: string
  match_source: string
  match_type: string
  match_entity_kind: string
  match_entity_value: string
  destination_id: string
  auto_route: boolean
  confidence_min: number
}

export type ReaderScope = 'inbox' | 'library' | 'all'

export type ReaderRenderer =
  | 'article'
  | 'image'
  | 'gallery'
  | 'video'
  | 'audio'
  | 'document'
  | 'text'
  | 'unknown'

export interface ReaderSourceIdentity {
  source_registration_id?: string
  submitted_url?: string
  canonical_url?: string
  provider: string
  provider_item_id?: string
  source_item_key: string
  source_locator?: string
  segment_key: string
  canonicalizer?: {
    adapter: string
    version: string
  }
}

export type ReaderResolvedTextSource = 'source' | 'user' | 'deterministic' | 'provider' | 'model'

export interface ReaderResolvedText {
  value: string
  source: ReaderResolvedTextSource
  observation_id?: string
}

export interface ReaderAssetFailure {
  code: string
  message: string
  retryable: boolean
}

export type ReaderAcquisitionState = 'pending' | 'available' | 'reference_only' | 'failed'

export interface ReaderAssetVariant {
  asset_variant_id: string
  kind: 'original' | 'preview' | 'thumbnail' | 'poster' | 'audio' | 'subtitles' | 'transcript'
  custody: 'reference' | 'cache' | 'mirror' | 'adopted'
  acquisition_state: ReaderAcquisitionState
  mime_type?: string
  width?: number
  height?: number
  duration_seconds?: number
  byte_size?: number
  digest?: {
    algorithm: 'sha256'
    value: string
  }
  content_href?: string
  source_url?: string
  failure?: ReaderAssetFailure
}

export interface ReaderMediaItem {
  attachment: {
    attachment_id: string
    fragment_revision_id: string
    media_asset_id: string
    role: 'primary' | 'gallery_item' | 'hero' | 'inline' | 'poster' | 'transcript' | 'other'
    position: number
    caption?: string
    source_context?: string
  }
  media_asset_id: string
  provider_media_id?: string
  kind: 'image' | 'video' | 'audio' | 'document' | 'timed_text' | 'other'
  alt_text?: string
  variants: ReaderAssetVariant[]
}

export type ReaderReadingPosition =
  | { kind: 'none' }
  | { kind: 'article'; progress: number; block_anchor?: string; local_offset?: number }
  | {
      kind: 'video'
      elapsed_seconds: number
      duration_seconds?: number
      provider_media_id?: string
    }
  | { kind: 'gallery'; attachment_id: string; index: number }
  | { kind: 'document'; page: number; progress?: number }
  | { kind: 'audio'; elapsed_seconds: number; duration_seconds?: number }

export interface ReaderReadingState {
  principal_id: string
  fragment_id: string
  state: 'unread' | 'in_progress' | 'read'
  position: ReaderReadingPosition
  last_opened_at?: ISODateString
  completed_at?: ISODateString
  revision: number
}

export type ReaderEffectState = 'none' | 'pending' | 'succeeded' | 'partial' | 'failed'

export interface ReaderEffectSummary {
  state: ReaderEffectState
  references: string[]
}

export type ReaderCapabilityState =
  | 'provided'
  | 'missing'
  | 'pending'
  | 'failed'
  | 'stale'
  | 'not_applicable'

export type ReaderCapability =
  | 'title'
  | 'description'
  | 'body'
  | 'gallery_manifest'
  | 'original_media'
  | 'thumbnail_or_poster'
  | 'transcript'
  | 'OCR'
  | 'vision'
  | 'summary'
  | 'tags'
  | 'entities'

export interface ReaderCapabilityCoverage {
  capability: ReaderCapability
  state: ReaderCapabilityState
  observation_id?: string
  detail?: string
}

export interface ReaderOperationalSummaries {
  triage: {
    case_ids: string[]
    unresolved_count: number
  }
  routing: ReaderEffectSummary
  materialization: ReaderEffectSummary
  enrichment: ReaderCapabilityCoverage[]
  acquisition: Record<ReaderAcquisitionState, number>
}

export interface ReaderItem {
  schema_version: 'fe.reader.item.v1'
  fragment_id: string
  fragment_revision_id: string
  revision: number
  source: ReaderSourceIdentity
  renderer: ReaderRenderer
  display: {
    title: ReaderResolvedText
    description?: ReaderResolvedText
    byline?: ReaderResolvedText
    published_at?: ISODateString
    summary: ReaderResolvedText
  }
  article: {
    preview_markdown: string
    full_content_available: boolean
    full_content_href?: string
  }
  media: ReaderMediaItem[]
  playback?:
    | {
        kind: 'provider_embed'
        provider: 'youtube'
        provider_item_id: string
        start_seconds?: number
      }
    | {
        kind: 'blob_stream'
        asset_variant_id: string
        mime_type: string
        start_seconds?: number
      }
    | {
        kind: 'external_stream'
        url: string
        mime_type: string
        policy: 'allowlisted_provider' | 'signed_source'
        start_seconds?: number
      }
  tags: {
    combined: string[]
    attributed: Array<{
      value: string
      source: 'user' | 'provider' | 'deterministic' | 'model'
      observation_id?: string
    }>
  }
  annotations: Array<{
    annotation_id: string
    capture_id: string
    kind: 'highlight' | 'capture_note'
    text: string
    captured_at: ISODateString
    selector?: {
      exact: string
      prefix?: string
      suffix?: string
    }
    position?: {
      block_anchor?: string
      start_offset?: number
      end_offset?: number
    }
  }>
  curated_note?: {
    body_markdown: string
    revision: number
    updated_at: ISODateString
  }
  capture_count: number
  reading_state: ReaderReadingState
  operations: ReaderOperationalSummaries
  actions: Array<{
    command:
      | 'add_tag'
      | 'remove_tag'
      | 'append_capture_note'
      | 'update_curated_note'
      | 'set_reading_progress'
      | 'mark_read'
      | 'mark_unread'
      | 'request_asset_acquisition'
      | 'route'
      | 'materialize'
    input_schema: string
    expected_revision_required: boolean
  }>
}

export interface ReaderItemList {
  schema_version: 'fe.reader.list.v1'
  scope: ReaderScope
  items: ReaderItem[]
  next_cursor?: string
}
