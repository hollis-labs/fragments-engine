import { useEffect, useState } from 'react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { IngestSchedule, IngestSummary } from '@/lib/api'
import type { JsonObject } from '@/lib/types'

const FIELD =
  'h-8 w-full rounded-md border border-border bg-bg px-2 text-sm text-text outline-none transition focus:border-border-strong'
const LABEL = 'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'

/** Ingest kinds the backend accepts (ingest.DefaultSources). */
const INGEST_KINDS = ['claude_code', 'chatgpt_export', 'url_source'] as const

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className={LABEL}>{label}</span>
      {children}
    </label>
  )
}

function Checkbox({
  label,
  checked,
  onChange,
}: {
  label: string
  checked: boolean
  onChange: (next: boolean) => void
}) {
  return (
    <label className="flex items-center gap-2 text-[13px] text-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => onChange(e.target.checked)}
        className="size-3.5 accent-[var(--color-text-soft)]"
      />
      {label}
    </label>
  )
}

function errMessage(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : 'Request failed'
}

/** Parses a numeric string, returning undefined for blank/invalid input. */
function numOrUndefined(value: string): number | undefined {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  const n = Number(trimmed)
  return Number.isFinite(n) ? n : undefined
}

/* ─────────────────────────── Ingest source create / edit ────────────────────────── */

interface IngestEditDialogProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  /** When set, the dialog edits this source; when omitted, it creates a new one. */
  ingest?: IngestSummary | null
}

export function IngestEditDialog({ open, onClose, onSaved, ingest }: IngestEditDialogProps) {
  const api = useApi()
  const isEdit = !!ingest

  const [name, setName] = useState('')
  const [kind, setKind] = useState<string>('claude_code')
  const [enabled, setEnabled] = useState(true)
  const [sourceRoot, setSourceRoot] = useState('')
  const [namespace, setNamespace] = useState('')

  // Per-kind rules fields (string-backed; parsed on submit).
  const [maxFileSizeMb, setMaxFileSizeMb] = useState('')
  const [archiveRoot, setArchiveRoot] = useState('')
  const [copyTextExports, setCopyTextExports] = useState(false)
  const [deleteCopiedSource, setDeleteCopiedSource] = useState(false)
  const [requestTimeoutSeconds, setRequestTimeoutSeconds] = useState('')
  const [maxBodyMb, setMaxBodyMb] = useState('')
  const [userAgent, setUserAgent] = useState('')

  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName(ingest?.name ?? '')
    setKind(ingest?.kind ?? 'claude_code')
    setEnabled(ingest?.enabled ?? true)
    setSourceRoot(ingest?.source_root ?? '')
    setNamespace(ingest?.namespace ?? '')
    // The IngestSummary only echoes chatgpt_export archive rules; other rules
    // cannot be prefilled and submitting clears them (see dialog hint).
    setMaxFileSizeMb('')
    setArchiveRoot(ingest?.archive_root ?? '')
    setCopyTextExports(ingest?.copy_text_exports ?? false)
    setDeleteCopiedSource(ingest?.delete_copied_source ?? false)
    setRequestTimeoutSeconds('')
    setMaxBodyMb('')
    setUserAgent('')
    setSubmitting(false)
    setError(null)
  }, [open, ingest])

  function buildRules(): JsonObject {
    const rules: JsonObject = {}
    if (kind === 'claude_code') {
      const v = numOrUndefined(maxFileSizeMb)
      if (v !== undefined) rules.max_file_size_mb = v
    } else if (kind === 'chatgpt_export') {
      const v = numOrUndefined(maxFileSizeMb)
      if (v !== undefined) rules.max_file_size_mb = v
      if (archiveRoot.trim()) rules.archive_root = archiveRoot.trim()
      rules.copy_text_exports = copyTextExports
      rules.delete_copied_source = deleteCopiedSource
    } else if (kind === 'url_source') {
      const timeout = numOrUndefined(requestTimeoutSeconds)
      if (timeout !== undefined) rules.request_timeout_seconds = timeout
      const body = numOrUndefined(maxBodyMb)
      if (body !== undefined) rules.max_body_mb = body
      if (userAgent.trim()) rules.user_agent = userAgent.trim()
    }
    return rules
  }

  async function handleSubmit() {
    if (!name.trim()) {
      setError('Name is required.')
      return
    }
    if (!sourceRoot.trim()) {
      setError('Source root is required.')
      return
    }
    setSubmitting(true)
    setError(null)
    const input = {
      name: name.trim(),
      kind,
      enabled,
      sourceRoot: sourceRoot.trim(),
      namespace: namespace.trim(),
      rules: buildRules(),
    }
    try {
      if (isEdit) {
        await api.updateIngest(input)
      } else {
        await api.createIngest(input)
      }
      onSaved()
      onClose()
    } catch (err) {
      setError(errMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o: boolean) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          {isEdit ? `Edit ingest: ${ingest?.name}` : 'New ingest source'}
        </DialogTitle>
        <div className="mt-3 flex flex-col gap-3">
          <Field label="Name">
            <input
              className={FIELD}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="claude-history"
              disabled={isEdit}
            />
          </Field>
          <Field label="Kind">
            <select
              className={FIELD}
              value={kind}
              onChange={(e) => setKind(e.target.value)}
            >
              {INGEST_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Source root">
            <input
              className={FIELD}
              value={sourceRoot}
              onChange={(e) => setSourceRoot(e.target.value)}
              placeholder="/Users/you/.claude/projects"
            />
          </Field>
          <Field label="Namespace">
            <input
              className={FIELD}
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              placeholder="inbox"
            />
          </Field>
          <Checkbox label="Enabled" checked={enabled} onChange={setEnabled} />

          {/* Per-kind rules */}
          {kind === 'claude_code' && (
            <Field label="Max file size (MB)">
              <input
                className={FIELD}
                value={maxFileSizeMb}
                onChange={(e) => setMaxFileSizeMb(e.target.value)}
                inputMode="numeric"
                placeholder="optional"
              />
            </Field>
          )}
          {kind === 'chatgpt_export' && (
            <>
              <Field label="Max file size (MB)">
                <input
                  className={FIELD}
                  value={maxFileSizeMb}
                  onChange={(e) => setMaxFileSizeMb(e.target.value)}
                  inputMode="numeric"
                  placeholder="optional"
                />
              </Field>
              <Field label="Archive root">
                <input
                  className={FIELD}
                  value={archiveRoot}
                  onChange={(e) => setArchiveRoot(e.target.value)}
                  placeholder="optional"
                />
              </Field>
              <Checkbox
                label="Copy text exports"
                checked={copyTextExports}
                onChange={setCopyTextExports}
              />
              <Checkbox
                label="Delete copied source"
                checked={deleteCopiedSource}
                onChange={setDeleteCopiedSource}
              />
            </>
          )}
          {kind === 'url_source' && (
            <>
              <Field label="Request timeout (seconds)">
                <input
                  className={FIELD}
                  value={requestTimeoutSeconds}
                  onChange={(e) => setRequestTimeoutSeconds(e.target.value)}
                  inputMode="numeric"
                  placeholder="optional"
                />
              </Field>
              <Field label="Max body (MB)">
                <input
                  className={FIELD}
                  value={maxBodyMb}
                  onChange={(e) => setMaxBodyMb(e.target.value)}
                  inputMode="numeric"
                  placeholder="optional"
                />
              </Field>
              <Field label="User agent">
                <input
                  className={FIELD}
                  value={userAgent}
                  onChange={(e) => setUserAgent(e.target.value)}
                  placeholder="optional"
                />
              </Field>
            </>
          )}

          {isEdit && (
            <p className="text-[11px] text-text-subtle">
              Update is a full-record replace. Rules fields left blank are cleared on save —
              re-enter any rules you want to keep.
            </p>
          )}
          {error && <p className="text-sm text-danger-soft">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button size="sm" onClick={handleSubmit} disabled={submitting}>
              {submitting ? 'Saving…' : isEdit ? 'Save changes' : 'Create ingest'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

/* ─────────────────────────── Ingest schedule create / edit ────────────────────────── */

interface IngestScheduleDialogProps {
  open: boolean
  onClose: () => void
  onSaved: () => void
  ingests: IngestSummary[]
  /** When set, the dialog edits this schedule; when omitted, it creates a new one. */
  schedule?: IngestSchedule | null
}

export function IngestScheduleDialog({
  open,
  onClose,
  onSaved,
  ingests,
  schedule,
}: IngestScheduleDialogProps) {
  const api = useApi()
  const isEdit = !!schedule

  const [ingestName, setIngestName] = useState('')
  const [cronExpr, setCronExpr] = useState('')
  const [enabled, setEnabled] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setIngestName(schedule?.ingest_name ?? ingests[0]?.name ?? '')
    setCronExpr(schedule?.cron_expr ?? '')
    setEnabled(schedule?.enabled ?? true)
    setSubmitting(false)
    setError(null)
  }, [open, schedule, ingests])

  async function handleSubmit() {
    if (!ingestName) {
      setError('Pick an ingest source.')
      return
    }
    if (!cronExpr.trim()) {
      setError('Cron expression is required.')
      return
    }
    setSubmitting(true)
    setError(null)
    const input = { ingestName, cronExpr: cronExpr.trim(), enabled }
    try {
      if (isEdit && schedule) {
        await api.updateIngestSchedule(schedule.id, input)
      } else {
        await api.createIngestSchedule(input)
      }
      onSaved()
      onClose()
    } catch (err) {
      setError(errMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o: boolean) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          {isEdit ? 'Edit schedule' : 'New schedule'}
        </DialogTitle>
        <div className="mt-3 flex flex-col gap-3">
          <Field label="Ingest source">
            <select
              className={FIELD}
              value={ingestName}
              onChange={(e) => setIngestName(e.target.value)}
            >
              {ingests.length === 0 && <option value="">No ingest sources</option>}
              {ingests.map((ingest) => (
                <option key={ingest.name} value={ingest.name}>
                  {ingest.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Cron expression">
            <input
              className={`${FIELD} font-mono`}
              value={cronExpr}
              onChange={(e) => setCronExpr(e.target.value)}
              placeholder="0 * * * *"
              spellCheck={false}
            />
          </Field>
          <p className="text-[11px] text-text-subtle">
            Standard 5-field cron (minute hour day-of-month month day-of-week).
          </p>
          <Checkbox label="Enabled" checked={enabled} onChange={setEnabled} />
          {error && <p className="text-sm text-danger-soft">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button size="sm" onClick={handleSubmit} disabled={submitting}>
              {submitting ? 'Saving…' : isEdit ? 'Save changes' : 'Create schedule'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

/* ─────────────────────────────── Confirm dialog ──────────────────────────────── */

interface ConfirmDialogProps {
  open: boolean
  title: string
  body: string
  confirmLabel?: string
  onConfirm: () => void
  onClose: () => void
  /** When set, shown as an error inside the dialog instead of closing on failure. */
  error?: string | null
  busy?: boolean
}

export function ConfirmDialog({
  open,
  title,
  body,
  confirmLabel = 'Delete',
  onConfirm,
  onClose,
  error,
  busy,
}: ConfirmDialogProps) {
  return (
    <Dialog open={open} onOpenChange={(o: boolean) => !o && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          {title}
        </DialogTitle>
        <p className="mt-2 text-[13px] text-text-muted">{body}</p>
        {error && <p className="mt-2 text-sm text-danger-soft">{error}</p>}
        <div className="mt-3 flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="destructive" size="sm" onClick={onConfirm} disabled={busy}>
            {busy ? 'Working…' : confirmLabel}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
