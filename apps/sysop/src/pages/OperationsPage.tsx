import { useEffect, useMemo, useRef, useState } from 'react'
import { Plus, RefreshCw, Waypoints } from 'lucide-react'
import { PageHeader } from '@/components/domain/page-header'
import { SummaryCards } from '@/components/domain/summary-cards'
import FilterBar from '@/components/domain/filter-bar/filter-bar'
import FragmentTable from '@/components/domain/fragment-table'
import { FragmentDetailDialog } from '@/components/domain/fragment-detail-dialog'
import { ApplyRouteDialog } from '@/components/domain/apply-route-dialog'
import { IntakeDialog } from '@/components/domain/intake-dialog'
import { EmptyState } from '@/components/domain/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/useApi'
import { usePoll } from '@/hooks/usePoll'
import { ApiError, type QueueStats, type SearchMode } from '@/lib/api'
import { FRAGMENT_STATUSES, SUMMARY_ACCENTS } from '@/lib/constants'
import {
  EMPTY_INBOX_FILTERS,
  readInboxFilters,
  saveInboxFilters,
  type EntitySelection,
  type InboxFilters,
  type RouteFilter,
} from '@/lib/inbox-filters-storage'
import type { InboxEntityGroup, InboxItem, SearchResult } from '@/lib/types'

function TableSkeleton() {
  return (
    <div className="flex flex-col gap-2 p-4">
      {Array.from({ length: 6 }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full rounded-md" />
      ))}
    </div>
  )
}

/** Known lifecycle states first, then anything else alphabetically. */
function orderStatuses(statuses: string[]): string[] {
  const known = FRAGMENT_STATUSES as readonly string[]
  return [...statuses].sort((a, b) => {
    const ai = known.indexOf(a)
    const bi = known.indexOf(b)
    if (ai >= 0 && bi >= 0) return ai - bi
    if (ai >= 0) return -1
    if (bi >= 0) return 1
    return a.localeCompare(b)
  })
}

function entityKeyOf(entity: EntitySelection | null): string {
  return entity ? `${entity.kind} ${entity.value}` : ''
}

/** Adapt a /v1/search hit into the InboxItem shape the fragment table renders. */
function searchResultToItem(result: SearchResult): InboxItem {
  const f = result.fragment
  return {
    fragment_id: f.id,
    reason: result.snippet,
    staged_at: f.created_at,
    route_id: '',
    title: f.title,
    source: f.source,
    source_type: f.source_type,
    status: f.status,
    created_at: f.created_at,
  }
}

export default function OperationsPage() {
  const api = useApi()
  const scrollRef = useRef<HTMLDivElement>(null)

  const [filters, setFilters] = useState<InboxFilters>(
    () => readInboxFilters() ?? EMPTY_INBOX_FILTERS,
  )
  const [entityGroups, setEntityGroups] = useState<InboxEntityGroup[]>([])
  const [openFragmentId, setOpenFragmentId] = useState<string | null>(null)
  const [applyRouteOpen, setApplyRouteOpen] = useState(false)
  const [intakeOpen, setIntakeOpen] = useState(false)
  const [queue, setQueue] = useState<QueueStats | null>(null)

  // Search-mode controls — only meaningful while a search query is active.
  const [searchSettings, setSearchSettings] = useState<{ mode: SearchMode; limit: number }>({
    mode: 'auto',
    limit: 20,
  })
  // Concrete strategy the server reported for the last search.
  const [searchModeUsed, setSearchModeUsed] = useState<'semantic' | 'keyword' | undefined>(
    undefined,
  )

  // A non-empty search query flips the page from the staged inbox to a
  // server-side /v1/search across every fragment. Entity + route facets are
  // inbox-only, so they're suppressed while searching.
  const searchQuery = filters.search.trim()
  const searchMode = searchQuery.length > 0
  const entity = searchMode ? null : filters.entity

  // Delivery-queue counts for the overview strip.
  useEffect(() => {
    let cancelled = false
    void api
      .fetchQueueStatus()
      .then((stats) => {
        if (!cancelled) setQueue(stats)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [api])

  // Persist filter state whenever it changes.
  useEffect(() => {
    saveInboxFilters(filters)
  }, [filters])

  // Entity facet groups — loaded once; failure here shouldn't block the table.
  useEffect(() => {
    let cancelled = false
    void api
      .fetchInboxEntities()
      .then((groups) => {
        if (!cancelled) setEntityGroups(groups)
      })
      .catch(() => {})
    return () => {
      cancelled = true
    }
  }, [api])

  // When exactly one status chip is active during a search, pass it to the
  // server as the `status` param; multi-status stays a client-side refinement.
  const searchStatus =
    searchMode && filters.statuses.length === 1 ? filters.statuses[0] : undefined

  // Poll the active data source: search hits, an entity-scoped slice, or the
  // raw inbox — each a single request, so a transient failure can't half-load.
  const { data, error, isLoading, refetch } = usePoll<InboxItem[]>(async (signal) => {
    if (searchMode) {
      const response = await api.searchFragmentsDetailed(
        {
          q: searchQuery,
          mode: searchSettings.mode,
          limit: searchSettings.limit,
          status: searchStatus,
        },
        { signal },
      )
      setSearchModeUsed(response.mode_used)
      return response.results.map(searchResultToItem)
    }
    setSearchModeUsed(undefined)
    if (entity) {
      return api.fetchInboxEntityItems({ kind: entity.kind, value: entity.value })
    }
    return api.fetchInbox({}, { signal })
  })

  // Refetch immediately when the server-side query changes. The search
  // parameters (mode/limit/status) only contribute while a search is active —
  // otherwise toggling them with no query would needlessly refetch the inbox.
  const reloadKey = searchMode
    ? `search\0${searchQuery}\0${searchSettings.mode}\0${searchSettings.limit}\0${searchStatus ?? ''}`
    : `inbox\0${entityKeyOf(entity)}`
  const didMountRef = useRef(false)
  useEffect(() => {
    if (!didMountRef.current) {
      didMountRef.current = true
      return
    }
    void refetch()
  }, [reloadKey, refetch])

  const items = useMemo(() => data ?? [], [data])

  const availableStatuses = useMemo(
    () => orderStatuses([...new Set(items.map((i) => i.status).filter(Boolean))]),
    [items],
  )

  // Client-side status / route refinement over the fetched window. Route only
  // applies in inbox mode (search hits carry no route assignment).
  const filtered = useMemo(() => {
    return items.filter((it) => {
      if (filters.statuses.length > 0 && !filters.statuses.includes(it.status)) return false
      if (!searchMode) {
        if (filters.route === 'routed' && !it.route_id) return false
        if (filters.route === 'unrouted' && it.route_id) return false
      }
      return true
    })
  }, [items, filters.statuses, filters.route, searchMode])

  const summaryCards = useMemo(() => {
    const sources = new Set(items.map((i) => i.source).filter(Boolean)).size
    if (searchMode) {
      return [
        { label: 'Results', value: items.length, accentColor: SUMMARY_ACCENTS.total },
        { label: 'Sources', value: sources, accentColor: SUMMARY_ACCENTS.sources },
      ]
    }
    const routed = items.filter((i) => i.route_id).length
    return [
      { label: 'Staged', value: items.length, accentColor: SUMMARY_ACCENTS.total },
      { label: 'Unrouted', value: items.length - routed, accentColor: SUMMARY_ACCENTS.unrouted },
      { label: 'Routed', value: routed, accentColor: SUMMARY_ACCENTS.routed },
      { label: 'Sources', value: sources, accentColor: SUMMARY_ACCENTS.sources },
      {
        label: 'Queue pending',
        value: queue?.pending ?? '—',
        accentColor: 'var(--color-status-inbox)',
      },
      {
        label: 'Queue failed',
        value: queue?.failed ?? '—',
        accentColor: 'var(--color-status-blocked)',
      },
    ]
  }, [items, queue, searchMode])

  const activeFilterCount =
    (filters.statuses.length > 0 ? 1 : 0) +
    (!searchMode && filters.route !== 'both' ? 1 : 0) +
    (!searchMode && filters.entity !== null ? 1 : 0)

  const searchMatchCount = searchMode ? filtered.length : undefined

  const errorMessage =
    error instanceof ApiError
      ? error.message
      : error instanceof Error
        ? error.message
        : error
          ? 'Request failed'
          : null

  function patchFilters(patch: Partial<InboxFilters>) {
    setFilters((prev) => ({ ...prev, ...patch }))
  }

  function handleStatusToggle(status: string) {
    setFilters((prev) => ({
      ...prev,
      statuses: prev.statuses.includes(status)
        ? prev.statuses.filter((s) => s !== status)
        : [...prev.statuses, status],
    }))
  }

  function handleClear() {
    setFilters(EMPTY_INBOX_FILTERS)
  }

  const showSkeleton = isLoading && data === null
  const showError = !showSkeleton && errorMessage !== null && data === null

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      {/* Pinned: header + summary + filter bar sit outside the scroll area. */}
      <div className="shrink-0">
        <PageHeader title="Operations">
          {!searchMode && filters.entity && (
            <Button variant="outline" size="sm" onClick={() => setApplyRouteOpen(true)}>
              <Waypoints className="h-3.5 w-3.5" />
              Apply route
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => setIntakeOpen(true)}>
            <Plus className="h-3.5 w-3.5" />
            Intake
          </Button>
          <Button variant="outline" size="sm" onClick={() => void refetch()} disabled={isLoading}>
            <RefreshCw className={`h-3.5 w-3.5 ${isLoading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </PageHeader>

        {!showSkeleton && !showError && <SummaryCards cards={summaryCards} />}

        <FilterBar
          availableStatuses={availableStatuses}
          activeStatuses={filters.statuses}
          onStatusToggle={handleStatusToggle}
          routeFilter={filters.route}
          onRouteFilterChange={(route: RouteFilter) => patchFilters({ route })}
          entityGroups={entityGroups}
          entitySelection={filters.entity}
          onEntityChange={(selection) => patchFilters({ entity: selection })}
          searchQuery={filters.search}
          onSearchChange={(search) => patchFilters({ search })}
          searchMatchCount={searchMatchCount}
          activeFilterCount={activeFilterCount}
          onClear={handleClear}
          searchActive={searchMode}
          searchMode={searchSettings.mode}
          onSearchModeChange={(mode) => setSearchSettings((s) => ({ ...s, mode }))}
          searchLimit={searchSettings.limit}
          onSearchLimitChange={(limit) => setSearchSettings((s) => ({ ...s, limit }))}
          searchModeUsed={searchModeUsed}
        />
      </div>

      <div ref={scrollRef} className="min-h-0 flex-1 overflow-auto">
        {showSkeleton ? (
          <TableSkeleton />
        ) : showError ? (
          <div className="p-4">
            <EmptyState
              variant="error"
              title="Can't reach Fragments Engine on :8091."
              description={errorMessage ?? 'Start it with `fragments-engine serve-api`.'}
              action={{ label: 'Retry', onClick: () => void refetch() }}
            />
          </div>
        ) : items.length === 0 ? (
          <div className="p-4">
            {searchMode ? (
              <EmptyState
                variant="no-results"
                title="No fragments matched."
                description="Try a different search term."
              />
            ) : (
              <EmptyState
                variant="empty-inbox"
                title="Inbox empty."
                description="Run `fragments-engine ingest run` to pull in your first fragments."
                command="fragments-engine ingest run"
              />
            )}
          </div>
        ) : filtered.length === 0 ? (
          <div className="p-4">
            <EmptyState
              variant="no-results"
              title="No matches."
              description="Try clearing filters or broadening your search."
              action={{ label: 'Clear filters', onClick: handleClear }}
            />
          </div>
        ) : (
          <FragmentTable
            items={filtered}
            scrollRootRef={scrollRef}
            onOpenFragment={setOpenFragmentId}
          />
        )}
      </div>

      <FragmentDetailDialog fragmentId={openFragmentId} onClose={() => setOpenFragmentId(null)} />

      <ApplyRouteDialog
        open={applyRouteOpen}
        onClose={() => setApplyRouteOpen(false)}
        entityOptions={filters.entity ? [filters.entity] : []}
        onApplied={() => void refetch()}
      />

      <IntakeDialog
        open={intakeOpen}
        onClose={() => setIntakeOpen(false)}
        onCreated={() => void refetch()}
      />
    </div>
  )
}
