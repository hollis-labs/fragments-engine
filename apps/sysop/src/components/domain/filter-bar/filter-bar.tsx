import { Tag } from 'lucide-react'
import {
  FilterBar as KitFilterBar,
  FilterChipGroup,
  FilterCycleToggle,
  FilterEntityCombobox,
  type CycleOption,
  type FilterChip,
  statusTone,
} from '@hollis-labs/sysop-ui'
import type {
  EntitySelection,
  MaterializedFilter,
  RouteFilter,
  VisualFilter,
} from '@/lib/inbox-filters-storage'
import type { InboxEntityGroup } from '@/lib/types'
import type { SearchMode } from '@/lib/api'

const SEARCH_MODES: readonly { value: SearchMode; label: string }[] = [
  { value: 'auto', label: 'Auto' },
  { value: 'semantic', label: 'Semantic' },
  { value: 'keyword', label: 'Keyword' },
]

const ROUTE_CYCLE_OPTIONS: readonly [CycleOption<RouteFilter>, ...CycleOption<RouteFilter>[]] = [
  { value: 'both', label: 'Both', dotColor: 'bg-text-subtle', title: 'All fragments (no route filter)' },
  { value: 'unrouted', label: 'Unrouted', dotColor: 'bg-status-inbox', title: 'Fragments awaiting a route' },
  { value: 'routed', label: 'Routed', dotColor: 'bg-status-routed', title: 'Fragments with a route assigned' },
]

const SEARCH_LIMITS: readonly number[] = [10, 20, 50, 100]
const VISUAL_CYCLE_OPTIONS: readonly [CycleOption<VisualFilter>, ...CycleOption<VisualFilter>[]] = [
  { value: 'all', label: 'All', dotColor: 'bg-text-subtle', title: 'All fragments' },
  { value: 'visual', label: 'Visual', dotColor: 'bg-status-indexed', title: 'Only fragments with preview media' },
  { value: 'pins', label: 'Pins', dotColor: 'bg-status-routed', title: 'Pinterest pin fragments only' },
]
const MATERIALIZED_CYCLE_OPTIONS: readonly [
  CycleOption<MaterializedFilter>,
  ...CycleOption<MaterializedFilter>[],
] = [
  { value: 'all', label: 'All', dotColor: 'bg-text-subtle', title: 'All fragments' },
  {
    value: 'pending',
    label: 'Pending',
    dotColor: 'bg-status-inbox',
    title: 'Not yet materialized',
  },
  {
    value: 'materialized',
    label: 'Saved',
    dotColor: 'bg-status-routed',
    title: 'Already materialized to an output destination',
  },
]

// Entity selections are encoded into a single combobox id; a space separates
// kind from value (entity kinds never contain spaces).
const ENTITY_SEP = ' '

function encodeEntityId(kind: string, value: string): string {
  return `${kind}${ENTITY_SEP}${value}`
}

function decodeEntityId(id: string): EntitySelection | null {
  const sep = id.indexOf(ENTITY_SEP)
  if (sep < 0) return null
  return { kind: id.slice(0, sep), value: id.slice(sep + 1) }
}

interface FilterBarProps {
  availableStatuses: string[]
  activeStatuses: string[]
  onStatusToggle: (status: string) => void
  routeFilter: RouteFilter
  onRouteFilterChange: (value: RouteFilter) => void
  visualFilter: VisualFilter
  onVisualFilterChange: (value: VisualFilter) => void
  materializedFilter: MaterializedFilter
  onMaterializedFilterChange: (value: MaterializedFilter) => void
  entityGroups: InboxEntityGroup[]
  entitySelection: EntitySelection | null
  onEntityChange: (selection: EntitySelection | null) => void
  searchQuery: string
  onSearchChange: (q: string) => void
  searchMatchCount?: number
  activeFilterCount: number
  onClear?: () => void
  /** When searching, route + entity facets are suppressed (inbox-only). */
  searchActive?: boolean
  /** Requested /v1/search retrieval mode. */
  searchMode?: SearchMode
  onSearchModeChange?: (mode: SearchMode) => void
  /** Result limit passed to /v1/search. */
  searchLimit?: number
  onSearchLimitChange?: (limit: number) => void
  /** Concrete strategy the server reported running (`mode_used`). */
  searchModeUsed?: 'semantic' | 'keyword'
}

/**
 * Fragments operations filter bar — a thin app composition over the kit's
 * `FilterBar` shell. The shell owns the search hero + summary + clear; the
 * chip row below is app-specific: status chips (always), search mode/limit
 * (search mode only), route cycle + entity combobox (inbox mode only).
 */
export default function FilterBar({
  availableStatuses,
  activeStatuses,
  onStatusToggle,
  routeFilter,
  onRouteFilterChange,
  visualFilter,
  onVisualFilterChange,
  materializedFilter,
  onMaterializedFilterChange,
  entityGroups,
  entitySelection,
  onEntityChange,
  searchQuery,
  onSearchChange,
  searchMatchCount,
  activeFilterCount,
  onClear,
  searchActive = false,
  searchMode = 'auto',
  onSearchModeChange,
  searchLimit = 20,
  onSearchLimitChange,
  searchModeUsed,
}: FilterBarProps) {
  let summaryText = ''
  if (activeFilterCount > 0 && searchQuery.length > 0) {
    summaryText = `${activeFilterCount} filter${activeFilterCount === 1 ? '' : 's'} · ${searchMatchCount ?? 0} match${searchMatchCount === 1 ? '' : 'es'}`
  } else if (activeFilterCount > 0) {
    summaryText = `${activeFilterCount} filter${activeFilterCount === 1 ? '' : 's'}`
  } else if (searchQuery.length > 0) {
    summaryText = `${searchMatchCount ?? 0} match${searchMatchCount === 1 ? '' : 'es'}`
  }

  // Status chips carry their own tone classes so the active state matches the
  // status badge palette.
  const statusChips: FilterChip[] = availableStatuses.map((status) => {
    const tone = statusTone(status)
    return {
      value: status,
      activeClassName: `${tone.border} ${tone.bg} ${tone.text}`,
    }
  })

  const entityItems = entityGroups.map((g) => ({
    id: encodeEntityId(g.kind, g.value),
    name: `${g.kind}: ${g.value}`,
    count: g.fragment_count,
  }))
  const entityValue = entitySelection
    ? encodeEntityId(entitySelection.kind, entitySelection.value)
    : null

  return (
    <KitFilterBar
      searchQuery={searchQuery}
      onSearchChange={onSearchChange}
      searchPlaceholder="Search fragments by title, reason, or id…"
      searchAriaLabel="Search fragments"
      activeFilterCount={activeFilterCount}
      summary={summaryText}
      onClear={onClear}
    >
      {/* Status chips */}
      <div className="flex flex-wrap items-center gap-1">
        <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">Status:</span>
        {availableStatuses.length === 0 && (
          <span className="text-[10px] text-text-subtle/70">—</span>
        )}
        <FilterChipGroup chips={statusChips} selected={activeStatuses} onToggle={onStatusToggle} />
      </div>

      {/* Search controls — mode toggle + limit, shown only while searching. */}
      {searchActive && (
        <>
          <div className="flex items-center gap-1 border-l border-border pl-3">
            <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">
              Mode:
            </span>
            <div className="inline-flex overflow-hidden rounded border border-border">
              {SEARCH_MODES.map((m) => (
                <button
                  key={m.value}
                  type="button"
                  onClick={() => onSearchModeChange?.(m.value)}
                  aria-pressed={searchMode === m.value}
                  className={`px-2 py-0.5 text-[10px] uppercase tracking-wider transition-colors ${
                    searchMode === m.value
                      ? 'bg-panel-hover text-text'
                      : 'bg-panel-2/50 text-text-subtle hover:text-text-soft'
                  }`}
                >
                  {m.label}
                </button>
              ))}
            </div>
          </div>

          <div className="flex items-center gap-1 border-l border-border pl-3">
            <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">
              Limit:
            </span>
            <select
              value={searchLimit}
              onChange={(e) => onSearchLimitChange?.(Number(e.target.value))}
              aria-label="Search result limit"
              className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-text-soft outline-none transition-colors hover:border-border-strong focus:border-border-strong"
            >
              {SEARCH_LIMITS.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </div>

          {searchModeUsed && (
            <span
              className="border-l border-border pl-3 text-[10px] uppercase tracking-wider text-text-subtle"
              title={`Server ran a ${searchModeUsed} search`}
            >
              ran: <span className="text-text-soft">{searchModeUsed}</span>
            </span>
          )}
        </>
      )}

      <div className="flex items-center gap-1 border-l border-border pl-3">
        <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">
          Visual:
        </span>
        <FilterCycleToggle
          options={VISUAL_CYCLE_OPTIONS}
          value={visualFilter}
          onChange={onVisualFilterChange}
          ariaLabel="Visual filter"
        />
      </div>

      <div className="flex items-center gap-1 border-l border-border pl-3">
        <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">
          Saved:
        </span>
        <FilterCycleToggle
          options={MATERIALIZED_CYCLE_OPTIONS}
          value={materializedFilter}
          onChange={onMaterializedFilterChange}
          ariaLabel="Materialized filter"
        />
      </div>

      {/* Route + entity facets are inbox-only — hidden while searching. */}
      {!searchActive && (
        <>
          {/* Route cycle */}
          <div className="flex items-center gap-1 border-l border-border pl-3">
            <span className="mr-1 text-[10px] uppercase tracking-wider text-text-subtle">
              Route:
            </span>
            <FilterCycleToggle
              options={ROUTE_CYCLE_OPTIONS}
              value={routeFilter}
              onChange={onRouteFilterChange}
              ariaLabel="Route filter"
            />
          </div>

          {/* Entity combobox */}
          <div className="flex flex-wrap items-center gap-2 border-l border-border pl-3">
            <FilterEntityCombobox
              icon={<Tag className="h-3.5 w-3.5" />}
              items={entityItems}
              value={entityValue}
              onChange={(id) => onEntityChange(id ? decodeEntityId(id) : null)}
              allLabel="All entities"
              ariaLabel="Filter by entity"
            />
          </div>
        </>
      )}
    </KitFilterBar>
  )
}
