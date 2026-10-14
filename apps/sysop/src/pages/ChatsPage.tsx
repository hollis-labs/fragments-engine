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
  CHAT_SEARCH_MODES,
  isTypingTarget,
  moveSelection,
  EMPTY_CHAT_FILTERS,
  activeChatFilterCount,
  noticeText,
  resolveSearchMode,
  type ChatSearchMode,
  type ChatStatus,
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
import { ChatPresets } from '@/components/chats/ChatPresets'
import { loadPresets, removePreset, savePresets, upsertPreset, type ChatPreset } from '@/lib/chats-presets'

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
  const [notice, setNotice] = useState<string | undefined>()
  const [status, setStatus] = useState<ChatStatus | null>(null)
  const [statusLoaded, setStatusLoaded] = useState(false)
  const [projectOptions, setProjectOptions] = useState<ChatProjectCount[]>([])
  const [presets, setPresets] = useState<ChatPreset[]>(loadPresets)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const requestId = useRef(0)
  const openIdRef = useRef<string | null>(null)

  const update = useCallback((patch: Partial<ChatFilters>) => {
    setFilters((current) => ({ ...current, ...patch }))
  }, [])

  useEffect(() => saveChatFilters(filters), [filters])
  useEffect(() => savePresets(presets), [presets])
  useEffect(() => {
    openIdRef.current = openId
  }, [openId])

  // Applying a preset also restores its search text into the search box.
  const applyFilters = useCallback((next: ChatFilters) => {
    setQuery(next.search)
    setFilters(next)
  }, [])

  // Keep the keyboard selection on a row that is still listed.
  useEffect(() => {
    setSelectedId((current) => (current && items.some((i) => i.id === current) ? current : null))
  }, [items])

  // Keyboard: j/k move, Enter opens, / focuses search, Esc leaves the search
  // box or clears the selection. Nothing fires while typing or in the dialog.
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.metaKey || e.ctrlKey || e.altKey || openIdRef.current) return
      if (isTypingTarget(e.target)) {
        if (e.key === 'Escape' && e.target instanceof HTMLElement) e.target.blur()
        return
      }
      const ids = items.map((i) => i.id)
      if (e.key === 'j') setSelectedId((c) => moveSelection(ids, c, 1))
      else if (e.key === 'k') setSelectedId((c) => moveSelection(ids, c, -1))
      else if (e.key === 'Enter' && selectedId && e.target === document.body) setOpenId(selectedId)
      else if (e.key === '/') {
        e.preventDefault()
        document.querySelector<HTMLInputElement>('input[aria-label="Search chats"]')?.focus()
      } else if (e.key === 'Escape') setSelectedId(null)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [items, selectedId])

  // Debounce the search box so each keystroke is not a query.
  useEffect(() => {
    const timer = setTimeout(() => update({ search: query.trim() }), 300)
    return () => clearTimeout(timer)
  }, [query, update])

  useEffect(() => {
    api.fetchTranscriptFacets().then(setFacets).catch(() => setFacets(null))
  }, [api])

  // Auto mode depends on whether semantic search works, so the first query
  // waits for the status answer (a failed status just means "not semantic").
  useEffect(() => {
    api
      .fetchTranscriptStatus()
      .then(setStatus)
      .catch(() => setStatus(null))
      .finally(() => setStatusLoaded(true))
  }, [api])

  const requestMode = resolveSearchMode(filters.mode, status?.semantic === true)

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
      mode: filters.search ? requestMode : undefined,
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
    [filters, requestMode],
  )

  const load = useCallback(async () => {
    if (!statusLoaded) return
    const id = ++requestId.current
    setLoading(true)
    setError(null)
    try {
      const page = await api.fetchTranscriptSessions(params)
      if (id !== requestId.current) return
      setItems(page.items)
      setCursor(page.next_cursor)
      setHint(page.hint)
      setNotice(page.notice)
    } catch (err) {
      if (id === requestId.current) setError(err instanceof Error ? err.message : 'Failed to load chats')
    } finally {
      if (id === requestId.current) setLoading(false)
    }
  }, [api, params, statusLoaded])

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
          searchPlaceholder="Search chats…"
          searchAriaLabel="Search chats"
          activeFilterCount={activeCount}
          summary={filters.search ? `${items.length}${cursor ? '+' : ''} matches` : activeCount ? `${activeCount} filter${activeCount === 1 ? '' : 's'}` : ''}
          onClear={() => {
            setQuery('')
            setFilters({ ...EMPTY_CHAT_FILTERS, mode: filters.mode, sort: filters.sort, desc: filters.desc })
          }}
        >
          <Field label="Presets">
            <ChatPresets
              presets={presets}
              selectClass={SELECT_CLASS}
              onApply={applyFilters}
              onSave={(name) => setPresets((current) => upsertPreset(current, name, { ...filters, search: query.trim() }))}
              onDelete={(name) => setPresets((current) => removePreset(current, name))}
            />
          </Field>
          <Field label="Search">
            <select
              aria-label="Search mode"
              className={SELECT_CLASS}
              value={filters.mode}
              title={CHAT_SEARCH_MODES.find((m) => m.value === filters.mode)?.hint}
              onChange={(e) => update({ mode: e.target.value as ChatSearchMode })}
            >
              {CHAT_SEARCH_MODES.map((m) => (
                <option key={m.value} value={m.value} title={m.hint}>{m.label}</option>
              ))}
            </select>
          </Field>
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
        <div className="flex flex-wrap items-center gap-1.5 border-b border-border-soft px-4 py-2 text-[11px]" aria-label="Quick filters">
          <button
            type="button"
            aria-pressed={filters.enriched === 'yes'}
            onClick={() => update({ enriched: filters.enriched === 'yes' ? 'all' : 'yes' })}
            className={`rounded border px-2 py-0.5 transition-colors ${filters.enriched === 'yes' ? 'border-border-strong bg-panel-hover text-text' : 'border-border text-text-soft hover:border-border-strong'}`}
          >
            Enriched only{facets?.enriched !== undefined ? ` (${facets.enriched})` : ''}
          </button>
          {(facets?.tag_counts ?? []).slice(0, 12).map((t) => (
            <button
              key={t.tag}
              type="button"
              aria-pressed={filters.tag === t.tag}
              onClick={() => update({ tag: filters.tag === t.tag ? '' : t.tag })}
              className={`rounded border px-2 py-0.5 transition-colors ${filters.tag === t.tag ? 'border-border-strong bg-panel-hover text-text' : 'border-border bg-panel-2/50 text-text-soft hover:border-border-strong'}`}
            >
              {t.tag} <span className="tabular-nums text-text-subtle">{t.count}</span>
            </button>
          ))}
        </div>
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
            {filters.search && noticeText(notice) && (
              <p role="status" className="pb-3 text-xs text-text-subtle">{noticeText(notice)}</p>
            )}
            <EmptyState variant="no-results" title="No chats in view" description="Adjust filters, or import agent sessions." />
          </div>
        ) : (
          <>
            {filters.search && noticeText(notice) && (
              <p role="status" className="px-4 pt-3 text-xs text-text-subtle">{noticeText(notice)}</p>
            )}
            <ChatsTable items={items} sort={filters.sort} desc={filters.desc} onSort={handleSort} onOpen={setOpenId} selectedId={selectedId} />
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
