import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Copy, ExternalLink, RefreshCw } from 'lucide-react'
import {
  Button,
  DetailPageLayout,
  EmptyState,
  Skeleton,
} from '@hollis-labs/sysop-ui'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ReaderQuickActionSeam } from '@/components/reader/ReaderQuickActionSeam'
import { ReaderActions } from '@/components/reader/ReaderActions'
import { ReaderStateSummary } from '@/components/reader/ReaderStateSummary'
import { ReaderDetailHeader } from '@/components/reader/ReaderDetailHeader'
import { ReaderContentRenderer } from '@/features/reader'
import { useApi } from '@/hooks/useApi'
import {
  isReaderBodyBackedText,
  readingStateLabel,
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

  const pin = useMemo<ReaderRevisionPin | undefined>(
    () =>
      item
        ? { fragmentId: item.fragment_id, fragmentRevisionId: item.fragment_revision_id }
        : undefined,
    [item],
  )
  const sidecar = pin ? renderSidecar?.(pin) : undefined
  const sourceHref = item ? safeReaderSourceHref(item) : undefined

  function goBack() {
    const state = location.state as ReaderReturnState | null
    if (isReaderListPath(state?.readerReturnPath)) {
      navigate(-1)
      return
    }
    navigate('/reader?scope=inbox')
  }

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
      readingState={item ? readingStateLabel(item.reading_state.state) : undefined}
      actions={
        <>
          {item && pin && (
            <ReaderQuickActionSeam>
              {renderActions ? (
                renderActions(item, pin)
              ) : (
                <ReaderActions key={item.fragment_id} item={item} onItemChange={setItem} />
              )}
            </ReaderQuickActionSeam>
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
        >

          {(item.tags.combined.length > 0 || item.annotations.length > 0 || item.curated_note) && (
            <section className="grid gap-7 border-t border-border-soft pt-6 lg:grid-cols-2">
              <div>
                <h2 className="text-[14px] font-semibold text-text">Tags</h2>
                {item.tags.combined.length > 0 ? (
                  <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-2" aria-label="Tags">
                    {item.tags.combined.map((tag) => (
                      <li key={tag} className="text-[13px] text-text-muted">
                        #{tag}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="mt-2 text-[13px] text-text-subtle">No tags</p>
                )}
              </div>
              <div>
                <h2 className="text-[14px] font-semibold text-text">Capture context</h2>
                {item.curated_note?.body_markdown ? (
                  <p className="mt-3 whitespace-pre-wrap text-[13px] leading-5 text-text-muted">
                    {readerPlainTextExcerpt(item.curated_note.body_markdown, 1000)}
                  </p>
                ) : item.annotations.length > 0 ? (
                  <p className="mt-3 text-[13px] leading-5 text-text-muted">
                    {item.annotations.length === 1
                      ? '1 capture annotation is attached.'
                      : `${item.annotations.length} capture annotations are attached.`}
                  </p>
                ) : (
                  <p className="mt-2 text-[13px] text-text-subtle">No capture notes</p>
                )}
              </div>
            </section>
          )}
        </ReaderDetailBody>
      )}
    </DetailPageLayout>
  )
}

function ReaderDetailBody({
  item,
  pin,
  sourceHref,
  renderContent,
  children,
}: {
  item: ReaderItem
  pin: ReaderRevisionPin
  sourceHref?: string
  renderContent?: ReaderDetailPageProps['renderContent']
  children?: ReactNode
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
        {renderContent ? (
          renderContent(item, pin)
        ) : (
          <ReaderContentRenderer item={item} presentation="detail" />
        )}
      </div>

      <ReaderStateSummary item={item} />

      {children}
    </div>
  )
}
