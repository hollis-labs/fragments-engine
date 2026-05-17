import type {
  DeliveryRetryConfig,
  Destination,
  DestinationStatusSummary,
  Fragment,
  FragmentAttachment,
  FragmentEntity,
  FragmentRelation,
  InboxEntityGroup,
  InboxItem,
  JsonObject,
  QueuePolicyConfig,
  Route,
  SearchResult,
} from './types'
import {
  normalizeFragment,
  normalizeFragmentAttachment,
  normalizeFragmentEntity,
  normalizeFragmentRelation,
  normalizeInboxEntityGroups,
  normalizeInboxItem,
  normalizeKeys,
  normalizeSearchResult,
  parseMetadataJson,
} from './normalize'

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ?? ''

export class ApiError extends Error {
  status: number
  data?: unknown

  constructor(message: string, status: number, data?: unknown) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.data = data
  }
}

export interface FetchInboxParams {
  limit?: number
  offset?: number
}

export interface FetchInboxEntitiesParams {
  kind?: string
}

export interface FetchInboxEntityItemsParams {
  kind: string
  value: string
}

export interface FetchFragmentParams {
  fragmentId: string
}

export interface ReanalyzeFragmentAttachmentsInput {
  fragmentId: string
  attachmentId?: string
}

/** Search mode requested for /v1/search. */
export type SearchMode = 'auto' | 'semantic' | 'keyword'

export interface SearchParams {
  q?: string
  entityKind?: string
  entityValue?: string
  /** Retrieval strategy hint; defaults to `auto` server-side. */
  mode?: SearchMode
  /** Max results to return; defaults to 20 server-side. */
  limit?: number
  /** Optional fragment-status filter. */
  status?: string
}

/** A /v1/search response — results plus the strategy the server actually ran. */
export interface SearchResponse {
  results: SearchResult[]
  /** Concrete strategy used (`auto` resolves to one of these). */
  mode_used?: 'semantic' | 'keyword'
}

/** A queued or failed ingest job from /v1/jobs/ingest. */
export interface IngestJobRecord {
  id: string
  ingest_name: string
  status: string
  attempts: number
  max_attempts: number
  enqueued_at: string
  last_error?: string
}

/** Pending + failed ingest jobs from /v1/jobs/ingest. */
export interface IngestJobs {
  pending: IngestJobRecord[]
  failed: IngestJobRecord[]
}

/** A single worker's liveness from /v1/workers/status. */
export interface WorkerStatus {
  name: string
  kind: string
  running: boolean
  detail?: string
}

/** A scheduler entry from /v1/workers/status. */
export interface SchedulerSchedule {
  ingest_name: string
  cron_expr: string
  next_run?: string
  last_run?: string
  enabled: boolean
}

/** Worker + scheduler liveness from /v1/workers/status. */
export interface WorkersStatus {
  workers: WorkerStatus[]
  scheduler: {
    running: boolean
    schedules: SchedulerSchedule[]
  }
}

/** Engine config payload from /v1/config. */
export interface EngineConfigResult {
  config: JsonObject
  path: string
}

/** Result of POST /v1/config/update. */
export interface ConfigUpdateResult {
  ok: boolean
  restart_required: boolean
}

/** Input to POST /v1/intake. */
export interface IntakeInput {
  content: string
  title?: string
  sourceType?: string
  tags?: string[]
}

export interface FetchEntitiesParams {
  kind?: string
  limit?: number
}

export interface IngestSummary {
  name: string
  kind: string
  enabled: boolean
  source_root: string
  namespace: string
  labels?: Record<string, string>
  archive_root?: string
  copy_text_exports?: boolean
  delete_copied_source?: boolean
}

/** Mutable surface of an ingest source — input to createIngest / updateIngest. */
export interface IngestSourceInput {
  name: string
  kind: string
  enabled: boolean
  sourceRoot: string
  namespace: string
  rules?: JsonObject
  labels?: Record<string, string>
}

/**
 * The full ingest source record from /v1/ingests/get — unlike IngestSummary,
 * it carries the raw `rules` map verbatim so an editor can round-trip it.
 */
export interface IngestRecord {
  name: string
  kind: string
  enabled: boolean
  source_root: string
  namespace: string
  rules?: JsonObject
  labels?: Record<string, string>
}

/** A run queued by /v1/ingests/run or /v1/ingests/run-ingest. */
export interface EnqueuedIngestRun {
  run_id: number
  ingest_name: string
}

/** A row of ingest run history from /v1/ingests/runs. */
export interface IngestRunRecord {
  id: number
  name: string
  kind: string
  status: string
  started_at?: string
  finished_at?: string
  inserted: number
  updated: number
  skipped: number
  error?: string
}

/** A cron schedule for an ingest source. */
export interface IngestSchedule {
  id: string
  ingest_name: string
  cron_expr: string
  enabled: boolean
  last_run?: string
  next_run?: string
  created_at: string
  updated_at: string
}

/** Mutable surface of an ingest schedule — input to create/update. */
export interface IngestScheduleInput {
  ingestName: string
  cronExpr: string
  enabled: boolean
}

export interface FetchEntityFragmentsParams {
  kind: string
  value: string
}

export interface FetchRoutePreviewParams {
  routeId: string
}

export interface RenameRouteInput {
  routeId: string
  name: string
}

export interface DeleteRouteInput {
  routeId: string
  force?: boolean
}

export interface ApplyRouteEntityInput {
  routeId: string
  kind: string
  value: string
  limit?: number
}

export interface FetchDestinationStatusParams {
  destinationId: string
}

export interface ValidateDestinationInput {
  name?: string
  kind: string
  configJson: string
}

export interface RenameDestinationInput {
  destinationId: string
  name: string
}

export interface DeleteDestinationInput {
  destinationId: string
  force?: boolean
}

export interface RetryDestinationInput extends DeliveryRetryConfig {
  destinationId: string
}

export interface UpdateDestinationQueuePolicyInput extends QueuePolicyConfig {
  destinationId: string
}

export interface QueueFilterParams {
  destinationId?: string
}

export interface DrainQueueInput {
  limit?: number
}

export interface QueueFailedActionInput {
  id: number
  force?: boolean
}

export interface ApiRequestOptions {
  signal?: AbortSignal
}

export interface EntityRecord {
  id: string
  kind: string
  value: string
  fragment_count: number
  created_at: string
}

export interface QueueStats {
  pending: number
  failed: number
}

export interface QueuePendingItem {
  id: number
  queue: string
  type: string
  fragment_id?: string
  route_id?: string
  destination_id?: string
  attempts: number
  max_tries: number
  available_at: string
  reserved_at?: string
  created_at: string
}

export interface QueueFailedItem {
  id: number
  queue: string
  type: string
  fragment_id?: string
  route_id?: string
  destination_id?: string
  attempts: number
  error: string
  failed_at: string
}

export interface QueueEvent {
  id: number
  failed_job_id?: number
  fragment_id?: string
  route_id?: string
  destination_id?: string
  event_type: string
  detail_json?: string
  created_at: string
}

export interface QueueDestinationSummary {
  destination_id: string
  destination_name: string
  provider: string
  config_valid: boolean
  reachable: boolean
  alert: boolean
  alert_reason?: string
  pending_count: number
  failed_count: number
  replay_count: number
  purge_count: number
  dead_letter_count: number
  last_event_at?: string
  last_failure_at?: string
  last_failure_error?: string
}

export interface RouteLogEntry {
  id: number
  fragment_id: string
  route_id: string
  destination_id: string
  decision: string
  reason: string
  created_at: string
}

export interface FragmentRelationDetail {
  relation: FragmentRelation
  related: Fragment
}

export interface FragmentDetail {
  fragment: Fragment
  entities: FragmentEntity[]
  attachments: FragmentAttachment[]
  route_log: RouteLogEntry[]
  relations: FragmentRelationDetail[]
  related: SearchResult[]
}

export interface AttachmentReanalysisResult {
  fragment_id: string
  attachment_ids: string[]
  updated_count: number
  skipped_count: number
  provider_backend?: string
}

export interface RecallStatus {
  backend?: string
  enabled?: boolean
  last_indexed_at?: string
  indexed_count?: number
  [key: string]: unknown
}

export interface RoutePreviewItem {
  fragment_id: string
  title: string
  source: string
  source_type: string
  reason: string
}

export interface RoutePreviewResult {
  route_id: string
  matched_count: number
  preview_items: RoutePreviewItem[]
}

export interface RouteDeleteResult {
  route_id: string
  deleted: boolean
  force: boolean
  staged_refs: number
  route_log_refs: number
}

export interface RouteApplyItem {
  fragment_id: string
  status: string
  written_path?: string
  error?: string
}

export interface RouteApplyResult {
  route_id: string
  entity_kind: string
  entity_value: string
  matched_count: number
  routed_count: number
  failed_count: number
  items: RouteApplyItem[]
}

export interface DestinationStatusRecord {
  destination: Destination
  provider?: string
  config_valid?: boolean
  config_error?: string
  reachable?: boolean
  reachability?: string
  effective_retry?: DeliveryRetryConfig
  effective_queue_policy?: QueuePolicyConfig
  last_attempt?: DestinationStatusSummary['last_attempt']
  last_success?: DestinationStatusSummary['last_success']
  last_failure?: DestinationStatusSummary['last_failure']
  metrics?: DestinationStatusSummary['metrics']
}

export interface DestinationValidationResult {
  destination: Destination
  provider?: string
  config_valid?: boolean
  config_error?: string
  reachable?: boolean
  reachability?: string
}

export interface DestinationDeleteResult {
  destination_id: string
  deleted: boolean
  force: boolean
  route_ids?: string[]
}

type QueryValue = string | number | boolean | null | undefined

function appendParam(searchParams: URLSearchParams, key: string, value: QueryValue) {
  if (value === undefined || value === null || value === '') {
    return
  }
  searchParams.set(key, String(value))
}

function buildUrl(path: string, query?: Record<string, QueryValue>) {
  const url = new URL(`${API_BASE_URL}${path}`, window.location.origin)
  if (query) {
    for (const [key, value] of Object.entries(query)) {
      appendParam(url.searchParams, key, value)
    }
  }
  return `${url.pathname}${url.search}`
}

function mapDestination(value: unknown): Destination {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  const config = parseMetadataJson(normalized.config ?? normalized.config_json)
  const queuePolicy =
    config.queue_policy &&
    typeof config.queue_policy === 'object' &&
    !Array.isArray(config.queue_policy)
      ? (config.queue_policy as unknown as QueuePolicyConfig)
      : undefined

  return {
    id: typeof normalized.id === 'string' ? normalized.id : '',
    name: typeof normalized.name === 'string' ? normalized.name : '',
    kind: typeof normalized.kind === 'string' ? normalized.kind : '',
    config,
    ...(queuePolicy ? { queue_policy: queuePolicy } : {}),
  }
}

function mapRoute(value: unknown): Route {
  return normalizeKeys(value) as Route
}

function mapDestinationStatusRecord(value: unknown): DestinationStatusRecord {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    destination: mapDestination(normalized.destination),
    provider: typeof normalized.provider === 'string' ? normalized.provider : undefined,
    config_valid:
      typeof normalized.config_valid === 'boolean' ? normalized.config_valid : undefined,
    config_error:
      typeof normalized.config_error === 'string' ? normalized.config_error : undefined,
    reachable: typeof normalized.reachable === 'boolean' ? normalized.reachable : undefined,
    reachability:
      typeof normalized.reachability === 'string' ? normalized.reachability : undefined,
    effective_retry: normalizeKeys(normalized.effective_retry) as DeliveryRetryConfig | undefined,
    effective_queue_policy: normalizeKeys(
      normalized.effective_queue_policy,
    ) as QueuePolicyConfig | undefined,
    last_attempt: normalizeKeys(normalized.last_attempt) as
      | DestinationStatusSummary['last_attempt']
      | undefined,
    last_success: normalizeKeys(normalized.last_success) as
      | DestinationStatusSummary['last_success']
      | undefined,
    last_failure: normalizeKeys(normalized.last_failure) as
      | DestinationStatusSummary['last_failure']
      | undefined,
    metrics: normalizeKeys(normalized.metrics) as DestinationStatusSummary['metrics'] | undefined,
  }
}

function mapDestinationValidationResult(value: unknown): DestinationValidationResult {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    destination: mapDestination(normalized.destination),
    provider: typeof normalized.provider === 'string' ? normalized.provider : undefined,
    config_valid:
      typeof normalized.config_valid === 'boolean' ? normalized.config_valid : undefined,
    config_error:
      typeof normalized.config_error === 'string' ? normalized.config_error : undefined,
    reachable: typeof normalized.reachable === 'boolean' ? normalized.reachable : undefined,
    reachability:
      typeof normalized.reachability === 'string' ? normalized.reachability : undefined,
  }
}

function mapAttachmentReanalysisResult(value: unknown): AttachmentReanalysisResult {
  return normalizeKeys(value) as AttachmentReanalysisResult
}

function mapRecallStatus(value: unknown): RecallStatus {
  return normalizeKeys(value) as RecallStatus
}

function mapEntityRecord(value: unknown): EntityRecord {
  return normalizeKeys(value) as EntityRecord
}

function mapIngestSummary(value: unknown): IngestSummary {
  return normalizeKeys(value) as IngestSummary
}

function mapEnqueuedIngestRun(value: unknown): EnqueuedIngestRun {
  return normalizeKeys(value) as EnqueuedIngestRun
}

function mapIngestRunRecord(value: unknown): IngestRunRecord {
  return normalizeKeys(value) as IngestRunRecord
}

function mapIngestSchedule(value: unknown): IngestSchedule {
  return normalizeKeys(value) as IngestSchedule
}

function mapIngestRecord(value: unknown): IngestRecord {
  const record = normalizeKeys(value) as IngestRecord
  // normalizeKeys() recurses into nested objects; carry the free-form `rules`
  // map through verbatim so its rule keys are never rewritten.
  if (value && typeof value === 'object' && 'rules' in value) {
    record.rules = (value as { rules?: JsonObject }).rules
  }
  return record
}

function mapRoutePreviewResult(value: unknown): RoutePreviewResult {
  return normalizeKeys(value) as RoutePreviewResult
}

function mapRouteDeleteResult(value: unknown): RouteDeleteResult {
  return normalizeKeys(value) as RouteDeleteResult
}

function mapRouteApplyResult(value: unknown): RouteApplyResult {
  return normalizeKeys(value) as RouteApplyResult
}

function mapDestinationDeleteResult(value: unknown): DestinationDeleteResult {
  return normalizeKeys(value) as DestinationDeleteResult
}

function mapQueueStats(value: unknown): QueueStats {
  return normalizeKeys(value) as QueueStats
}

function mapQueuePendingItem(value: unknown): QueuePendingItem {
  return normalizeKeys(value) as QueuePendingItem
}

function mapQueueFailedItem(value: unknown): QueueFailedItem {
  return normalizeKeys(value) as QueueFailedItem
}

function mapQueueEvent(value: unknown): QueueEvent {
  return normalizeKeys(value) as QueueEvent
}

function mapQueueDestinationSummary(value: unknown): QueueDestinationSummary {
  return normalizeKeys(value) as QueueDestinationSummary
}

function mapFragmentDetail(value: unknown): FragmentDetail {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    fragment: normalizeFragment(normalized.fragment),
    entities: Array.isArray(normalized.entities)
      ? normalized.entities.map((item) => normalizeFragmentEntity(item))
      : [],
    attachments: Array.isArray(normalized.attachments)
      ? normalized.attachments.map((item) => normalizeFragmentAttachment(item))
      : [],
    route_log: Array.isArray(normalized.route_log)
      ? (normalized.route_log.map((item) => normalizeKeys(item)) as RouteLogEntry[])
      : [],
    relations: Array.isArray(normalized.relations)
      ? normalized.relations.map((item) => {
          const detail = normalizeKeys(item) as Record<string, unknown>
          return {
            relation: normalizeFragmentRelation(detail.relation),
            related: normalizeFragment(detail.related),
          }
        })
      : [],
    related: Array.isArray(normalized.related)
      ? normalized.related.map((item) => normalizeSearchResult(item))
      : [],
  }
}

async function parseResponseBody(response: Response): Promise<unknown> {
  const contentType = response.headers.get('content-type') ?? ''
  if (contentType.includes('application/json')) {
    return response.json()
  }

  const text = await response.text()
  return text.length > 0 ? text : undefined
}

async function apiFetch<TResponse>(
  path: string,
  init?: RequestInit,
  query?: Record<string, QueryValue>,
): Promise<TResponse> {
  const response = await fetch(buildUrl(path, query), {
    credentials: 'same-origin',
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
      ...(init?.headers ?? {}),
    },
  })

  const data = await parseResponseBody(response)
  if (!response.ok) {
    const message =
      typeof data === 'string'
        ? data
        : typeof data === 'object' && data !== null && 'message' in data
          ? String(data.message)
          : response.statusText || 'Request failed'
    throw new ApiError(message, response.status, data)
  }

  return data as TResponse
}

function postJson<TResponse>(path: string, body?: JsonObject, options?: ApiRequestOptions) {
  return apiFetch<TResponse>(path, {
    method: 'POST',
    body: body ? JSON.stringify(body) : undefined,
    signal: options?.signal,
  })
}

export async function fetchInbox(
  params: FetchInboxParams = {},
  options?: ApiRequestOptions,
): Promise<InboxItem[]> {
  const data = await apiFetch<{ items: InboxItem[] }>(
    '/v1/inbox',
    { signal: options?.signal },
    {
      limit: params.limit,
      offset: params.offset,
    },
  )
  return data.items.map((item) => normalizeInboxItem(item))
}

export async function fetchInboxEntities(
  params: FetchInboxEntitiesParams = {},
): Promise<InboxEntityGroup[]> {
  const data = await apiFetch<{ items: InboxEntityGroup[] }>('/v1/inbox/entities', undefined, {
    kind: params.kind,
  })
  return normalizeInboxEntityGroups(data.items)
}

export async function fetchInboxEntityItems(
  params: FetchInboxEntityItemsParams,
): Promise<InboxItem[]> {
  const data = await apiFetch<{ items: InboxItem[] }>('/v1/inbox/entity-items', undefined, {
    kind: params.kind,
    value: params.value,
  })
  return data.items.map((item) => normalizeInboxItem(item))
}

export async function fetchFragment(params: FetchFragmentParams): Promise<FragmentDetail> {
  const data = await apiFetch<{ detail: FragmentDetail }>('/v1/fragments/get', undefined, {
    'fragment-id': params.fragmentId,
    fragment_id: params.fragmentId,
  })
  return mapFragmentDetail(data.detail)
}

export async function fetchRelatedFragments(params: FetchFragmentParams): Promise<SearchResult[]> {
  const data = await apiFetch<{ results: SearchResult[] }>('/v1/fragments/related', undefined, {
    'fragment-id': params.fragmentId,
    fragment_id: params.fragmentId,
  })
  return data.results.map((item) => normalizeSearchResult(item))
}

export async function reanalyzeFragmentAttachments(
  input: ReanalyzeFragmentAttachmentsInput,
): Promise<AttachmentReanalysisResult> {
  const data = await postJson<{ result: AttachmentReanalysisResult }>(
    '/v1/fragments/reanalyze-attachments',
    {
      fragment_id: input.fragmentId,
      ...(input.attachmentId ? { attachment_id: input.attachmentId } : {}),
    },
  )
  return mapAttachmentReanalysisResult(data.result)
}

export async function searchFragments(
  params: SearchParams,
  options?: ApiRequestOptions,
): Promise<SearchResult[]> {
  const result = await searchFragmentsDetailed(params, options)
  return result.results
}

/**
 * GET /v1/search — like searchFragments, but also surfaces `mode_used` so the
 * caller can show which retrieval strategy actually ran.
 */
export async function searchFragmentsDetailed(
  params: SearchParams,
  options?: ApiRequestOptions,
): Promise<SearchResponse> {
  const data = await apiFetch<{ results?: SearchResult[]; mode_used?: string }>(
    '/v1/search',
    { signal: options?.signal },
    {
      q: params.q,
      'entity-kind': params.entityKind,
      entity_kind: params.entityKind,
      'entity-value': params.entityValue,
      entity_value: params.entityValue,
      mode: params.mode,
      limit: params.limit,
      status: params.status,
    },
  )
  const modeUsed =
    data.mode_used === 'semantic' || data.mode_used === 'keyword' ? data.mode_used : undefined
  return {
    results: (data.results ?? []).map((item) => normalizeSearchResult(item)),
    mode_used: modeUsed,
  }
}

/** POST /v1/intake — manually create a fragment from free-form content. */
export async function createIntake(
  input: IntakeInput,
  options?: ApiRequestOptions,
): Promise<Fragment> {
  const body: JsonObject = { content: input.content }
  if (input.title && input.title.trim()) body.title = input.title.trim()
  if (input.sourceType && input.sourceType.trim()) body.source_type = input.sourceType.trim()
  if (input.tags && input.tags.length > 0) body.tags = input.tags
  const data = await postJson<{ result: unknown }>('/v1/intake', body, options)
  return normalizeFragment(data.result)
}

/** GET /v1/jobs/ingest — pending + failed ingest jobs. */
export async function fetchIngestJobs(options?: ApiRequestOptions): Promise<IngestJobs> {
  const data = await apiFetch<{ pending?: unknown[]; failed?: unknown[] }>(
    '/v1/jobs/ingest',
    { signal: options?.signal },
  )
  return {
    pending: (data.pending ?? []).map((item) => normalizeKeys(item) as IngestJobRecord),
    failed: (data.failed ?? []).map((item) => normalizeKeys(item) as IngestJobRecord),
  }
}

/** GET /v1/workers/status — worker liveness + scheduler state. */
export async function fetchWorkersStatus(options?: ApiRequestOptions): Promise<WorkersStatus> {
  const data = await apiFetch<{ workers?: unknown[]; scheduler?: unknown }>(
    '/v1/workers/status',
    { signal: options?.signal },
  )
  const scheduler = normalizeKeys(data.scheduler ?? {}) as Record<string, unknown>
  return {
    workers: (data.workers ?? []).map((item) => normalizeKeys(item) as WorkerStatus),
    scheduler: {
      running: scheduler.running === true,
      schedules: Array.isArray(scheduler.schedules)
        ? (scheduler.schedules as unknown[]).map((s) => normalizeKeys(s) as SchedulerSchedule)
        : [],
    },
  }
}

/** GET /v1/config — the full engine config plus its on-disk path. */
export async function fetchEngineConfig(
  options?: ApiRequestOptions,
): Promise<EngineConfigResult> {
  const data = await apiFetch<{ config?: JsonObject; path?: string }>('/v1/config', {
    signal: options?.signal,
  })
  return {
    config: (data.config ?? {}) as JsonObject,
    path: typeof data.path === 'string' ? data.path : '',
  }
}

/** POST /v1/config/update — replace the whole engine config. */
export async function updateEngineConfig(
  config: JsonObject,
  options?: ApiRequestOptions,
): Promise<ConfigUpdateResult> {
  const data = await postJson<{ ok?: boolean; restart_required?: boolean }>(
    '/v1/config/update',
    { config },
    options,
  )
  return {
    ok: data.ok === true,
    restart_required: data.restart_required === true,
  }
}

export async function fetchRecallStatus(): Promise<RecallStatus> {
  const data = await apiFetch<{ status: RecallStatus }>('/v1/recall/status')
  return mapRecallStatus(data.status)
}

export async function fetchEntities(params: FetchEntitiesParams = {}): Promise<EntityRecord[]> {
  const data = await apiFetch<{ items: EntityRecord[] }>('/v1/entities', undefined, {
    kind: params.kind,
    limit: params.limit,
  })
  return data.items.map((item) => mapEntityRecord(item))
}

export async function fetchIngests(): Promise<IngestSummary[]> {
  const data = await apiFetch<{ ingests: IngestSummary[] }>('/v1/ingests')
  return data.ingests.map((item) => mapIngestSummary(item))
}

function ingestSourceBody(input: IngestSourceInput): JsonObject {
  return {
    name: input.name,
    kind: input.kind,
    enabled: input.enabled,
    source_root: input.sourceRoot,
    namespace: input.namespace,
    rules: input.rules ?? {},
    labels: input.labels ?? {},
  }
}

/** GET /v1/ingests/get — the full record (incl. raw rules) for one ingest source. */
export async function fetchIngest(name: string): Promise<IngestRecord> {
  const data = await apiFetch<{ ingest: IngestRecord }>('/v1/ingests/get', undefined, { name })
  return mapIngestRecord(data.ingest)
}

/** POST /v1/ingests/create — add a new ingest source. */
export async function createIngest(input: IngestSourceInput): Promise<IngestSummary> {
  const data = await postJson<{ ingest: IngestSummary }>(
    '/v1/ingests/create',
    ingestSourceBody(input),
  )
  return mapIngestSummary(data.ingest)
}

/** POST /v1/ingests/update — full-record replace of an ingest source, keyed by name. */
export async function updateIngest(input: IngestSourceInput): Promise<IngestSummary> {
  const data = await postJson<{ ingest: IngestSummary }>(
    '/v1/ingests/update',
    ingestSourceBody(input),
  )
  return mapIngestSummary(data.ingest)
}

/** POST /v1/ingests/delete — remove an ingest source by name. */
export async function deleteIngest(name: string): Promise<string> {
  const data = await postJson<{ deleted: string }>('/v1/ingests/delete', { name })
  return data.deleted
}

/** POST /v1/ingests/set-enabled — toggle an ingest source on or off. */
export async function setIngestEnabled(name: string, enabled: boolean): Promise<IngestSummary> {
  const data = await postJson<{ ingest: IngestSummary }>('/v1/ingests/set-enabled', {
    name,
    enabled,
  })
  return mapIngestSummary(data.ingest)
}

/** POST /v1/ingests/run — enqueues a run for every enabled ingest. Returns the queued runs. */
export async function runIngests(): Promise<EnqueuedIngestRun[]> {
  const data = await postJson<{ runs: unknown[] }>('/v1/ingests/run')
  return (data.runs ?? []).map((item) => mapEnqueuedIngestRun(item))
}

/** POST /v1/ingests/run-ingest — enqueues a run for one ingest by name. */
export async function runIngest(name: string): Promise<EnqueuedIngestRun> {
  const data = await postJson<EnqueuedIngestRun>('/v1/ingests/run-ingest', { name })
  return mapEnqueuedIngestRun(data)
}

/** GET /v1/ingests/runs — recent ingest runs, newest first. */
export async function fetchIngestRuns(limit = 50): Promise<IngestRunRecord[]> {
  const data = await apiFetch<{ runs: unknown[] }>('/v1/ingests/runs', undefined, { limit })
  return (data.runs ?? []).map((item) => mapIngestRunRecord(item))
}

/** GET /v1/ingests/schedules — all ingest cron schedules. */
export async function fetchIngestSchedules(): Promise<IngestSchedule[]> {
  const data = await apiFetch<{ schedules: unknown[] }>('/v1/ingests/schedules')
  return (data.schedules ?? []).map((item) => mapIngestSchedule(item))
}

/** POST /v1/ingests/schedules — create a cron schedule. */
export async function createIngestSchedule(input: IngestScheduleInput): Promise<IngestSchedule> {
  const data = await postJson<{ schedule: IngestSchedule }>('/v1/ingests/schedules', {
    ingest_name: input.ingestName,
    cron_expr: input.cronExpr,
    enabled: input.enabled,
  })
  return mapIngestSchedule(data.schedule)
}

/** POST /v1/ingests/schedules/update — update a cron schedule by id. */
export async function updateIngestSchedule(
  id: string,
  input: IngestScheduleInput,
): Promise<IngestSchedule> {
  const data = await postJson<{ schedule: IngestSchedule }>('/v1/ingests/schedules/update', {
    id,
    ingest_name: input.ingestName,
    cron_expr: input.cronExpr,
    enabled: input.enabled,
  })
  return mapIngestSchedule(data.schedule)
}

/** POST /v1/ingests/schedules/delete — delete a cron schedule by id. */
export async function deleteIngestSchedule(id: string): Promise<string> {
  const data = await postJson<{ deleted: string }>('/v1/ingests/schedules/delete', { id })
  return data.deleted
}

/** POST /v1/ingests/validate — dry-check one ingest source by name. */
export async function validateIngest(name: string): Promise<unknown> {
  const data = await postJson<{ validation: unknown }>('/v1/ingests/validate', { name })
  return data.validation
}

/** POST /v1/ingests/preview — preview fragments one ingest would pull. */
export async function previewIngest(name: string, limit = 10): Promise<unknown> {
  const data = await postJson<{ preview: unknown }>('/v1/ingests/preview', { name, limit })
  return data.preview
}

export async function fetchEntityFragments(
  params: FetchEntityFragmentsParams,
): Promise<SearchResult[]> {
  const data = await apiFetch<{ results: SearchResult[] }>(
    '/v1/entities/fragments',
    undefined,
    {
      kind: params.kind,
      value: params.value,
    },
  )
  return data.results.map((item) => normalizeSearchResult(item))
}

export async function fetchRoutes(): Promise<Route[]> {
  const data = await apiFetch<{ items: Route[] }>('/v1/routes')
  return data.items.map((item) => mapRoute(item))
}

export async function fetchRoutePreview(
  params: FetchRoutePreviewParams,
): Promise<RoutePreviewResult> {
  const data = await apiFetch<{ item: RoutePreviewResult }>('/v1/routes/preview', undefined, {
    'route-id': params.routeId,
    route_id: params.routeId,
  })
  return mapRoutePreviewResult(data.item)
}

export async function renameRoute(input: RenameRouteInput): Promise<Route> {
  const data = await postJson<{ item: Route }>('/v1/routes/rename', {
    route_id: input.routeId,
    name: input.name,
  })
  return mapRoute(data.item)
}

export async function deleteRoute(input: DeleteRouteInput): Promise<RouteDeleteResult> {
  const data = await postJson<{ item: RouteDeleteResult }>('/v1/routes/delete', {
    route_id: input.routeId,
    force: input.force ?? false,
  })
  return mapRouteDeleteResult(data.item)
}

export async function applyRouteEntity(input: ApplyRouteEntityInput): Promise<RouteApplyResult> {
  const data = await postJson<{ result: RouteApplyResult }>('/v1/routes/apply-entity', {
    route_id: input.routeId,
    kind: input.kind,
    value: input.value,
    ...(input.limit !== undefined ? { limit: input.limit } : {}),
  })
  return mapRouteApplyResult(data.result)
}

export interface CreateRouteInput {
  name: string
  matchSource?: string
  matchType?: string
  matchEntityKind?: string
  matchEntityValue?: string
  destinationId: string
  autoRoute?: boolean
  confidenceMin?: number
}

export async function createRoute(input: CreateRouteInput): Promise<Route> {
  const data = await postJson<{ item: Route }>('/v1/routes/create', {
    name: input.name,
    match_source: input.matchSource ?? '',
    match_type: input.matchType ?? '',
    match_entity_kind: input.matchEntityKind ?? '',
    match_entity_value: input.matchEntityValue ?? '',
    destination_id: input.destinationId,
    auto_route: input.autoRoute ?? false,
    confidence_min: input.confidenceMin ?? 0,
  })
  return mapRoute(data.item)
}

export interface CreateDestinationInput {
  name: string
  kind: string
  configJson: string
}

export async function createDestination(input: CreateDestinationInput): Promise<Destination> {
  const data = await postJson<{ item: Destination }>('/v1/destinations/create', {
    name: input.name,
    kind: input.kind,
    config_json: input.configJson,
  })
  return mapDestination(data.item)
}

export async function fetchDestinations(): Promise<Destination[]> {
  const data = await apiFetch<{ items: Destination[] }>('/v1/destinations')
  return data.items.map((item) => mapDestination(item))
}

export async function fetchDestinationStatus(
  params: FetchDestinationStatusParams,
): Promise<DestinationStatusRecord> {
  const data = await apiFetch<{ item: DestinationStatusRecord }>(
    '/v1/destinations/status',
    undefined,
    {
      'destination-id': params.destinationId,
      destination_id: params.destinationId,
    },
  )
  return mapDestinationStatusRecord(data.item)
}

export async function validateDestination(
  input: ValidateDestinationInput,
): Promise<DestinationValidationResult> {
  const data = await postJson<{ item: DestinationValidationResult }>(
    '/v1/destinations/validate',
    {
      ...(input.name ? { name: input.name } : {}),
      kind: input.kind,
      config_json: input.configJson,
    },
  )
  return mapDestinationValidationResult(data.item)
}

export async function renameDestination(input: RenameDestinationInput): Promise<Destination> {
  const data = await postJson<{ item: Destination }>('/v1/destinations/rename', {
    destination_id: input.destinationId,
    name: input.name,
  })
  return mapDestination(data.item)
}

export async function deleteDestination(
  input: DeleteDestinationInput,
): Promise<DestinationDeleteResult> {
  const data = await postJson<{ item: DestinationDeleteResult }>('/v1/destinations/delete', {
    destination_id: input.destinationId,
    force: input.force ?? false,
  })
  return mapDestinationDeleteResult(data.item)
}

export async function retryDestination(input: RetryDestinationInput): Promise<Destination> {
  const data = await postJson<{ item: Destination }>('/v1/destinations/retry', {
    destination_id: input.destinationId,
    max_attempts: input.max_attempts,
    backoff_ms: input.backoff_ms,
  })
  return mapDestination(data.item)
}

export async function updateDestinationQueuePolicy(
  input: UpdateDestinationQueuePolicyInput,
): Promise<Destination> {
  const data = await postJson<{ item: Destination }>('/v1/destinations/queue-policy', {
    destination_id: input.destinationId,
    replay_cooldown_seconds: input.replay_cooldown_seconds,
    max_replays_per_hour: input.max_replays_per_hour,
    alert_pending_threshold: input.alert_pending_threshold,
    alert_dead_letter_threshold: input.alert_dead_letter_threshold,
  })
  return mapDestination(data.item)
}

export async function fetchQueueStatus(): Promise<QueueStats> {
  const data = await apiFetch<{ stats: QueueStats }>('/v1/queue/status')
  return mapQueueStats(data.stats)
}

export async function fetchPendingQueue(
  params: QueueFilterParams = {},
): Promise<QueuePendingItem[]> {
  const data = await apiFetch<{ items: QueuePendingItem[] }>('/v1/queue/pending', undefined, {
    'destination-id': params.destinationId,
    destination_id: params.destinationId,
  })
  return data.items.map((item) => mapQueuePendingItem(item))
}

export async function fetchFailedQueue(
  params: QueueFilterParams = {},
): Promise<QueueFailedItem[]> {
  const data = await apiFetch<{ items: QueueFailedItem[] }>('/v1/queue/failed', undefined, {
    'destination-id': params.destinationId,
    destination_id: params.destinationId,
  })
  return data.items.map((item) => mapQueueFailedItem(item))
}

export async function fetchQueueEvents(
  params: QueueFilterParams = {},
): Promise<QueueEvent[]> {
  const data = await apiFetch<{ items: QueueEvent[] }>('/v1/queue/events', undefined, {
    'destination-id': params.destinationId,
    destination_id: params.destinationId,
  })
  return data.items.map((item) => mapQueueEvent(item))
}

export async function fetchQueueDestinations(): Promise<QueueDestinationSummary[]> {
  const data = await apiFetch<{ items: QueueDestinationSummary[] }>('/v1/queue/destinations')
  return data.items.map((item) => mapQueueDestinationSummary(item))
}

export async function drainQueue(input: DrainQueueInput = {}): Promise<QueueStats> {
  const data = await postJson<{ processed: number; stats: QueueStats }>('/v1/queue/drain', {
    ...(input.limit !== undefined ? { limit: input.limit } : {}),
  })
  return mapQueueStats(data.stats)
}

export async function replayQueue(input: QueueFailedActionInput): Promise<QueueStats> {
  const data = await postJson<{ replayed: number; stats: QueueStats }>('/v1/queue/replay', {
    id: input.id,
    force: input.force ?? false,
  })
  return mapQueueStats(data.stats)
}

export async function purgeQueue(input: QueueFailedActionInput): Promise<QueueStats> {
  const data = await postJson<{ purged: number; stats: QueueStats }>('/v1/queue/purge', {
    id: input.id,
    force: input.force ?? false,
  })
  return mapQueueStats(data.stats)
}

export const apiClient = {
  fetchInbox,
  fetchInboxEntities,
  fetchInboxEntityItems,
  fetchFragment,
  fetchRelatedFragments,
  reanalyzeFragmentAttachments,
  searchFragments,
  searchFragmentsDetailed,
  createIntake,
  fetchIngestJobs,
  fetchWorkersStatus,
  fetchEngineConfig,
  updateEngineConfig,
  fetchRecallStatus,
  fetchEntities,
  fetchIngests,
  fetchIngest,
  createIngest,
  updateIngest,
  deleteIngest,
  setIngestEnabled,
  runIngests,
  runIngest,
  fetchIngestRuns,
  fetchIngestSchedules,
  createIngestSchedule,
  updateIngestSchedule,
  deleteIngestSchedule,
  validateIngest,
  previewIngest,
  fetchEntityFragments,
  fetchRoutes,
  fetchRoutePreview,
  createRoute,
  createDestination,
  renameRoute,
  deleteRoute,
  applyRouteEntity,
  fetchDestinations,
  fetchDestinationStatus,
  validateDestination,
  renameDestination,
  deleteDestination,
  retryDestination,
  updateDestinationQueuePolicy,
  fetchQueueStatus,
  fetchPendingQueue,
  fetchFailedQueue,
  fetchQueueEvents,
  fetchQueueDestinations,
  drainQueue,
  replayQueue,
  purgeQueue,
}

export type ApiClient = typeof apiClient
