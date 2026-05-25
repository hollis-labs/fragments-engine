import { useEffect, useMemo, useState } from 'react'
import {
  Copy,
  FileText,
  Folder,
  FolderOpen,
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

interface BrowserFolder {
  kind: 'folder'
  path: string
  name: string
  count: number
  previewCount: number
  modifiedAt: string
}

interface BrowserFile {
  kind: 'file'
  item: FragmentBrowseItem
}

type BrowserEntry = BrowserFolder | BrowserFile

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

function virtualPath(item: FragmentBrowseItem): string {
  if (!item.materialized) {
    return ['inbox', item.source || 'manual', item.source_type || 'fragment', item.title || item.fragment_id].join('/')
  }
  switch (kindOf(item)) {
    case 'note':
      return `docs/notes/${item.title || item.fragment_id}`
    case 'quote':
      return `docs/quotes/${item.title || item.fragment_id}`
    case 'report':
      return `docs/reports/${item.title || item.fragment_id}`
    case 'pin':
      return `media/pins/${item.title || item.source_id || item.fragment_id}`
    case 'reference':
      return `docs/references/${item.title || item.source_id || item.fragment_id}`
    default:
      return `docs/references/${item.title || item.source_id || item.fragment_id}`
  }
}

function pathSegments(path: string): string[] {
  return path.split('/').map((segment) => segment.trim()).filter(Boolean)
}

function childEntries(items: FragmentBrowseItem[], currentPath: string): BrowserEntry[] {
  const current = pathSegments(currentPath)
  const folders = new Map<string, BrowserFolder>()
  const files: BrowserFile[] = []

  for (const item of items) {
    const segments = pathSegments(virtualPath(item))
    const matchesCurrent = current.every((segment, index) => segments[index] === segment)
    if (!matchesCurrent) continue

    if (segments.length > current.length + 1) {
      const name = segments[current.length]
      const path = [...current, name].join('/')
      const existing = folders.get(path)
      if (existing) {
        existing.count += 1
        existing.previewCount += previewable(item) ? 1 : 0
        if (item.modified_at > existing.modifiedAt) existing.modifiedAt = item.modified_at
      } else {
        folders.set(path, {
          kind: 'folder',
          path,
          name,
          count: 1,
          previewCount: previewable(item) ? 1 : 0,
          modifiedAt: item.modified_at,
        })
      }
      continue
    }

    if (segments.length === current.length + 1) {
      files.push({ kind: 'file', item })
    }
  }

  return [
    ...[...folders.values()].sort((a, b) => a.name.localeCompare(b.name)),
    ...files,
  ]
}

function FolderSummary({ folder }: { folder: BrowserFolder }) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-[11px] text-text-subtle">
      <span>{folder.count} item{folder.count === 1 ? '' : 's'}</span>
      <span>{folder.previewCount} preview{folder.previewCount === 1 ? '' : 's'}</span>
      {folder.modifiedAt && (
        <span>
          modified {formatShortDate(folder.modifiedAt)} · {formatRelativeTime(folder.modifiedAt)}
        </span>
      )}
    </div>
  )
}

function LibraryBreadcrumb({
  currentPath,
  onNavigate,
}: {
  currentPath: string
  onNavigate: (path: string) => void
}) {
  const segments = pathSegments(currentPath)
  return (
    <div className="flex flex-wrap items-center gap-1 border-b border-border-soft bg-panel-2/20 px-4 py-2 text-[11px] uppercase tracking-[.14em] text-text-subtle">
      <button
        type="button"
        onClick={() => onNavigate('')}
        className={`rounded px-2 py-1 transition hover:bg-panel-hover hover:text-text ${
          segments.length === 0 ? 'bg-panel-hover text-text' : ''
        }`}
      >
        ffs
      </button>
      {segments.map((segment, index) => {
        const path = segments.slice(0, index + 1).join('/')
        return (
          <span key={path} className="inline-flex items-center gap-1">
            <span className="text-text-subtle/60">/</span>
            <button
              type="button"
              onClick={() => onNavigate(path)}
              className={`rounded px-2 py-1 transition hover:bg-panel-hover hover:text-text ${
                index === segments.length - 1 ? 'bg-panel-hover text-text' : ''
              }`}
            >
              {segment}
            </button>
          </span>
        )
      })}
    </div>
  )
}

function LibraryListView({
  entries,
  previewURL,
  onOpenFile,
  onOpenFolder,
}: {
  entries: BrowserEntry[]
  previewURL: (item: FragmentBrowseItem) => string | undefined
  onOpenFile: (fragmentId: string) => void
  onOpenFolder: (path: string) => void
}) {
  return (
    <div className="flex flex-col divide-y divide-border-soft">
      {entries.map((entry) => {
        if (entry.kind === 'folder') {
          return (
            <button
              key={`folder:${entry.path}`}
              type="button"
              onClick={() => onOpenFolder(entry.path)}
              className="flex items-center gap-3 bg-bg px-4 py-3 text-left transition hover:bg-panel-hover/50"
            >
              <div className="flex h-11 w-11 shrink-0 items-center justify-center rounded-md border border-border bg-panel-2/50">
                <Folder className="h-5 w-5 text-text-soft" />
              </div>
              <div className="min-w-0 flex-1">
                <p className="truncate text-[14px] text-text">{entry.name}</p>
                <FolderSummary folder={entry} />
              </div>
              <span className="font-mono text-[10px] uppercase tracking-[.14em] text-text-subtle">
                {entry.path}
              </span>
            </button>
          )
        }
        const item = entry.item
        const Icon = typeIcon(item)
        const image = previewURL(item)
        return (
          <button
            key={`file:${item.fragment_id}`}
            type="button"
            onClick={() => onOpenFile(item.fragment_id)}
            className="flex items-center gap-3 bg-bg px-4 py-3 text-left transition hover:bg-panel-hover/50"
          >
            <div className="flex h-11 w-11 shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-panel-2/50">
              {image ? (
                <img
                  src={image}
                  alt={item.title || item.fragment_id}
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              ) : (
                <Icon className="h-5 w-5 text-text-subtle" />
              )}
            </div>
            <div className="min-w-0 flex-1">
              <p className="truncate text-[14px] text-text">{item.title || '(untitled fragment)'}</p>
              <div className="flex flex-wrap items-center gap-2 text-[11px] text-text-subtle">
                <span>{item.source_type || item.source}</span>
                <span>{formatShortDate(item.modified_at)} · {formatRelativeTime(item.modified_at)}</span>
                {item.materialized && <span className="text-status-routed">saved</span>}
              </div>
            </div>
            <div className="hidden max-w-[26rem] flex-wrap justify-end gap-1 md:flex">
              {(item.tags ?? []).slice(0, 5).map((tag) => (
                <span
                  key={tag}
                  className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] text-text-soft"
                >
                  {tag}
                </span>
              ))}
            </div>
            <button
              type="button"
              onClick={(event) => {
                event.stopPropagation()
                copyText(item.canonical_path || virtualPath(item))
              }}
              className="inline-flex items-center gap-1 rounded border border-border bg-panel-2/50 px-2 py-1 font-mono text-[10px] text-text-subtle transition hover:text-text"
              title="Copy path"
            >
              <Copy className="h-3 w-3" />
            </button>
          </button>
        )
      })}
    </div>
  )
}

function LibraryCardView({
  entries,
  previewURL,
  onOpenFile,
  onOpenFolder,
}: {
  entries: BrowserEntry[]
  previewURL: (item: FragmentBrowseItem) => string | undefined
  onOpenFile: (fragmentId: string) => void
  onOpenFolder: (path: string) => void
}) {
  return (
    <div className="flex flex-col gap-3 p-4">
      {entries.map((entry) => {
        if (entry.kind === 'folder') {
          return (
            <button
              key={`folder:${entry.path}`}
              type="button"
              onClick={() => onOpenFolder(entry.path)}
              className="group flex overflow-hidden rounded-md border border-border bg-panel-2/40 text-left transition hover:border-border-strong hover:bg-panel-hover/50"
            >
              <div className="flex h-28 w-40 shrink-0 items-center justify-center border-r border-border bg-bg">
                <FolderOpen className="h-8 w-8 text-text-soft" />
              </div>
              <div className="flex min-w-0 flex-1 flex-col gap-2 p-4">
                <div className="text-[10px] uppercase tracking-[.16em] text-text-subtle">folder</div>
                <p className="truncate text-[15px] text-text">{entry.name}</p>
                <FolderSummary folder={entry} />
                <span className="mt-auto truncate font-mono text-[11px] text-text-subtle">{entry.path}</span>
              </div>
            </button>
          )
        }
        const item = entry.item
        const Icon = typeIcon(item)
        return (
          <button
            key={item.fragment_id}
            type="button"
            onClick={() => onOpenFile(item.fragment_id)}
            className="group flex overflow-hidden rounded-md border border-border bg-panel-2/40 text-left transition hover:border-border-strong hover:bg-panel-hover/50"
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
  const [currentPath, setCurrentPath] = useState('')
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

  useEffect(() => {
    setCurrentPath('')
  }, [kindFilter, materializedFilter, search, statuses, visualFilter])

  const entries = useMemo(
    () => childEntries(filtered, currentPath),
    [currentPath, filtered],
  )

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
      summary={!loading && !error ? <SummaryCards cards={summaryCards} /> : undefined}
      filters={
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
            setCurrentPath('')
          }}
        />
      }
    >
      <>
        {loading ? (
          <div className="flex flex-col gap-2 p-4">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-20 w-full rounded-xl" />
            ))}
          </div>
        ) : error ? (
          <p className="p-4 text-sm text-danger-soft">{error}</p>
        ) : filtered.length === 0 || entries.length === 0 ? (
          <div className="p-4">
            <EmptyState
              variant="no-results"
              title="No fragments in view"
              description="Adjust filters or ingest more material into Fragments Engine."
            />
          </div>
        ) : viewMode === 'list' ? (
          <>
            <LibraryBreadcrumb currentPath={currentPath} onNavigate={setCurrentPath} />
            <LibraryListView
              entries={entries}
              previewURL={previewURL}
              onOpenFile={setOpenFragmentId}
              onOpenFolder={setCurrentPath}
            />
          </>
        ) : (
          <>
            <LibraryBreadcrumb currentPath={currentPath} onNavigate={setCurrentPath} />
            <LibraryCardView
              entries={entries}
              previewURL={previewURL}
              onOpenFile={setOpenFragmentId}
              onOpenFolder={setCurrentPath}
            />
          </>
        )}
        <FragmentDetailDialog fragmentId={openFragmentId} onClose={() => setOpenFragmentId(null)} />
      </>
    </ListPageLayout>
  )
}
