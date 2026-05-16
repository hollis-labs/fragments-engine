import { useEffect, useState } from 'react'
import { Waypoints } from 'lucide-react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { StatusBadge } from './status-badge'
import { CopyableId } from './copyable-id'
import { ApplyRouteDialog } from './apply-route-dialog'
import { useApi } from '@/hooks/useApi'
import { ApiError, type FragmentDetail } from '@/lib/api'
import { formatRelativeTime, formatShortDate } from '@/lib/utils'

interface FragmentDetailDialogProps {
  /** Fragment to show; the dialog is open whenever this is non-null. */
  fragmentId: string | null
  onClose: () => void
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="border-t border-border-strong px-4 py-3">
      <p className="mb-2 text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle">
        {title}
      </p>
      {children}
    </section>
  )
}

function DetailBody({ detail, onRoute }: { detail: FragmentDetail; onRoute: () => void }) {
  const { fragment, entities, attachments, route_log, related } = detail

  return (
    <>
      {/* Header */}
      <div className="flex flex-col gap-2 px-4 pb-3 pr-10">
        <div className="flex items-start gap-2">
          <StatusBadge status={fragment.status} className="mt-0.5 shrink-0" />
          <DialogTitle
            className="line-clamp-2 min-w-0 break-words text-base font-semibold leading-snug tracking-tight text-text"
            title={fragment.title || undefined}
          >
            {fragment.title || '(untitled fragment)'}
          </DialogTitle>
        </div>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-text-subtle">
          <span className="font-mono text-text-subtle/80">id:</span>
          <CopyableId id={fragment.id} />
          <span className="uppercase tracking-[.12em] text-text-soft">
            {fragment.source}
            {fragment.source_type ? ` · ${fragment.source_type}` : ''}
          </span>
          {fragment.ingest_name && <span>ingest: {fragment.ingest_name}</span>}
          {fragment.created_at && (
            <span title={fragment.created_at}>
              {formatShortDate(fragment.created_at)} · {formatRelativeTime(fragment.created_at)}
            </span>
          )}
        </div>
        <div>
          <Button variant="outline" size="sm" onClick={onRoute}>
            <Waypoints className="h-3.5 w-3.5" />
            Route
          </Button>
        </div>
      </div>

      {fragment.summary && (
        <Section title="Summary">
          <p className="text-sm leading-6 text-text-muted">{fragment.summary}</p>
        </Section>
      )}

      <Section title="Content">
        {fragment.content ? (
          <pre className="max-h-72 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-bg p-3 text-[12px] leading-5 text-text-muted">
            {fragment.content}
          </pre>
        ) : (
          <p className="text-sm text-text-subtle">No content.</p>
        )}
      </Section>

      <Section title={`Entities (${entities.length})`}>
        {entities.length > 0 ? (
          <div className="flex flex-wrap gap-1.5">
            {entities.map((e, i) => (
              <span
                key={`${e.kind}:${e.value}:${i}`}
                className="inline-flex items-center gap-1 rounded border border-border bg-panel-2/50 px-2 py-0.5 text-[11px] text-text-soft"
                title={`source: ${e.source} · confidence: ${e.confidence}`}
              >
                <span className="text-text-subtle">{e.kind}:</span>
                <span className="text-text">{e.value}</span>
              </span>
            ))}
          </div>
        ) : (
          <p className="text-sm text-text-subtle">No entities extracted.</p>
        )}
      </Section>

      {attachments.length > 0 && (
        <Section title={`Attachments (${attachments.length})`}>
          <ul className="flex flex-col gap-1">
            {attachments.map((a) => (
              <li key={a.id} className="flex items-center gap-2 text-[12px] text-text-muted">
                <span className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-text-subtle">
                  {a.kind}
                </span>
                <span className="truncate">{a.name}</span>
                <span className="ml-auto font-mono text-[10px] text-text-subtle">{a.mime_type}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      {route_log.length > 0 && (
        <Section title={`Route log (${route_log.length})`}>
          <ul className="flex flex-col gap-1.5">
            {route_log.map((entry) => (
              <li key={entry.id} className="text-[12px] text-text-muted">
                <span className="font-mono uppercase tracking-wider text-text-soft">
                  {entry.decision}
                </span>
                {entry.reason && <span className="text-text-subtle"> — {entry.reason}</span>}
                <span className="ml-2 text-[10px] text-text-subtle/80">
                  {formatRelativeTime(entry.created_at)}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      {related.length > 0 && (
        <Section title={`Related (${related.length})`}>
          <ul className="flex flex-col gap-1">
            {related.map((r) => (
              <li key={r.fragment.id} className="flex items-center gap-2 text-[12px]">
                <span className="truncate text-text-muted">
                  {r.fragment.title || r.fragment.id}
                </span>
                <span className="ml-auto font-mono text-[10px] tabular-nums text-text-subtle">
                  {r.score.toFixed(2)}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </>
  )
}

/** Fragment detail surfaced as a modal so the inbox stays in place. */
export function FragmentDetailDialog({ fragmentId, onClose }: FragmentDetailDialogProps) {
  const api = useApi()
  const [detail, setDetail] = useState<FragmentDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applyOpen, setApplyOpen] = useState(false)
  // Bumped after a route is applied so the detail re-fetches its new status.
  const [reloadKey, setReloadKey] = useState(0)

  useEffect(() => {
    if (!fragmentId) return
    let cancelled = false
    setDetail(null)
    setError(null)
    setLoading(true)
    void api
      .fetchFragment({ fragmentId })
      .then((result) => {
        if (!cancelled) setDetail(result)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(
          err instanceof ApiError || err instanceof Error
            ? err.message
            : 'Failed to load fragment',
        )
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [api, fragmentId, reloadKey])

  return (
    <>
      <Dialog open={fragmentId !== null} onOpenChange={(open) => !open && onClose()}>
        <DialogContent className="overflow-hidden p-0 sm:max-w-2xl">
          <div className="max-h-[80vh] overflow-y-auto">
            <div className="px-4 pt-4">
              <p className="text-[10px] font-semibold uppercase tracking-[.28em] text-text-subtle">
                Fragment
              </p>
            </div>
          {loading && (
            <div className="flex flex-col gap-3 p-4">
              <Skeleton className="h-5 w-2/3" />
              <Skeleton className="h-3 w-1/2" />
              <Skeleton className="h-32 w-full" />
            </div>
          )}
          {error && !loading && (
            <div className="p-4">
              <p className="text-sm text-danger-soft">{error}</p>
            </div>
          )}
            {detail && !loading && (
              <DetailBody detail={detail} onRoute={() => setApplyOpen(true)} />
            )}
          </div>
        </DialogContent>
      </Dialog>

      <ApplyRouteDialog
        open={applyOpen}
        onClose={() => setApplyOpen(false)}
        entityOptions={detail ? detail.entities.map((e) => ({ kind: e.kind, value: e.value })) : []}
        onApplied={() => setReloadKey((k) => k + 1)}
      />
    </>
  )
}
