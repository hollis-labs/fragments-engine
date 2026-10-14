/** Types and pure helpers for the owner-only Chats surface (/v1/transcripts). */

export interface ChatSession {
  id: string
  tool: string
  kind: 'main' | 'subagent' | string
  parent_id?: string
  agent_type?: string
  model?: string
  project?: string
  git_branch?: string
  title: string
  started_at?: string
  ended_at?: string
  turns: number
  tokens: number
  tool_calls: number
  errors: number
  snippet?: string
  /** Cosine similarity (semantic) or fused rank score (hybrid). */
  score?: number
  /** Why this chat matched a summary, semantic or hybrid search. */
  match?: ChatMatch
  /** Enrichment fields appear once the enrichment pipeline has run. */
  enriched?: boolean
  tags?: string[]
}

export interface ChatMatch {
  /** title, topics, entities, summary, decisions, follow_ups, transcript or semantic. */
  field: string
  /** Hybrid only: every list that contributed. */
  sources?: string[]
  /** Short excerpt; matched words are wrapped in [ ]. */
  excerpt?: string
}

export interface ChatSessionPage {
  items: ChatSession[]
  next_cursor?: string
  /** 'too_broad' when a search could not finish in time; refine it. */
  hint?: string
  /** Results listed without a snippet (very large documents or time budget). */
  snippets_skipped?: number
  /** The mode that produced the items; differs from the request when one was unavailable. */
  mode?: string
  /** Stable code explaining a fallback or an empty semantic result. */
  notice?: string
}

export interface ChatToolCall {
  name: string
  status: string
  result_bytes: number
  started_at?: string
  ended_at?: string
}

export interface ChatTurn {
  seq: number
  role: string
  ts?: string
  is_sidechain?: boolean
  stop_reason?: string
  text: string
  text_truncated?: boolean
  has_thinking?: boolean
  thinking_bytes?: number
  event_id: string
  tool_calls?: ChatToolCall[]
}

export interface ChatTreeNode extends ChatSession {
  children?: ChatTreeNode[]
}

export interface ChatUsage {
  requests: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
}

export interface ChatDecision {
  text: string
  /** Turn sequence numbers the statement cites. */
  turns?: number[]
}

export interface ChatEnrichment {
  summary?: string
  decisions?: ChatDecision[]
  follow_ups?: ChatDecision[]
  tags?: string[]
  session_type?: string
  outcome?: string
}

export interface ChatDetail {
  session: ChatSession
  usage: ChatUsage
  turns: ChatTurn[]
  turn_total: number
  next_turn?: number
  subagents: ChatTreeNode[]
  enrichment?: ChatEnrichment
}

export interface ChatFacets {
  tools: string[]
  models: string[]
  project_count: number
  tags?: string[]
  /** Enrichment topics with how many chats carry each, most used first. */
  tag_counts?: { tag: string; count: number }[]
  /** Chats that have a current enrichment. */
  enriched?: number
  total: number
}

export interface ChatProjectCount {
  project: string
  sessions: number
}

export interface ChatStatus {
  enabled: boolean
  available: boolean
  /** True only when the embedding server answers and vectors match its model. */
  semantic: boolean
  semantic_notice?: string
  auth: string
}

export type ChatSearchMode = 'auto' | 'text' | 'summary' | 'semantic' | 'hybrid'
export type ChatRequestMode = Exclude<ChatSearchMode, 'auto'>

export const CHAT_SEARCH_MODES: { value: ChatSearchMode; label: string; hint: string }[] = [
  { value: 'auto', label: 'Auto', hint: 'Hybrid when semantic search is available, otherwise transcript text' },
  { value: 'text', label: 'Text', hint: 'Words in the chat transcripts' },
  { value: 'summary', label: 'Summary', hint: 'Titles, summaries, decisions, follow-ups, topics and entities' },
  { value: 'semantic', label: 'Semantic', hint: 'Meaning-based match on summaries' },
  { value: 'hybrid', label: 'Hybrid', hint: 'Semantic, summary and transcript matches combined' },
]

/** The mode to request: Auto picks Hybrid only when semantic search works. */
export function resolveSearchMode(mode: ChatSearchMode, semanticAvailable: boolean): ChatRequestMode {
  return mode === 'auto' ? (semanticAvailable ? 'hybrid' : 'text') : mode
}

const NOTICE_TEXT: Record<string, string> = {
  semantic_unavailable: 'Semantic search is unavailable: the local embedding server is not reachable or not configured.',
  no_vectors: 'Semantic search is unavailable: no summaries have been embedded yet.',
  model_mismatch: 'Semantic search is unavailable: the stored vectors were made with a different embedding model.',
  dim_mismatch: 'Semantic search is unavailable: the embedding server returns a different vector size than the stored vectors.',
  transcript_too_broad: 'Transcript text was too broad to include; showing the other matches.',
}

/** Owner-facing text for a page notice code, or undefined for none/unknown. */
export function noticeText(code?: string): string | undefined {
  return code ? NOTICE_TEXT[code] : undefined
}

const FIELD_LABELS: Record<string, string> = {
  follow_ups: 'follow-up',
  decisions: 'decision',
  topics: 'topic',
  entities: 'entity',
}

export function matchFieldLabel(field: string): string {
  return FIELD_LABELS[field] ?? field
}

/** Split text marked with [ ] into plain and highlighted runs. */
export function highlightParts(text: string): { text: string; hit: boolean }[] {
  const parts: { text: string; hit: boolean }[] = []
  let rest = text
  while (rest) {
    const open = rest.indexOf('[')
    const close = open >= 0 ? rest.indexOf(']', open + 1) : -1
    if (open < 0 || close < 0) {
      parts.push({ text: rest, hit: false })
      break
    }
    if (open > 0) parts.push({ text: rest.slice(0, open), hit: false })
    parts.push({ text: rest.slice(open + 1, close), hit: true })
    rest = rest.slice(close + 1)
  }
  return parts
}

export type ChatSortKey = 'started' | 'turns' | 'tokens' | 'tool_calls' | 'errors'
export type ChatKindFilter = 'all' | 'main' | 'subagent'
export type ChatErrorFilter = 'all' | 'errors' | 'clean'
export type ChatEnrichedFilter = 'all' | 'yes' | 'no'

export interface ChatFilters {
  search: string
  /** How the search box is interpreted; not counted as a narrowing filter. */
  mode: ChatSearchMode
  tool: string
  model: string
  project: string
  kind: ChatKindFilter
  errors: ChatErrorFilter
  enriched: ChatEnrichedFilter
  tag: string
  since: string
  until: string
  sort: ChatSortKey
  desc: boolean
}

export const EMPTY_CHAT_FILTERS: ChatFilters = {
  search: '',
  mode: 'auto',
  tool: '',
  model: '',
  project: '',
  kind: 'all',
  errors: 'all',
  enriched: 'all',
  tag: '',
  since: '',
  until: '',
  sort: 'started',
  desc: true,
}

const SEARCH_MODES: readonly ChatSearchMode[] = ['auto', 'text', 'summary', 'semantic', 'hybrid']
const SORT_KEYS: readonly ChatSortKey[] = ['started', 'turns', 'tokens', 'tool_calls', 'errors']

export function parseChatFilters(raw: unknown): ChatFilters {
  if (typeof raw !== 'object' || raw === null) return { ...EMPTY_CHAT_FILTERS }
  const r = raw as Record<string, unknown>
  const str = (v: unknown) => (typeof v === 'string' ? v : '')
  return {
    search: str(r.search),
    mode: SEARCH_MODES.includes(r.mode as ChatSearchMode) ? (r.mode as ChatSearchMode) : 'auto',
    tool: str(r.tool),
    model: str(r.model),
    project: str(r.project),
    kind: r.kind === 'main' || r.kind === 'subagent' ? r.kind : 'all',
    errors: r.errors === 'errors' || r.errors === 'clean' ? r.errors : 'all',
    enriched: r.enriched === 'yes' || r.enriched === 'no' ? r.enriched : 'all',
    tag: str(r.tag),
    since: str(r.since),
    until: str(r.until),
    sort: SORT_KEYS.includes(r.sort as ChatSortKey) ? (r.sort as ChatSortKey) : 'started',
    desc: r.desc !== false,
  }
}

/** Count of narrowing filters (search and sort excluded) for the filter bar badge. */
export function activeChatFilterCount(f: ChatFilters): number {
  return [f.tool, f.model, f.project, f.tag, f.since, f.until].filter(Boolean).length +
    (f.kind !== 'all' ? 1 : 0) + (f.errors !== 'all' ? 1 : 0) + (f.enriched !== 'all' ? 1 : 0)
}

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

export function formatBytes(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`
  if (n >= 1 << 10) return `${(n / (1 << 10)).toFixed(1)} KB`
  return `${n} B`
}

/** Last path segment of a working directory, for the compact Project column. */
export function projectName(cwd?: string): string {
  if (!cwd) return ''
  const parts = cwd.split('/').filter(Boolean)
  return parts[parts.length - 1] ?? cwd
}

/** Flatten a subagent tree depth-first with indentation depth. */
export function flattenTree(
  nodes: ChatTreeNode[],
  depth = 0,
): { node: ChatTreeNode; depth: number }[] {
  return nodes.flatMap((node) => [{ node, depth }, ...flattenTree(node.children ?? [], depth + 1)])
}

/** Move the keyboard selection by delta rows, clamped; starts at the top/bottom. */
export function moveSelection(ids: string[], current: string | null, delta: 1 | -1): string | null {
  if (ids.length === 0) return null
  const at = current ? ids.indexOf(current) : -1
  if (at < 0) return delta === 1 ? ids[0] : ids[ids.length - 1]
  return ids[Math.min(ids.length - 1, Math.max(0, at + delta))]
}

/** True when a key press is aimed at a text field, so shortcuts must not fire. */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false
  return target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(target.tagName)
}
