const KEY = 'sysop:inbox:filters:v1'

/**
 * Route-state filter tri-state.
 * - `both`     = no filter (default)
 * - `routed`   = only fragments with a route assigned
 * - `unrouted` = only fragments still awaiting a route
 */
export type RouteFilter = 'both' | 'routed' | 'unrouted'
export type VisualFilter = 'all' | 'visual' | 'pins'
export type MaterializedFilter = 'all' | 'materialized' | 'pending'

const ROUTE_VALUES: readonly RouteFilter[] = ['both', 'routed', 'unrouted'] as const
const VISUAL_VALUES: readonly VisualFilter[] = ['all', 'visual', 'pins'] as const
const MATERIALIZED_VALUES: readonly MaterializedFilter[] = [
  'all',
  'materialized',
  'pending',
] as const

export function parseRouteFilter(raw: unknown): RouteFilter {
  if (typeof raw !== 'string') return 'both'
  return (ROUTE_VALUES as readonly string[]).includes(raw) ? (raw as RouteFilter) : 'both'
}

export function parseVisualFilter(raw: unknown): VisualFilter {
  if (typeof raw !== 'string') return 'all'
  return (VISUAL_VALUES as readonly string[]).includes(raw) ? (raw as VisualFilter) : 'all'
}

export function parseMaterializedFilter(raw: unknown): MaterializedFilter {
  if (typeof raw !== 'string') return 'all'
  return (MATERIALIZED_VALUES as readonly string[]).includes(raw)
    ? (raw as MaterializedFilter)
    : 'all'
}

/** Selected entity facet, encoded as `kind` + `value`. */
export interface EntitySelection {
  kind: string
  value: string
}

export interface InboxFilters {
  /** Free-text search over title / reason / fragment id / source. */
  search: string
  /** Active fragment-status chips. Empty = show all statuses. */
  statuses: string[]
  /** Route-assignment tri-state. */
  route: RouteFilter
  /** Entity facet, or null when unfiltered. */
  entity: EntitySelection | null
  /** Visual narrowing for image-backed work, including Pinterest pins. */
  visual: VisualFilter
  /** Whether a fragment has already been materialized into an output destination. */
  materialized: MaterializedFilter
}

export const EMPTY_INBOX_FILTERS: InboxFilters = {
  search: '',
  statuses: [],
  route: 'both',
  entity: null,
  visual: 'all',
  materialized: 'all',
}

export function saveInboxFilters(filters: InboxFilters): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(filters))
  } catch {
    // localStorage may be unavailable (private mode / quota) — fail quiet.
  }
}

export function readInboxFilters(): InboxFilters | null {
  try {
    const raw = localStorage.getItem(KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as unknown
    if (!parsed || typeof parsed !== 'object') return null
    const f = parsed as Record<string, unknown>
    const statuses = Array.isArray(f.statuses)
      ? (f.statuses as unknown[]).filter((s): s is string => typeof s === 'string')
      : []
    let entity: EntitySelection | null = null
    if (f.entity && typeof f.entity === 'object') {
      const e = f.entity as Record<string, unknown>
      if (typeof e.kind === 'string' && typeof e.value === 'string') {
        entity = { kind: e.kind, value: e.value }
      }
    }
    return {
      search: typeof f.search === 'string' ? f.search : '',
      statuses,
      route: parseRouteFilter(f.route),
      entity,
      visual: parseVisualFilter(f.visual),
      materialized: parseMaterializedFilter(f.materialized),
    }
  } catch {
    return null
  }
}

export function clearInboxFilters(): void {
  try {
    localStorage.removeItem(KEY)
  } catch {
    // ignore
  }
}
