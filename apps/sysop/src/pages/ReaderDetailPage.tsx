import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Copy, ExternalLink, RefreshCw } from 'lucide-react'
import {
  Button,
  DetailPageLayout,
  EmptyState,
  Skeleton,
} from '@hollis-labs/sysop-ui'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ReaderQuickActionSeam } from '@/components/reader/ReaderQuickActionSeam'
import { ReaderActionPill, ReaderEffectActions, ReaderReadingControls } from '@/components/reader/ReaderInlineActions'
import { ReaderNotes } from '@/components/reader/ReaderNotes'
import { ReaderStateSummary } from '@/components/reader/ReaderStateSummary'
import { ReaderTags } from '@/components/reader/ReaderTags'
import { ReaderDetailHeader } from '@/components/reader/ReaderDetailHeader'
import { ReaderContentRenderer } from '@/features/reader'
import { useApi } from '@/hooks/useApi'
import {
  isReaderBodyBackedText,
  readerPlainTextExcerpt,
  safeReaderSourceHref,
  sourceHost,
  sourceLabel,
} from '@/lib/reader'
import type { ReaderItem } from '@/lib/types'

export interface ReaderRevisionPin {
  fragmentId: string
  fragmentRevisionId: string
}

export interface ReaderDetailPageProps {
  renderContent?: (item: ReaderItem, pin: ReaderRevisionPin) => ReactNode
  renderActions?: (item: ReaderItem, pin: ReaderRevisionPin) => ReactNode
  renderSidecar?: (pin: ReaderRevisionPin) => ReactNode
}

interface ReaderReturnState {
  readerReturnPath?: string
}

interface ReaderNeighbors {
  fragmentId?: string
  previous?: string
  next?: string
}

function detailErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'The Reader item could not be loaded.'
}

function isReaderListPath(value: unknown): value is string {
  return typeof value === 'string' && /^\/reader(?:\?|$)/.test(value)
}

function shortRevision(value: string): string {
  return value.length > 16 ? `${value.slice(0, 12)}…` : value
}

export default function ReaderDetailPage({
  renderContent,
  renderActions,
  renderSidecar,
}: ReaderDetailPageProps) {
  const api = useApi()
  const navigate = useNavigate()
  const location = useLocation()
  const { fragmentId = '' } = useParams()
  const [searchParams] = useSearchParams()
  const revisionValues = searchParams.getAll('revision_id')
  const revisionId = revisionValues.length === 1 ? revisionValues[0].trim() : undefined
  const invalidRevision = revisionValues.length > 1 || (revisionValues.length === 1 && !revisionId)

  const [item, setItem] = useState<ReaderItem>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [reload, setReload] = useState(0)
  const [copyStatus, setCopyStatus] = useState<string>()
  const [neighbors, setNeighbors] = useState<ReaderNeighbors>({})
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    scrollRef.current?.classList.add('reader-hide-scrollbar')
  }, [])

  useEffect(() => {
    if (invalidRevision) {
      setItem(undefined)
      setLoading(false)
      setError('The revision link is invalid.')
      return
    }

    const controller = new AbortController()
    setLoading(true)
    setError(undefined)
    setCopyStatus(undefined)

    void api
      .fetchReaderItem({ fragmentId, revisionId }, { signal: controller.signal })
      .then((response) => {
        if (controller.signal.aborted) return
        setItem(response)
        if (response.fragment_id !== fragmentId) {
          navigate(`/reader/${encodeURIComponent(response.fragment_id)}${location.search}`, {
            replace: true,
            state: location.state,
          })
        }
      })
      .catch((reason: unknown) => {
        if (controller.signal.aborted) return
        setItem(undefined)
        setError(detailErrorMessage(reason))
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })

    return () => controller.abort()
  }, [api, fragmentId, invalidRevision, location.search, location.state, navigate, reload, revisionId])

  useEffect(() => {
    if (!item?.fragment_id || revisionId) {
      return
    }
    const controller = new AbortController()
    void (async () => {
      const ids: string[] = []
      const seenIDs = new Set<string>()
      const seenCursors = new Set<string>()
      let cursor: string | undefined

      for (let page = 0; page < 100; page += 1) {
        const response = await api.fetchReaderItems({ scope: 'inbox', cursor }, { signal: controller.signal })
        for (const candidate of response.items) {
          if (seenIDs.has(candidate.fragment_id)) continue
          seenIDs.add(candidate.fragment_id)
          ids.push(candidate.fragment_id)
        }
        if (!response.next_cursor || seenCursors.has(response.next_cursor)) break
        seenCursors.add(response.next_cursor)
        cursor = response.next_cursor
      }

      if (controller.signal.aborted) return
      const index = ids.indexOf(item.fragment_id)
      setNeighbors(index < 0 ? {} : {
        fragmentId: item.fragment_id,
        previous: ids[index - 1],
        next: ids[index + 1],
      })
    })().catch(() => {
      if (!controller.signal.aborted) setNeighbors({})
    })
    return () => controller.abort()
  }, [api, item?.fragment_id, revisionId])

  const pin = useMemo<ReaderRevisionPin | undefined>(
    () =>
      item
        ? { fragmentId: item.fragment_id, fragmentRevisionId: item.fragment_revision_id }
        : undefined,
    [item],
  )
  const sidecar = pin ? renderSidecar?.(pin) : undefined
  const sourceHref = item ? safeReaderSourceHref(item) : undefined
  const activeNeighbors = !revisionId && neighbors.fragmentId === item?.fragment_id ? neighbors : {}

  function goBack() {
    const state = location.state as ReaderReturnState | null
    if (isReaderListPath(state?.readerReturnPath)) {
      navigate(-1)
      return
    }
    navigate('/reader?scope=inbox')
  }

  function openNeighbor(nextFragmentID: string | undefined) {
    if (!nextFragmentID) return
    navigate(`/reader/${encodeURIComponent(nextFragmentID)}`, { state: location.state })
  }

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return
      if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
      const target = event.target
      if (
        target instanceof Element &&
        target.closest('input, textarea, select, [contenteditable="true"], [role="dialog"]')
      ) return
      const destination = event.key === 'ArrowLeft' ? activeNeighbors.previous : activeNeighbors.next
      if (!destination) return
      event.preventDefault()
      openNeighbor(destination)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  })

  async function copyLink() {
    try {
      if (!navigator.clipboard?.writeText) throw new Error('Clipboard is unavailable')
      await navigator.clipboard.writeText(window.location.href)
      setCopyStatus('Link copied')
    } catch {
      setCopyStatus('Could not copy link')
    }
  }

  const header = (
    <ReaderDetailHeader
      title={item?.display.title.value || (loading ? 'Loading fragment' : 'Reader item')}
      onBack={goBack}
      onPrevious={() => openNeighbor(activeNeighbors.previous)}
      onNext={() => openNeighbor(activeNeighbors.next)}
      hasPrevious={Boolean(activeNeighbors.previous)}
      hasNext={Boolean(activeNeighbors.next)}
      readingState={item ? <ReaderReadingControls item={item} onItemChange={setItem} /> : undefined}
      actions={
        <>
          {item && pin && (
            renderActions ? (
              <ReaderQuickActionSeam>{renderActions(item, pin)}</ReaderQuickActionSeam>
            ) : (
              <ReaderEffectActions key={item.fragment_id} item={item} onItemChange={setItem} />
            )
          )}
          <Button
            variant="outline"
            size="sm"
            className="min-h-11 sm:min-h-8"
            onClick={() => void copyLink()}
            disabled={!item}
          >
            <Copy className="h-3.5 w-3.5" aria-hidden="true" />
            Copy link
          </Button>
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
          {copyStatus && (
            <span className="text-[12px] text-text-subtle" role="status" aria-live="polite">
              {copyStatus}
            </span>
          )}
        </>
      }
    />
  )

  return (
    <DetailPageLayout
      header={header}
      scrollRef={scrollRef}
      aside={
        sidecar && pin ? (
          <aside
            className="h-full border-l border-border-soft bg-panel-2/20"
            data-reader-sidecar-pin
            data-fragment-id={pin.fragmentId}
            data-fragment-revision-id={pin.fragmentRevisionId}
          >
            {sidecar}
          </aside>
        ) : undefined
      }
      asideClassName="w-[22rem]"
    >
      {loading ? (
        <div
          className="mx-auto flex w-full max-w-[76rem] flex-col gap-4 px-4 py-6 sm:px-6"
          role="status"
          aria-label="Loading Reader item"
        >
          <Skeleton className="h-24 w-full rounded-sm" />
          <Skeleton className="h-48 w-full rounded-sm" />
          <Skeleton className="h-28 w-full rounded-sm" />
        </div>
      ) : error || !item || !pin ? (
        <div className="mx-auto w-full max-w-3xl px-4 py-16">
          <EmptyState
            variant="error"
            title="Reader item unavailable"
            description={error ?? 'The Reader item could not be loaded.'}
            action={{ label: 'Back to Reader', onClick: goBack }}
          />
        </div>
      ) : (
        <ReaderDetailBody
          item={item}
          pin={pin}
          sourceHref={sourceHref}
          renderContent={renderContent}
          onItemChange={setItem}
        />
      )}
    </DetailPageLayout>
  )
}

function ReaderDetailBody({
  item,
  pin,
  sourceHref,
  renderContent,
  onItemChange,
}: {
  item: ReaderItem
  pin: ReaderRevisionPin
  sourceHref?: string
  renderContent?: ReaderDetailPageProps['renderContent']
  onItemChange: (item: ReaderItem) => void
}) {
  const body = item.article.preview_markdown
  const summaryIsBody = isReaderBodyBackedText(item.display.summary.value, body)
  const description = item.display.description?.value ?? ''
  const descriptionIsDuplicate =
    isReaderBodyBackedText(description, body) ||
    isReaderBodyBackedText(description, item.display.summary.value)
  const summary = summaryIsBody ? '' : readerPlainTextExcerpt(item.display.summary.value, 1200)
  const distinctDescription = descriptionIsDuplicate
    ? ''
    : readerPlainTextExcerpt(description, 1400)

  return (
    <div
      className="mx-auto flex w-full max-w-[76rem] flex-col gap-7 px-4 py-6 sm:px-6 sm:py-8"
      data-reader-revision-pin
      data-fragment-id={pin.fragmentId}
      data-fragment-revision-id={pin.fragmentRevisionId}
    >
      <section className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_16rem] lg:items-start">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-text-subtle">
            <span className="font-medium text-text-soft">{sourceLabel(item)}</span>
            {sourceHost(item) && <span>{sourceHost(item)}</span>}
            <span>{item.capture_count === 1 ? 'Captured once' : `Captured ${item.capture_count} times`}</span>
            <span
              title={`Revision ${item.fragment_revision_id}`}
              aria-label={`Revision ${item.fragment_revision_id}`}
            >
              Revision {shortRevision(item.fragment_revision_id)}
            </span>
          </div>
          {item.display.byline?.value && (
            <p className="mt-3 text-[13px] text-text-muted">By {item.display.byline.value}</p>
          )}
          {summary && (
            <p className="mt-4 max-w-[70ch] text-[16px] leading-[1.65] text-text-muted">
              {summary}
            </p>
          )}
          {distinctDescription && (
            <p className="mt-3 max-w-[70ch] text-[14px] leading-[1.6] text-text-soft">
              {distinctDescription}
            </p>
          )}
          <p className="mt-3 text-[11px] text-text-subtle">
            Title from {item.display.title.source}; summary from {item.display.summary.source}
          </p>
          <div className="mt-4">
            <ReaderTags item={item} onItemChange={onItemChange} />
          </div>
        </div>

        {sourceHref && (
          <a
            href={sourceHref}
            target="_blank"
            rel="noreferrer"
            className="inline-flex min-h-11 items-center justify-center gap-2 rounded-sm border border-border bg-panel-2/35 px-3 text-[13px] font-medium text-text-muted outline-none hover:bg-panel-hover hover:text-text focus-visible:ring-2 focus-visible:ring-ring lg:justify-start"
          >
            View original source
            <ExternalLink className="h-3.5 w-3.5" aria-hidden="true" />
          </a>
        )}
      </section>

      <div className="w-full" data-reader-reading-stage>
        {['image', 'gallery', 'video', 'audio', 'document'].includes(item.renderer) &&
          item.operations.acquisition.available === 0 && (
          <div className="mb-4 flex min-h-24 flex-wrap items-center justify-between gap-4 border-y border-border-soft py-4">
            <div>
              <p className="text-[13px] font-medium text-text">Captured media is not loaded</p>
              <p className="mt-1 text-[12px] text-text-subtle">Load an available representation to preview it here.</p>
            </div>
            <ReaderActionPill item={item} onItemChange={onItemChange} command="request_asset_acquisition" />
          </div>
        )}
        {renderContent ? (
          renderContent(item, pin)
        ) : (
          <ReaderContentRenderer item={item} presentation="detail" />
        )}
      </div>

      <ReaderStateSummary item={item} />

      <section className="border-t border-border-soft pt-6">
        <ReaderNotes item={item} onItemChange={onItemChange} />
      </section>
    </div>
  )
}
