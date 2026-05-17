import { useEffect, useState } from 'react'
import {
  ChevronDown,
  ChevronRight,
  FileText,
  Film,
  Image as ImageIcon,
  Music,
  Paperclip,
  RefreshCw,
  Waypoints,
} from 'lucide-react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Button } from '@/components/ui/button'
import { StatusBadge } from './status-badge'
import { CopyableId } from './copyable-id'
import { ApplyRouteDialog } from './apply-route-dialog'
import { useApi } from '@/hooks/useApi'
import { ApiError, type FragmentDetail } from '@/lib/api'
import { formatRelativeTime, formatShortDate } from '@/lib/utils'
import type { FragmentAttachment } from '@/lib/types'

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

/** Human-readable file size. */
function formatBytes(bytes: number | undefined): string {
  if (bytes === undefined || bytes < 0) return '—'
  if (bytes < 1024) return `${bytes} B`
  const units = ['KB', 'MB', 'GB']
  let value = bytes / 1024
  let unitIdx = 0
  while (value >= 1024 && unitIdx < units.length - 1) {
    value /= 1024
    unitIdx += 1
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unitIdx]}`
}

/** Renders an icon chosen by attachment kind / mime type. */
function AttachmentIcon({ attachment }: { attachment: FragmentAttachment }) {
  const probe = `${attachment.kind} ${attachment.mime_type}`.toLowerCase()
  const className = 'h-4 w-4 shrink-0 text-text-subtle'
  if (probe.includes('image')) return <ImageIcon className={className} />
  if (probe.includes('video')) return <Film className={className} />
  if (probe.includes('audio')) return <Music className={className} />
  if (probe.includes('text') || probe.includes('document') || probe.includes('pdf')) {
    return <FileText className={className} />
  }
  return <Paperclip className={className} />
}

/** Reads a string-valued metadata field, trying several common keys. */
function metaString(a: FragmentAttachment, keys: string[]): string | undefined {
  const meta = a.metadata
  if (!meta) return undefined
  for (const key of keys) {
    const v = meta[key]
    if (typeof v === 'string' && v.trim() !== '') return v
  }
  return undefined
}

/** Extracted (non-OCR) text content surfaced for an attachment, if present. */
function extractedTextOf(a: FragmentAttachment): string | undefined {
  if (typeof a.extracted_text_preview === 'string' && a.extracted_text_preview.trim() !== '') {
    return a.extracted_text_preview
  }
  return metaString(a, ['extracted_text', 'text_content', 'text', 'transcript'])
}

/** OCR result text surfaced for an attachment, if present in metadata. */
function ocrTextOf(a: FragmentAttachment): string | undefined {
  return metaString(a, ['ocr_text', 'ocr', 'ocr_result'])
}

/** Collapsible block of monospace text — collapsed by default. */
function CollapsibleText({ label, text }: { label: string; text: string }) {
  const [open, setOpen] = useState(false)
  return (
    <div className="rounded-md border border-border bg-bg">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-1.5 px-2 py-1.5 text-left text-[11px] font-semibold uppercase tracking-[.12em] text-text-subtle transition hover:text-text-soft"
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        {label}
        <span className="ml-auto font-mono text-[10px] lowercase tracking-normal text-text-subtle/70">
          {text.length} chars
        </span>
      </button>
      {open && (
        <pre className="max-h-56 overflow-auto whitespace-pre-wrap border-t border-border px-3 py-2 text-[12px] leading-5 text-text-muted">
          {text}
        </pre>
      )}
    </div>
  )
}

interface AttachmentCardProps {
  attachment: FragmentAttachment
  /** True while a re-analyze request for this attachment is in flight. */
  reanalyzing: boolean
  onReanalyze: () => void
}

/** One attachment with media metadata + extracted text / OCR / vision analysis. */
function AttachmentCard({ attachment, reanalyzing, onReanalyze }: AttachmentCardProps) {
  const a = attachment
  const extracted = extractedTextOf(a)
  const ocr = ocrTextOf(a)
  const hasVision =
    a.vision_summary !== undefined ||
    (a.vision_tags?.length ?? 0) > 0 ||
    (a.vision_entities?.length ?? 0) > 0 ||
    a.vision_backend !== undefined ||
    a.vision_confidence !== undefined
  const hasAnalysis =
    hasVision ||
    a.analysis_summary !== undefined ||
    (a.analysis_tags?.length ?? 0) > 0 ||
    extracted !== undefined ||
    ocr !== undefined ||
    a.ocr_status !== undefined ||
    (a.extracted_text_bytes ?? 0) > 0

  return (
    <li className="rounded-md border border-border bg-panel-2/40 p-3">
      {/* Media header */}
      <div className="flex items-center gap-2">
        <AttachmentIcon attachment={a} />
        <span className="truncate text-[13px] text-text" title={a.name}>
          {a.name || '(unnamed attachment)'}
        </span>
        <span className="rounded border border-border bg-bg px-1.5 py-0.5 text-[10px] uppercase tracking-wider text-text-subtle">
          {a.kind || 'file'}
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-2 font-mono text-[10px] text-text-subtle">
          <span>{formatBytes(a.size_bytes)}</span>
          <span className="text-text-subtle/70">{a.mime_type || '—'}</span>
        </span>
      </div>

      {/* Re-analyze action */}
      <div className="mt-2 flex items-center gap-2">
        <Button variant="outline" size="xs" onClick={onReanalyze} disabled={reanalyzing}>
          <RefreshCw className={`h-3 w-3 ${reanalyzing ? 'animate-spin' : ''}`} />
          {reanalyzing ? 'Re-analyzing…' : 'Re-analyze'}
        </Button>
        {!hasAnalysis && (
          <span className="text-[11px] text-text-subtle">No analysis yet.</span>
        )}
      </div>

      {/* Analysis body */}
      {hasAnalysis && (
        <div className="mt-2 flex flex-col gap-2">
          {a.analysis_summary && (
            <p className="text-[12px] leading-5 text-text-muted">{a.analysis_summary}</p>
          )}
          {a.analysis_tags && a.analysis_tags.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {a.analysis_tags.map((tag, i) => (
                <span
                  key={`atag:${tag}:${i}`}
                  className="rounded border border-border bg-bg px-1.5 py-0.5 text-[10px] text-text-soft"
                >
                  {tag}
                </span>
              ))}
            </div>
          )}

          {extracted && <CollapsibleText label="Extracted text" text={extracted} />}
          {(a.extracted_text_bytes ?? 0) > 0 && (
            <p className="text-[11px] text-text-subtle">
              {a.extracted_text_bytes!.toLocaleString()} bytes of text extracted
            </p>
          )}
          {ocr && <CollapsibleText label="OCR result" text={ocr} />}
          {a.ocr_status && (
            <p className="text-[11px] text-text-subtle">
              OCR: <span className="text-text-soft">{a.ocr_status}</span>
            </p>
          )}

          {hasVision && (
            <div className="rounded-md border border-border bg-bg p-2">
              <div className="flex items-center gap-2">
                <p className="text-[10px] font-semibold uppercase tracking-[.14em] text-text-subtle">
                  Vision analysis
                </p>
                {a.vision_backend && (
                  <span className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 font-mono text-[10px] text-text-subtle">
                    {a.vision_backend}
                  </span>
                )}
                {a.vision_confidence !== undefined && (
                  <span className="ml-auto font-mono text-[10px] tabular-nums text-text-subtle">
                    confidence {a.vision_confidence.toFixed(2)}
                  </span>
                )}
              </div>
              {a.vision_summary && (
                <p className="mt-1.5 text-[12px] leading-5 text-text-muted">{a.vision_summary}</p>
              )}
              {a.vision_tags && a.vision_tags.length > 0 && (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {a.vision_tags.map((tag, i) => (
                    <span
                      key={`vtag:${tag}:${i}`}
                      className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] text-text-soft"
                    >
                      {tag}
                    </span>
                  ))}
                </div>
              )}
              {a.vision_entities && a.vision_entities.length > 0 && (
                <div className="mt-1.5 flex flex-wrap gap-1">
                  {a.vision_entities.map((ent, i) => (
                    <span
                      key={`vent:${ent}:${i}`}
                      className="inline-flex items-center gap-1 rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px]"
                    >
                      <span className="text-text-subtle">entity:</span>
                      <span className="text-text">{ent}</span>
                    </span>
                  ))}
                </div>
              )}
              {a.vision_text_present !== undefined && (
                <p className="mt-1.5 text-[10px] text-text-subtle">
                  Text in image: {a.vision_text_present ? 'detected' : 'none'}
                </p>
              )}
            </div>
          )}
        </div>
      )}
    </li>
  )
}

interface DetailBodyProps {
  detail: FragmentDetail
  onRoute: () => void
  /** ids of attachments with an in-flight re-analyze request. */
  reanalyzingIds: Set<string>
  /** Re-analyze one attachment; pass undefined id to re-analyze the whole fragment. */
  onReanalyze: (attachmentId?: string) => void
  /** Error from the most recent re-analyze attempt, if any. */
  reanalyzeError: string | null
}

function DetailBody({
  detail,
  onRoute,
  reanalyzingIds,
  onReanalyze,
  reanalyzeError,
}: DetailBodyProps) {
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
          {reanalyzeError && (
            <p className="mb-2 text-[12px] text-danger-soft">{reanalyzeError}</p>
          )}
          <ul className="flex flex-col gap-2">
            {attachments.map((a) => (
              <AttachmentCard
                key={a.id}
                attachment={a}
                reanalyzing={reanalyzingIds.has(a.id)}
                onReanalyze={() => onReanalyze(a.id)}
              />
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
  // Bumped after a route is applied (or an attachment is re-analyzed) so the
  // detail re-fetches its new status.
  const [reloadKey, setReloadKey] = useState(0)
  // Attachment ids with an in-flight re-analyze request.
  const [reanalyzingIds, setReanalyzingIds] = useState<Set<string>>(new Set())
  const [reanalyzeError, setReanalyzeError] = useState<string | null>(null)

  useEffect(() => {
    if (!fragmentId) return
    let cancelled = false
    setDetail(null)
    setError(null)
    setReanalyzeError(null)
    setReanalyzingIds(new Set())
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

  async function handleReanalyze(attachmentId?: string) {
    if (!fragmentId) return
    setReanalyzeError(null)
    if (attachmentId) {
      setReanalyzingIds((prev) => new Set(prev).add(attachmentId))
    }
    try {
      await api.reanalyzeFragmentAttachments({ fragmentId, attachmentId })
      // Refresh the dialog so the new analysis lands.
      setReloadKey((k) => k + 1)
    } catch (err) {
      setReanalyzeError(
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'Failed to re-analyze attachment',
      )
    } finally {
      if (attachmentId) {
        setReanalyzingIds((prev) => {
          const next = new Set(prev)
          next.delete(attachmentId)
          return next
        })
      }
    }
  }

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
              <DetailBody
                detail={detail}
                onRoute={() => setApplyOpen(true)}
                reanalyzingIds={reanalyzingIds}
                onReanalyze={handleReanalyze}
                reanalyzeError={reanalyzeError}
              />
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
