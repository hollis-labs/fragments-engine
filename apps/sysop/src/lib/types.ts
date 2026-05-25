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
