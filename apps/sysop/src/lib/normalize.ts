import type {
  Fragment,
  FragmentAttachment,
  FragmentEntity,
  FragmentRelation,
  InboxEntityGroup,
  InboxItem,
  JsonObject,
  SearchResult,
} from './types'

const ZERO_TIME_RFC3339 = '0001-01-01T00:00:00Z'

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

export function toSnakeCase(value: string) {
  return value
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .replace(/([A-Z]+)([A-Z][a-z])/g, '$1_$2')
    .replace(/-/g, '_')
    .toLowerCase()
}

export function normalizeKeys<T>(value: T): T {
  if (Array.isArray(value)) {
    return value.map((item) => normalizeKeys(item)) as T
  }

  if (isRecord(value)) {
    const entries = Object.entries(value).map(([key, item]) => [
      toSnakeCase(key),
      normalizeKeys(item),
    ])
    return Object.fromEntries(entries) as T
  }

  return value
}

export function parseMetadataJson(value: unknown): JsonObject {
  if (isRecord(value)) {
    return value as JsonObject
  }

  if (typeof value !== 'string' || value.trim() === '') {
    return {}
  }

  try {
    const parsed = JSON.parse(value)
    return isRecord(parsed) ? (parsed as JsonObject) : {}
  } catch {
    return {}
  }
}

export function normalizeDateString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function normalizeOptionalDateString(value: unknown): string | undefined {
  if (typeof value !== 'string') {
    return undefined
  }

  if (value === '' || value === ZERO_TIME_RFC3339) {
    return undefined
  }

  return value
}

function normalizeStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined
  }

  return value.filter((item): item is string => typeof item === 'string')
}

function normalizeNumber(value: unknown): number | undefined {
  return typeof value === 'number' ? value : undefined
}

function normalizeBoolean(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined
}

export function normalizeFragment(value: unknown): Fragment {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  const metadataJson = typeof normalized.metadata_json === 'string' ? normalized.metadata_json : ''

  return {
    id: typeof normalized.id === 'string' ? normalized.id : '',
    source: typeof normalized.source === 'string' ? normalized.source : '',
    source_type: typeof normalized.source_type === 'string' ? normalized.source_type : '',
    source_id: typeof normalized.source_id === 'string' ? normalized.source_id : '',
    title: typeof normalized.title === 'string' ? normalized.title : '',
    content: typeof normalized.content === 'string' ? normalized.content : '',
    content_hash: typeof normalized.content_hash === 'string' ? normalized.content_hash : '',
    created_at: normalizeDateString(normalized.created_at),
    ingested_at: normalizeDateString(normalized.ingested_at),
    status:
      normalized.status === 'inbox' ||
      normalized.status === 'routed' ||
      normalized.status === 'indexed'
        ? normalized.status
        : 'inbox',
    summary: typeof normalized.summary === 'string' ? normalized.summary : '',
    indexed_at: normalizeOptionalDateString(normalized.indexed_at),
    metadata_json: metadataJson,
    metadata: parseMetadataJson(metadataJson),
    ingest_name: typeof normalized.ingest_name === 'string' ? normalized.ingest_name : '',
    canonical_path: typeof normalized.canonical_path === 'string' ? normalized.canonical_path : '',
  }
}

export function normalizeFragmentAttachment(value: unknown): FragmentAttachment {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  const metadataJson =
    typeof normalized.metadata_json === 'string' ? normalized.metadata_json : undefined
  const metadata =
    metadataJson !== undefined
      ? parseMetadataJson(metadataJson)
      : parseMetadataJson(normalized.metadata)

  return {
    id: typeof normalized.id === 'string' ? normalized.id : '',
    kind: typeof normalized.kind === 'string' ? normalized.kind : '',
    role: typeof normalized.role === 'string' ? normalized.role : '',
    name: typeof normalized.name === 'string' ? normalized.name : '',
    mime_type: typeof normalized.mime_type === 'string' ? normalized.mime_type : '',
    source_path:
      typeof normalized.source_path === 'string' ? normalized.source_path : undefined,
    external_url:
      typeof normalized.external_url === 'string' ? normalized.external_url : undefined,
    storage_path:
      typeof normalized.storage_path === 'string' ? normalized.storage_path : undefined,
    preview_storage_path:
      typeof normalized.preview_storage_path === 'string'
        ? normalized.preview_storage_path
        : undefined,
    size_bytes: normalizeNumber(normalized.size_bytes),
    source: typeof normalized.source === 'string' ? normalized.source : '',
    source_item_id:
      typeof normalized.source_item_id === 'string' ? normalized.source_item_id : undefined,
    metadata_json: metadataJson,
    metadata,
    analysis_summary:
      typeof normalized.analysis_summary === 'string' ? normalized.analysis_summary : undefined,
    analysis_tags: normalizeStringArray(normalized.analysis_tags),
    vision_backend:
      typeof normalized.vision_backend === 'string' ? normalized.vision_backend : undefined,
    vision_summary:
      typeof normalized.vision_summary === 'string' ? normalized.vision_summary : undefined,
    vision_tags: normalizeStringArray(normalized.vision_tags),
    vision_entities: normalizeStringArray(normalized.vision_entities),
    vision_text_present: normalizeBoolean(normalized.vision_text_present),
    vision_confidence: normalizeNumber(normalized.vision_confidence),
    extracted_text_preview:
      typeof normalized.extracted_text_preview === 'string'
        ? normalized.extracted_text_preview
        : undefined,
    extracted_text_bytes: normalizeNumber(normalized.extracted_text_bytes),
    ocr_status: typeof normalized.ocr_status === 'string' ? normalized.ocr_status : undefined,
    created_at: normalizeDateString(normalized.created_at),
  }
}

export function normalizeFragmentEntity(value: unknown): FragmentEntity {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    kind: typeof normalized.kind === 'string' ? normalized.kind : '',
    value: typeof normalized.value === 'string' ? normalized.value : '',
    source: typeof normalized.source === 'string' ? normalized.source : '',
    confidence: typeof normalized.confidence === 'number' ? normalized.confidence : 0,
  }
}

export function normalizeFragmentRelation(value: unknown): FragmentRelation {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    fragment_id: typeof normalized.fragment_id === 'string' ? normalized.fragment_id : undefined,
    target_id:
      typeof normalized.target_id === 'string'
        ? normalized.target_id
        : typeof normalized.related_fragment_id === 'string'
          ? normalized.related_fragment_id
          : '',
    kind: typeof normalized.kind === 'string' ? normalized.kind : '',
    score: normalizeNumber(normalized.score),
    metadata_json:
      typeof normalized.metadata_json === 'string' ? normalized.metadata_json : undefined,
    created_at: normalizeOptionalDateString(normalized.created_at),
  }
}

export function normalizeSearchResult(value: unknown): SearchResult {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    fragment: normalizeFragment(normalized.fragment),
    score: typeof normalized.score === 'number' ? normalized.score : 0,
    snippet: typeof normalized.snippet === 'string' ? normalized.snippet : '',
    recall_trace: normalizeKeys(
      normalized.recall_trace ?? normalized.trace ?? {},
    ) as SearchResult['recall_trace'],
  }
}

export function normalizeInboxItem(value: unknown): InboxItem {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  const str = (key: string) => (typeof normalized[key] === 'string' ? (normalized[key] as string) : '')
  return {
    fragment_id: str('fragment_id'),
    reason: str('reason'),
    staged_at: normalizeDateString(normalized.staged_at),
    route_id: str('route_id'),
    title: str('title'),
    source: str('source'),
    source_type: str('source_type'),
    status: str('status'),
    created_at: normalizeDateString(normalized.created_at),
  }
}

function normalizeInboxEntityGroup(value: unknown): InboxEntityGroup {
  const normalized = normalizeKeys(value) as Record<string, unknown>
  return {
    kind: typeof normalized.kind === 'string' ? normalized.kind : '',
    value: typeof normalized.value === 'string' ? normalized.value : '',
    fragment_count:
      typeof normalized.fragment_count === 'number' ? normalized.fragment_count : 0,
  }
}

export function normalizeInboxEntityGroups(value: unknown): InboxEntityGroup[] {
  if (!Array.isArray(value)) {
    return []
  }

  return value.flatMap((item) => {
    if (Array.isArray(item)) {
      return normalizeInboxEntityGroups(item)
    }

    return [normalizeInboxEntityGroup(item)]
  })
}
