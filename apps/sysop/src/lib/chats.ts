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
  /** Enrichment fields appear once the enrichment pipeline has run. */
  tags?: string[]
  summary?: string
}

export interface ChatSessionPage {
  items: ChatSession[]
  next_cursor?: string
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
  projects: string[]
  total: number
}

export interface ChatStatus {
  enabled: boolean
  available: boolean
  semantic: boolean
  auth: string
}

export type ChatSortKey = 'started' | 'turns' | 'tokens' | 'tool_calls' | 'errors'
export type ChatKindFilter = 'all' | 'main' | 'subagent'
export type ChatErrorFilter = 'all' | 'errors' | 'clean'

export interface ChatFilters {
  search: string
  tool: string
  model: string
  project: string
  kind: ChatKindFilter
  errors: ChatErrorFilter
  since: string
  until: string
  sort: ChatSortKey
  desc: boolean
}

export const EMPTY_CHAT_FILTERS: ChatFilters = {
  search: '',
  tool: '',
  model: '',
  project: '',
  kind: 'all',
  errors: 'all',
  since: '',
  until: '',
  sort: 'started',
  desc: true,
}

const SORT_KEYS: readonly ChatSortKey[] = ['started', 'turns', 'tokens', 'tool_calls', 'errors']

export function parseChatFilters(raw: unknown): ChatFilters {
  if (typeof raw !== 'object' || raw === null) return { ...EMPTY_CHAT_FILTERS }
  const r = raw as Record<string, unknown>
  const str = (v: unknown) => (typeof v === 'string' ? v : '')
  return {
    search: str(r.search),
    tool: str(r.tool),
    model: str(r.model),
    project: str(r.project),
    kind: r.kind === 'main' || r.kind === 'subagent' ? r.kind : 'all',
    errors: r.errors === 'errors' || r.errors === 'clean' ? r.errors : 'all',
    since: str(r.since),
    until: str(r.until),
    sort: SORT_KEYS.includes(r.sort as ChatSortKey) ? (r.sort as ChatSortKey) : 'started',
    desc: r.desc !== false,
  }
}

/** Count of narrowing filters (search and sort excluded) for the filter bar badge. */
export function activeChatFilterCount(f: ChatFilters): number {
  return [f.tool, f.model, f.project, f.since, f.until].filter(Boolean).length +
    (f.kind !== 'all' ? 1 : 0) + (f.errors !== 'all' ? 1 : 0)
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
