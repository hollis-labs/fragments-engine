import { useEffect, useMemo, useState } from 'react'
import {
  Copy,
  FileText,
  Image as ImageIcon,
  Quote,
  RefreshCw,
  ScrollText,
  StickyNote,
} from 'lucide-react'
import {
  Button,
  EmptyState,
  ListPageLayout,
  PageHeader,
  Skeleton,
  SummaryCards,
  formatRelativeTime,
  formatShortDate,
} from '@hollis-labs/sysop-ui'
import FilterBar from '@/components/domain/filter-bar/filter-bar'
import { FragmentDetailDialog } from '@/components/domain/fragment-detail-dialog'
import { useApi } from '@/hooks/useApi'
import { SUMMARY_ACCENTS } from '@/lib/constants'
import type { FragmentBrowseItem } from '@/lib/types'

type ViewMode = 'list' | 'cards'
type KindFilter = 'all' | 'note' | 'quote' | 'report' | 'pin' | 'reference'
type SortMode = 'modified_desc' | 'created_desc' | 'title_asc'
type VisualFilter = 'all' | 'visual' | 'pins'
type MaterializedFilter = 'all' | 'pending' | 'materialized'

function matchesSearch(item: FragmentBrowseItem, query: string): boolean {
  const probe = [
    item.title,
    item.summary,
    item.fragment_id,
    item.canonical_path,
    item.source_type,
    item.source_id,
    ...(item.tags ?? []),
  ]
    .join('\n')
    .toLowerCase()
  return probe.includes(query.toLowerCase())
}

function kindOf(item: FragmentBrowseItem): KindFilter {
  switch (item.source_type) {
    case 'note':
    case 'quote':
    case 'report':
    case 'pin':
      return item.source_type
    case 'repo':
    case 'url':
    case 'article':
    case 'reference':
      return 'reference'
    default:
      return 'all'
  }
}

function previewable(item: FragmentBrowseItem): boolean {
  return Boolean(item.preview_attachment_id)
}

function typeIcon(item: FragmentBrowseItem) {
  switch (item.source_type) {
    case 'note':
      return StickyNote
    case 'quote':
      return Quote
    case 'report':
      return ScrollText
    case 'pin':
      return ImageIcon
    default:
      return FileText
  }
}

function sortItems(items: FragmentBrowseItem[], mode: SortMode): FragmentBrowseItem[] {
  const next = [...items]
  next.sort((a, b) => {
    if (mode === 'title_asc') return a.title.localeCompare(b.title)
    if (mode === 'created_desc') return b.created_at.localeCompare(a.created_at)
    return b.modified_at.localeCompare(a.modified_at)
  })
  return next
}

function copyText(value: string) {
  void navigator.clipboard?.writeText(value)
}

function LibraryList({
  items,
  previewURL,
  onOpen,
}: {
  items: FragmentBrowseItem[]
  previewURL: (item: FragmentBrowseItem) => string | undefined
  onOpen: (fragmentId: string) => void
}) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[880px]">
        <thead className="border-b border-border-strong text-[10px] uppercase tracking-[.18em] text-text-subtle">
          <tr>
            <th className="px-4 py-2 text-left font-medium">Name</th>
            <th className="px-3 py-2 text-left font-medium">Type</th>
            <th className="px-3 py-2 text-left font-medium">Tags</th>
            <th className="px-3 py-2 text-left font-medium">Modified</th>
            <th className="px-3 py-2 text-left font-medium">Path</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border-soft">
          {items.map((item) => {
            const Icon = typeIcon(item)
            return (
              <tr
                key={item.fragment_id}
                className="cursor-pointer bg-bg transition hover:bg-panel-hover/50"
                onClick={() => onOpen(item.fragment_id)}
              >
                <td className="px-4 py-2">
                  <div className="flex items-center gap-3">
                    <div className="flex h-10 w-10 items-center justify-center overflow-hidden rounded-md border border-border bg-panel-2/50">
                      {previewURL(item) ? (
                        <img
                          src={previewURL(item)}
                          alt={item.title || item.fragment_id}
                          className="h-full w-full object-cover"
                          loading="lazy"
                        />
                      ) : (
                        <Icon className="h-4 w-4 text-text-subtle" />
                      )}
                    </div>
                    <div className="min-w-0">
                      <p className="truncate text-[13px] text-text">{item.title || '(untitled fragment)'}</p>
                      <p className="truncate text-[11px] text-text-subtle">{item.summary || item.fragment_id}</p>
                    </div>
                  </div>
                </td>
                <td className="px-3 py-2 text-[11px] uppercase tracking-[.14em] text-text-soft">
                  {item.source_type || item.source}
                </td>
                <td className="px-3 py-2">
                  <div className="flex max-w-72 flex-wrap gap-1">
                    {(item.tags ?? []).slice(0, 4).map((tag) => (
                      <span
                        key={tag}
                        className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] text-text-soft"
                      >
                        {tag}
                      </span>
                    ))}
                  </div>
                </td>
                <td className="px-3 py-2 text-[11px] text-text-soft">
                  <div>{formatShortDate(item.modified_at)}</div>
                  <div className="text-text-subtle">{formatRelativeTime(item.modified_at)}</div>
                </td>
                <td className="px-3 py-2">
                  <button
                    type="button"
                    onClick={(event) => {
                      event.stopPropagation()
                      copyText(item.canonical_path)
                    }}
                    className="inline-flex items-center gap-1 rounded border border-border bg-panel-2/50 px-2 py-1 font-mono text-[10px] text-text-subtle transition hover:text-text"
                  >
                    <Copy className="h-3 w-3" />
                    <span className="max-w-64 truncate">{item.canonical_path || item.source_id}</span>
                  </button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function LibraryCards({
  items,
  previewURL,
  onOpen,
}: {
  items: FragmentBrowseItem[]
  previewURL: (item: FragmentBrowseItem) => string | undefined
  onOpen: (fragmentId: string) => void
}) {
  return (
    <div className="flex flex-col gap-3 p-4">
      {items.map((item) => {
        const Icon = typeIcon(item)
        return (
          <button
            key={item.fragment_id}
            type="button"
            onClick={() => onOpen(item.fragment_id)}
            className="group flex overflow-hidden rounded-xl border border-border bg-panel-2/40 text-left transition hover:border-border-strong hover:bg-panel-hover/50"
          >
            <div className="flex h-28 w-40 shrink-0 items-center justify-center overflow-hidden border-r border-border bg-bg">
              {previewURL(item) ? (
                <img
                  src={previewURL(item)}
                  alt={item.title || item.fragment_id}
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              ) : (
                <Icon className="h-6 w-6 text-text-subtle" />
              )}
            </div>
            <div className="flex min-w-0 flex-1 flex-col gap-2 p-4">
              <div className="flex flex-wrap items-center gap-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">
                <span>{item.source_type || item.source}</span>
                {item.materialized && (
                  <span className="rounded border border-status-routed/30 bg-status-routed/10 px-1.5 py-0.5 text-status-routed">
                    saved
                  </span>
                )}
              </div>
              <div className="min-w-0">
                <p className="truncate text-[15px] text-text">{item.title || '(untitled fragment)'}</p>
                <p className="mt-1 line-clamp-2 text-[12px] leading-5 text-text-subtle">
                  {item.summary || item.canonical_path || item.source_id}
                </p>
              </div>
              <div className="flex flex-wrap gap-1">
                {(item.tags ?? []).map((tag) => (
                  <span
                    key={tag}
                    className="rounded border border-border bg-bg px-1.5 py-0.5 text-[10px] text-text-soft"
                  >
                    {tag}
                  </span>
                ))}
              </div>
              <div className="mt-auto flex items-center justify-between text-[11px] text-text-soft">
                <span>{formatShortDate(item.modified_at)} · {formatRelativeTime(item.modified_at)}</span>
                <span className="font-mono text-text-subtle">{item.canonical_path || item.source_id}</span>
              </div>
            </div>
          </button>
        )
      })}
    </div>
  )
}

export default function LibraryPage() {
  const api = useApi()
  const [items, setItems] = useState<FragmentBrowseItem[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [openFragmentId, setOpenFragmentId] = useState<string | null>(null)
  const [viewMode, setViewMode] = useState<ViewMode>('list')
  const [sortMode, setSortMode] = useState<SortMode>('modified_desc')
  const [kindFilter, setKindFilter] = useState<KindFilter>('all')
  const [search, setSearch] = useState('')
  const [statuses, setStatuses] = useState<string[]>([])
  const [visualFilter, setVisualFilter] = useState<VisualFilter>('all')
  const [materializedFilter, setMaterializedFilter] = useState<MaterializedFilter>('all')

  async function load() {
    setLoading(true)
    setError(null)
    try {
      const result = await api.fetchBrowseFragments({ limit: 500 })
      setItems(result.items)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load library')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void load()
  }, [])

  const availableStatuses = useMemo(
    () => [...new Set(items.map((item) => item.status).filter(Boolean))].sort(),
    [items],
  )

  const filtered = useMemo(() => {
    const searched = items.filter((item) => {
      if (search.trim() && !matchesSearch(item, search.trim())) return false
      if (statuses.length > 0 && !statuses.includes(item.status)) return false
      if (visualFilter === 'visual' && !previewable(item)) return false
      if (visualFilter === 'pins' && item.source_type !== 'pin') return false
      if (materializedFilter === 'materialized' && !item.materialized) return false
      if (materializedFilter === 'pending' && item.materialized) return false
      if (kindFilter !== 'all' && kindOf(item) !== kindFilter) return false
      return true
    })
    return sortItems(searched, sortMode)
  }, [items, kindFilter, materializedFilter, search, sortMode, statuses, visualFilter])

  const previewURL = (item: FragmentBrowseItem) =>
    item.preview_attachment_id
      ? api.fragmentAttachmentURL({
          fragmentId: item.fragment_id,
          attachmentId: item.preview_attachment_id,
          variant: 'preview',
        })
      : undefined

  const summaryCards = useMemo(
    () => [
      { label: 'Files', value: items.length, accentColor: SUMMARY_ACCENTS.total },
      { label: 'Visible', value: filtered.length, accentColor: SUMMARY_ACCENTS.sources },
      {
        label: 'Saved',
        value: items.filter((item) => item.materialized).length,
        accentColor: 'var(--color-status-routed)',
      },
      {
        label: 'Visual',
        value: items.filter((item) => previewable(item)).length,
        accentColor: 'var(--color-status-indexed)',
      },
    ],
    [filtered.length, items],
  )

  const activeFilterCount =
    (statuses.length > 0 ? 1 : 0) +
    (visualFilter !== 'all' ? 1 : 0) +
    (materializedFilter !== 'all' ? 1 : 0) +
    (kindFilter !== 'all' ? 1 : 0)

  return (
    <ListPageLayout
      header={
        <PageHeader title="Library">
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={kindFilter}
              onChange={(e) => setKindFilter(e.target.value as KindFilter)}
              className="rounded border border-border bg-panel-2/50 px-2 py-1 text-[11px] uppercase tracking-[.12em] text-text-soft"
            >
              <option value="all">All kinds</option>
              <option value="note">Notes</option>
              <option value="quote">Quotes</option>
              <option value="report">Reports</option>
              <option value="pin">Pins</option>
              <option value="reference">References</option>
            </select>
            <select
              value={sortMode}
              onChange={(e) => setSortMode(e.target.value as SortMode)}
              className="rounded border border-border bg-panel-2/50 px-2 py-1 text-[11px] uppercase tracking-[.12em] text-text-soft"
            >
              <option value="modified_desc">Recently modified</option>
              <option value="created_desc">Recently added</option>
              <option value="title_asc">Title A-Z</option>
            </select>
            <div className="inline-flex overflow-hidden rounded border border-border">
              <button
                type="button"
                onClick={() => setViewMode('list')}
                className={`px-3 py-1.5 text-[11px] uppercase tracking-[.12em] transition ${
                  viewMode === 'list'
                    ? 'bg-panel-hover text-text'
                    : 'bg-panel-2/50 text-text-subtle hover:text-text'
                }`}
              >
                List
              </button>
              <button
                type="button"
                onClick={() => setViewMode('cards')}
                className={`px-3 py-1.5 text-[11px] uppercase tracking-[.12em] transition ${
                  viewMode === 'cards'
                    ? 'bg-panel-hover text-text'
                    : 'bg-panel-2/50 text-text-subtle hover:text-text'
                }`}
              >
                Cards
              </button>
            </div>
            <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
              <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
              Refresh
            </Button>
          </div>
        </PageHeader>
      }
    >
      <>
        <div className="border-b border-border-strong px-4 py-4">
          <SummaryCards cards={summaryCards} />
        </div>
        <div className="border-b border-border-strong px-4 py-4">
          <FilterBar
            availableStatuses={availableStatuses}
            activeStatuses={statuses}
            onStatusToggle={(status) =>
              setStatuses((current) =>
                current.includes(status) ? current.filter((item) => item !== status) : [...current, status],
              )
            }
            routeFilter="both"
            onRouteFilterChange={() => {}}
            visualFilter={visualFilter}
            onVisualFilterChange={setVisualFilter}
            materializedFilter={materializedFilter}
            onMaterializedFilterChange={setMaterializedFilter}
            entityGroups={[]}
            entitySelection={null}
            onEntityChange={() => {}}
            searchQuery={search}
            onSearchChange={setSearch}
            searchMatchCount={filtered.length}
            activeFilterCount={activeFilterCount}
            onClear={() => {
              setSearch('')
              setStatuses([])
              setVisualFilter('all')
              setMaterializedFilter('all')
              setKindFilter('all')
            }}
          />
        </div>
        {loading ? (
          <div className="flex flex-col gap-2 p-4">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-20 w-full rounded-xl" />
            ))}
          </div>
        ) : error ? (
          <p className="p-4 text-sm text-danger-soft">{error}</p>
        ) : filtered.length === 0 ? (
          <div className="p-4">
            <EmptyState
              variant="no-results"
              title="No fragments in view"
              description="Adjust filters or ingest more material into Fragments Engine."
            />
          </div>
        ) : viewMode === 'list' ? (
          <LibraryList items={filtered} previewURL={previewURL} onOpen={setOpenFragmentId} />
        ) : (
          <LibraryCards items={filtered} previewURL={previewURL} onOpen={setOpenFragmentId} />
        )}
        <FragmentDetailDialog fragmentId={openFragmentId} onClose={() => setOpenFragmentId(null)} />
      </>
    </ListPageLayout>
  )
}
