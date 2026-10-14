import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import {
  Button,
  EmptyState,
  FilterBar as KitFilterBar,
  ListPageLayout,
  PageHeader,
  Skeleton,
  SummaryCards,
} from '@hollis-labs/sysop-ui'
import ChatsTable from '@/components/chats/ChatsTable'
import { ChatDetailDialog } from '@/components/chats/ChatDetailDialog'
import { useApi } from '@/hooks/useApi'
import { SUMMARY_ACCENTS } from '@/lib/constants'
import {
  EMPTY_CHAT_FILTERS,
  activeChatFilterCount,
  type ChatEnrichedFilter,
  type ChatErrorFilter,
  type ChatFacets,
  type ChatFilters,
  type ChatKindFilter,
  type ChatProjectCount,
  type ChatSession,
  type ChatSortKey,
} from '@/lib/chats'
import { loadChatFilters, saveChatFilters } from '@/lib/chats-filters-storage'

const PAGE = 50
const SELECT_CLASS =
  'rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-text-soft outline-none transition-colors hover:border-border-strong focus:border-border-strong'

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex items-center gap-1 border-l border-border pl-3 first:border-l-0 first:pl-0">
      <span className="text-[10px] uppercase tracking-wider text-text-subtle">{label}:</span>
      {children}
    </label>
  )
}

export default function ChatsPage() {
  const api = useApi()
  const [filters, setFilters] = useState<ChatFilters>(loadChatFilters)
  const [query, setQuery] = useState(filters.search)
  const [items, setItems] = useState<ChatSession[]>([])
  const [cursor, setCursor] = useState<string | undefined>()
  const [facets, setFacets] = useState<ChatFacets | null>(null)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [openId, setOpenId] = useState<string | null>(null)
  const [hint, setHint] = useState<string | undefined>()
  const [projectOptions, setProjectOptions] = useState<ChatProjectCount[]>([])
  const requestId = useRef(0)

  const update = useCallback((patch: Partial<ChatFilters>) => {
    setFilters((current) => ({ ...current, ...patch }))
  }, [])

  useEffect(() => saveChatFilters(filters), [filters])

  // Debounce the search box so each keystroke is not a query.
  useEffect(() => {
    const timer = setTimeout(() => update({ search: query.trim() }), 300)
    return () => clearTimeout(timer)
  }, [query, update])

  useEffect(() => {
    api.fetchTranscriptFacets().then(setFacets).catch(() => setFacets(null))
  }, [api])

  // Project suggestions are fetched as the owner types, so every project is
  // reachable without sending the whole list.
  useEffect(() => {
    const controller = new AbortController()
    const timer = setTimeout(() => {
      api
        .fetchTranscriptProjects(filters.project, { signal: controller.signal })
        .then(setProjectOptions)
        .catch(() => {})
    }, 200)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [api, filters.project])

  const params = useMemo(
    () => ({
      q: filters.search || undefined,
      tool: filters.tool,
      model: filters.model,
      project: filters.project,
      kind: filters.kind === 'all' ? '' : filters.kind,
      has_error: filters.errors === 'errors' ? 'true' : filters.errors === 'clean' ? 'false' : '',
      has_enrichment: filters.enriched === 'yes' ? 'true' : filters.enriched === 'no' ? 'false' : '',
      tag: filters.tag,
      since: filters.since,
      until: filters.until,
      sort: filters.sort,
      order: filters.desc ? ('desc' as const) : ('asc' as const),
      limit: PAGE,
    }),
    [filters],
  )

  const load = useCallback(async () => {
    const id = ++requestId.current
    setLoading(true)
    setError(null)
    try {
      const page = await api.fetchTranscriptSessions(params)
      if (id !== requestId.current) return
      setItems(page.items)
      setCursor(page.next_cursor)
      setHint(page.hint)
    } catch (err) {
      if (id === requestId.current) setError(err instanceof Error ? err.message : 'Failed to load chats')
    } finally {
      if (id === requestId.current) setLoading(false)
    }
  }, [api, params])

  useEffect(() => {
    void load()
  }, [load])

  async function loadMore() {
    if (!cursor) return
    setLoadingMore(true)
    try {
      const page = await api.fetchTranscriptSessions({ ...params, cursor })
      setItems((current) => [...current, ...page.items])
      setCursor(page.next_cursor)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load more chats')
    } finally {
      setLoadingMore(false)
    }
  }

  function handleSort(key: ChatSortKey) {
    setFilters((current) =>
      current.sort === key ? { ...current, desc: !current.desc } : { ...current, sort: key, desc: true },
    )
  }

  const activeCount = activeChatFilterCount(filters)
  const summaryCards = useMemo(
    () => [
      { label: 'Chats', value: facets?.total ?? 0, accentColor: SUMMARY_ACCENTS.total },
      { label: 'Showing', value: items.length, accentColor: SUMMARY_ACCENTS.sources },
      {
        label: 'With errors',
        value: items.filter((i) => i.errors > 0).length,
        accentColor: 'var(--color-status-inbox)',
      },
    ],
    [facets, items],
  )

  return (
    <ListPageLayout
      header={
        <PageHeader title="Chats">
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </PageHeader>
      }
      summary={!loading && !error ? <SummaryCards cards={summaryCards} /> : undefined}
      filters={
        <KitFilterBar
          searchQuery={query}
          onSearchChange={setQuery}
          searchPlaceholder="Search chat text…"
          searchAriaLabel="Search chats"
          activeFilterCount={activeCount}
          summary={filters.search ? `${items.length}${cursor ? '+' : ''} matches` : activeCount ? `${activeCount} filter${activeCount === 1 ? '' : 's'}` : ''}
          onClear={() => {
            setQuery('')
            setFilters({ ...EMPTY_CHAT_FILTERS, sort: filters.sort, desc: filters.desc })
          }}
        >
          <Field label="Tool">
            <select aria-label="Tool" className={SELECT_CLASS} value={filters.tool} onChange={(e) => update({ tool: e.target.value })}>
              <option value="">All</option>
              {(facets?.tools ?? []).map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          </Field>
          <Field label="Model">
            <select aria-label="Model" className={SELECT_CLASS} value={filters.model} onChange={(e) => update({ model: e.target.value })}>
              <option value="">All</option>
              {(facets?.models ?? []).map((m) => <option key={m} value={m}>{m}</option>)}
            </select>
          </Field>
          <Field label="Project">
            <input
              aria-label="Project"
              list="chat-projects"
              className={`${SELECT_CLASS} w-36 normal-case`}
              placeholder="path contains…"
              value={filters.project}
              onChange={(e) => update({ project: e.target.value })}
            />
            <datalist id="chat-projects">
              {projectOptions.map((p) => (
                <option key={p.project} value={p.project}>{p.sessions} chats</option>
              ))}
            </datalist>
          </Field>
          <Field label="Kind">
            <select aria-label="Kind" className={SELECT_CLASS} value={filters.kind} onChange={(e) => update({ kind: e.target.value as ChatKindFilter })}>
              <option value="all">All</option>
              <option value="main">Main</option>
              <option value="subagent">Subagent</option>
            </select>
          </Field>
          <Field label="Errors">
            <select aria-label="Errors" className={SELECT_CLASS} value={filters.errors} onChange={(e) => update({ errors: e.target.value as ChatErrorFilter })}>
              <option value="all">All</option>
              <option value="errors">Has errors</option>
              <option value="clean">No errors</option>
            </select>
          </Field>
          <Field label="Enriched">
            <select aria-label="Enriched" className={SELECT_CLASS} value={filters.enriched} onChange={(e) => update({ enriched: e.target.value as ChatEnrichedFilter })}>
              <option value="all">All</option>
              <option value="yes">Enriched</option>
              <option value="no">Not yet</option>
            </select>
          </Field>
          {(facets?.tags ?? []).length > 0 && (
            <Field label="Tag">
              <select aria-label="Tag" className={SELECT_CLASS} value={filters.tag} onChange={(e) => update({ tag: e.target.value })}>
                <option value="">All</option>
                {(facets?.tags ?? []).map((t) => <option key={t} value={t}>{t}</option>)}
              </select>
            </Field>
          )}
          <Field label="From">
            <input aria-label="From date" type="date" className={SELECT_CLASS} value={filters.since} onChange={(e) => update({ since: e.target.value })} />
          </Field>
          <Field label="To">
            <input aria-label="To date" type="date" className={SELECT_CLASS} value={filters.until} onChange={(e) => update({ until: e.target.value })} />
          </Field>
        </KitFilterBar>
      }
    >
      <>
        {loading ? (
          <div className="flex flex-col gap-2 p-4">
            {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-12 w-full rounded-xl" />)}
          </div>
        ) : error ? (
          <p className="p-4 text-sm text-danger-soft">{error}</p>
        ) : hint === 'too_broad' ? (
          <div className="p-4">
            <EmptyState
              variant="no-results"
              title="That search is too broad"
              description="It matches too much to answer quickly. Add another word, or narrow by tool, project or date."
            />
          </div>
        ) : items.length === 0 ? (
          <div className="p-4">
            <EmptyState variant="no-results" title="No chats in view" description="Adjust filters, or import agent sessions." />
          </div>
        ) : (
          <>
            <ChatsTable items={items} sort={filters.sort} desc={filters.desc} onSort={handleSort} onOpen={setOpenId} />
            {cursor && (
              <div className="p-3 text-center">
                <Button variant="outline" size="sm" onClick={() => void loadMore()} disabled={loadingMore}>
                  {loadingMore ? 'Loading…' : 'Load more'}
                </Button>
              </div>
            )}
          </>
        )}
        <ChatDetailDialog sessionId={openId} onClose={() => setOpenId(null)} onOpenSession={setOpenId} />
      </>
    </ListPageLayout>
  )
}
