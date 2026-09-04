import { useEffect, useRef, useState } from 'react'
import { BookOpen, RefreshCw } from 'lucide-react'
import { Button, EmptyState, ListPageLayout, PageHeader, Skeleton } from '@hollis-labs/sysop-ui'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import { ReaderCard } from '@/components/reader/ReaderCard'
import { ReaderContentRenderer } from '@/features/reader'
import { useApi } from '@/hooks/useApi'
import { isReaderScope, READER_SCOPES, readerScopeLabel } from '@/lib/reader'
import type { ReaderItem, ReaderScope } from '@/lib/types'

function ReaderListSkeleton() {
  return (
    <div
      className="mx-auto flex w-full max-w-[76rem] flex-col gap-3 px-4 py-5 sm:px-6"
      role="status"
      aria-label="Loading Reader"
    >
      {Array.from({ length: 5 }).map((_, index) => (
        <Skeleton key={index} className="h-56 w-full rounded-sm" />
      ))}
    </div>
  )
}

function listErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'The Reader list could not be loaded.'
}

export default function ReaderPage() {
  const api = useApi()
  const location = useLocation()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const requestedScopes = searchParams.getAll('scope')
  const requestedScope = requestedScopes[0] ?? null
  const scopeQueryIsValid = requestedScopes.length === 1 && isReaderScope(requestedScope)
  const scope: ReaderScope =
    scopeQueryIsValid ? requestedScope : 'inbox'

  const [items, setItems] = useState<ReaderItem[]>([])
  const [nextCursor, setNextCursor] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<string>()
  const [moreError, setMoreError] = useState<string>()
  const [reload, setReload] = useState(0)
  const requestGeneration = useRef(0)
  const activeScope = useRef<ReaderScope>(scope)
  activeScope.current = scope

  useEffect(() => {
    if (!scopeQueryIsValid) {
      setSearchParams({ scope: 'inbox' }, { replace: true })
    }
  }, [scopeQueryIsValid, setSearchParams])

  useEffect(() => {
    const controller = new AbortController()
    const generation = ++requestGeneration.current
    setLoading(true)
    setLoadingMore(false)
    setError(undefined)
    setMoreError(undefined)

    void api
      .fetchReaderItems({ scope }, { signal: controller.signal })
      .then((response) => {
        if (controller.signal.aborted || generation !== requestGeneration.current) return
        setItems(response.items)
        setNextCursor(response.next_cursor)
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted || generation !== requestGeneration.current) return
        setItems([])
        setNextCursor(undefined)
        setError(listErrorMessage(reason))
      })
      .finally(() => {
        if (!controller.signal.aborted && generation === requestGeneration.current) setLoading(false)
      })

    return () => controller.abort()
  }, [api, reload, scope])

  async function loadMore() {
    if (!nextCursor || loadingMore) return
    const generation = requestGeneration.current
    const requestedScope = scope
    setLoadingMore(true)
    setMoreError(undefined)
    try {
      const response = await api.fetchReaderItems({ scope, cursor: nextCursor })
      if (generation !== requestGeneration.current || activeScope.current !== requestedScope) return
      setItems((current) => {
        const seen = new Set(current.map((item) => item.fragment_id))
        return [...current, ...response.items.filter((item) => !seen.has(item.fragment_id))]
      })
      setNextCursor(response.next_cursor)
    } catch (reason) {
      if (generation !== requestGeneration.current || activeScope.current !== requestedScope) return
      setMoreError(listErrorMessage(reason))
    } finally {
      if (generation === requestGeneration.current && activeScope.current === requestedScope) {
        setLoadingMore(false)
      }
    }
  }

  function openItem(fragmentId: string) {
    navigate(`/reader/${encodeURIComponent(fragmentId)}`, {
      state: { readerReturnPath: `${location.pathname}${location.search}` },
    })
  }

  return (
    <ListPageLayout
      header={
        <PageHeader title="Reader">
          <Button
            variant="outline"
            size="sm"
            className="min-h-11 sm:min-h-8"
            onClick={() => setReload((value) => value + 1)}
            disabled={loading}
          >
            <RefreshCw
              className={`h-3.5 w-3.5 ${loading ? 'animate-spin motion-reduce:animate-none' : ''}`}
              aria-hidden="true"
            />
            Refresh
          </Button>
        </PageHeader>
      }
      tabs={
        <nav
          className="flex min-h-11 items-center gap-1 overflow-x-auto border-b border-border-soft bg-bg px-4 sm:px-6"
          aria-label="Reader scope"
        >
          {READER_SCOPES.map((candidate) => (
            <Link
              key={candidate}
              to={`/reader?scope=${candidate}`}
              aria-current={candidate === scope ? 'page' : undefined}
              className={`inline-flex min-h-11 shrink-0 items-center rounded-sm px-3 text-[13px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring sm:min-h-9 ${
                candidate === scope
                  ? 'bg-panel-2 text-text'
                  : 'text-text-subtle hover:bg-panel-hover-soft hover:text-text'
              }`}
            >
              {readerScopeLabel(candidate)}
            </Link>
          ))}
        </nav>
      }
    >
      {loading ? (
        <ReaderListSkeleton />
      ) : error ? (
        <div className="mx-auto w-full max-w-3xl px-4 py-16">
          <EmptyState
            variant="error"
            title="Reader could not load"
            description={error}
            action={{ label: 'Try again', onClick: () => setReload((value) => value + 1) }}
          />
        </div>
      ) : items.length === 0 ? (
        <div className="mx-auto w-full max-w-3xl px-4 py-16">
          <EmptyState
            variant="empty"
            title={`No fragments in ${scope}`}
            description="Captured fragments appear here as soon as their manifest is accepted, even while enrichment or media work continues."
          />
        </div>
      ) : (
        <div className="mx-auto flex w-full max-w-[76rem] flex-col gap-3 px-4 py-5 sm:px-6 sm:py-6">
          <div className="flex items-center gap-2 pb-1 text-[12px] text-text-subtle">
            <BookOpen className="h-3.5 w-3.5" aria-hidden="true" />
            <span>{items.length === 1 ? '1 fragment' : `${items.length} fragments`}</span>
          </div>
          {items.map((item) => (
            <ReaderCard
              key={item.fragment_id}
              item={item}
              onOpen={openItem}
              mediaSlot={<ReaderContentRenderer item={item} presentation="card" />}
            />
          ))}
          {moreError && (
            <div className="flex flex-wrap items-center justify-between gap-3 border-t border-border-soft py-4 text-[13px] text-danger-soft">
              <span>{moreError}</span>
              <Button
                variant="outline"
                size="sm"
                className="min-h-11 sm:min-h-8"
                onClick={() => void loadMore()}
              >
                Try again
              </Button>
            </div>
          )}
          {nextCursor && !moreError && (
            <div className="flex justify-center border-t border-border-soft py-5">
              <Button
                variant="outline"
                className="min-h-11 sm:min-h-9"
                onClick={() => void loadMore()}
                disabled={loadingMore}
              >
                {loadingMore ? 'Loading…' : 'Load more'}
              </Button>
            </div>
          )}
        </div>
      )}
    </ListPageLayout>
  )
}
