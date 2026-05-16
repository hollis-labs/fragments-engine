import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, Pencil, Play, Plus, RefreshCw, Trash2, XCircle } from 'lucide-react'
import { PageHeader } from '@/components/domain/page-header'
import {
  ConfirmDialog,
  IngestEditDialog,
  IngestScheduleDialog,
} from '@/components/domain/ingest-dialogs'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { IngestRunRecord, IngestSchedule, IngestSummary } from '@/lib/api'

const COLUMN_LABEL = 'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'

function errorMessage(err: unknown): string {
  if (err instanceof ApiError || err instanceof Error) return err.message
  return String(err)
}

function truncate(value: string, max = 120): string {
  return value.length > max ? `${value.slice(0, max)}…` : value
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function itemLabel(item: unknown, index: number): string {
  if (isObject(item)) {
    const label = item.title ?? item.name ?? item.id
    if (typeof label === 'string') return label
    if (typeof label === 'number') return String(label)
    return truncate(JSON.stringify(item), 120)
  }
  if (typeof item === 'string') return item
  return `Item ${index + 1}: ${truncate(JSON.stringify(item), 120)}`
}

/** Maps an ingest run status to a text colour class. */
function runStatusClass(status: string): string {
  switch (status) {
    case 'done':
      return 'text-status-indexed'
    case 'failed':
      return 'text-danger-soft'
    case 'running':
      return 'text-status-routed'
    default:
      return 'text-text-subtle'
  }
}

function orDash(value: string | undefined): string {
  return value && value.trim() !== '' ? value : '—'
}

type ValidationState =
  | { kind: 'loading' }
  | { kind: 'ok'; valid: boolean }
  | { kind: 'message'; text: string }
  | { kind: 'raw'; text: string }
  | { kind: 'error'; text: string }

function deriveValidation(result: unknown): ValidationState {
  if (isObject(result)) {
    if (typeof result.ok === 'boolean') return { kind: 'ok', valid: result.ok }
    if (typeof result.valid === 'boolean') return { kind: 'ok', valid: result.valid }
    if (typeof result.message === 'string') return { kind: 'message', text: result.message }
    if (typeof result.error === 'string') return { kind: 'message', text: result.error }
  }
  return { kind: 'raw', text: truncate(JSON.stringify(result), 120) }
}

function ValidationResult({ state }: { state: ValidationState }) {
  if (state.kind === 'loading') {
    return <span className="text-[11px] text-text-subtle">Validating…</span>
  }
  if (state.kind === 'ok') {
    return state.valid ? (
      <span className="flex items-center gap-1 text-[11px] text-status-indexed">
        <CheckCircle2 className="h-3.5 w-3.5" />
        Valid
      </span>
    ) : (
      <span className="flex items-center gap-1 text-[11px] text-danger-soft">
        <XCircle className="h-3.5 w-3.5" />
        Invalid
      </span>
    )
  }
  if (state.kind === 'error') {
    return <span className="text-[11px] text-danger-soft">{state.text}</span>
  }
  return <span className="text-[11px] text-text-muted">{state.text}</span>
}

interface PreviewState {
  name: string
  loading: boolean
  result: unknown
  error: string | null
}

function PreviewBody({ state }: { state: PreviewState }) {
  if (state.loading) {
    return (
      <div className="space-y-2">
        <Skeleton className="h-4 w-3/4" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-4 w-2/3" />
      </div>
    )
  }
  if (state.error) {
    return <p className="text-[13px] text-danger-soft">{state.error}</p>
  }
  if (Array.isArray(state.result)) {
    if (state.result.length === 0) {
      return <p className="text-[13px] text-text-muted">No preview items.</p>
    }
    return (
      <ul className="divide-y divide-border-soft rounded-md border border-border bg-bg">
        {state.result.map((item, index) => (
          <li key={index} className="px-3 py-2 text-[13px] text-text">
            {itemLabel(item, index)}
          </li>
        ))}
      </ul>
    )
  }
  return (
    <pre className="max-h-80 overflow-auto whitespace-pre-wrap rounded-md border border-border bg-bg p-3 text-[12px] text-text-muted">
      {JSON.stringify(state.result, null, 2)}
    </pre>
  )
}

function Section({
  title,
  count,
  action,
  children,
}: {
  title: string
  count?: number
  action?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="border-b border-border-strong">
      <div className="flex items-center justify-between px-4 py-2">
        <p className="text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle">
          {title}
          {count !== undefined && <span className="ml-2 text-text-subtle/70">{count}</span>}
        </p>
        {action}
      </div>
      {children}
    </section>
  )
}

export default function IngestPage() {
  const api = useApi()

  const [ingests, setIngests] = useState<IngestSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const [runs, setRuns] = useState<IngestRunRecord[]>([])
  const [runsLoading, setRunsLoading] = useState(true)

  const [schedules, setSchedules] = useState<IngestSchedule[]>([])
  const [schedulesLoading, setSchedulesLoading] = useState(true)

  const [running, setRunning] = useState(false)
  const [runNote, setRunNote] = useState<string | null>(null)
  const [runError, setRunError] = useState<string | null>(null)

  const [validations, setValidations] = useState<Record<string, ValidationState>>({})
  const [preview, setPreview] = useState<PreviewState | null>(null)

  // Dialog state. `ingest`/`schedule` undefined → create mode; set → edit mode.
  const [editDialog, setEditDialog] = useState<{ open: boolean; ingest?: IngestSummary | null }>({
    open: false,
  })
  const [scheduleDialog, setScheduleDialog] = useState<{
    open: boolean
    schedule?: IngestSchedule | null
  }>({ open: false })
  const [deleteIngestTarget, setDeleteIngestTarget] = useState<IngestSummary | null>(null)
  const [deleteScheduleTarget, setDeleteScheduleTarget] = useState<IngestSchedule | null>(null)
  const [confirmError, setConfirmError] = useState<string | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)

  const loadIngests = useCallback(async () => {
    setLoading(true)
    setLoadError(null)
    try {
      setIngests(await api.fetchIngests())
    } catch (err) {
      setLoadError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [api])

  const loadRuns = useCallback(async () => {
    setRunsLoading(true)
    try {
      setRuns(await api.fetchIngestRuns(50))
    } catch {
      // Surfaced via empty state; keep the page responsive.
    } finally {
      setRunsLoading(false)
    }
  }, [api])

  const loadSchedules = useCallback(async () => {
    setSchedulesLoading(true)
    try {
      setSchedules(await api.fetchIngestSchedules())
    } catch {
      // Surfaced via empty state; keep the page responsive.
    } finally {
      setSchedulesLoading(false)
    }
  }, [api])

  useEffect(() => {
    void loadIngests()
    void loadRuns()
    void loadSchedules()
  }, [loadIngests, loadRuns, loadSchedules])

  const handleRunAll = useCallback(async () => {
    setRunning(true)
    setRunError(null)
    setRunNote(null)
    try {
      const queued = await api.runIngests()
      setRunNote(`Queued ${queued.length} run${queued.length === 1 ? '' : 's'}.`)
      await loadRuns()
    } catch (err) {
      setRunError(errorMessage(err))
    } finally {
      setRunning(false)
    }
  }, [api, loadRuns])

  const handleRunOne = useCallback(
    async (name: string) => {
      setRunError(null)
      setRunNote(null)
      try {
        await api.runIngest(name)
        setRunNote(`Queued a run for ${name}.`)
        await loadRuns()
      } catch (err) {
        setRunError(errorMessage(err))
      }
    },
    [api, loadRuns],
  )

  const handleToggleEnabled = useCallback(
    async (ingest: IngestSummary) => {
      try {
        await api.setIngestEnabled(ingest.name, !ingest.enabled)
        await loadIngests()
      } catch (err) {
        setLoadError(errorMessage(err))
      }
    },
    [api, loadIngests],
  )

  const handleValidate = useCallback(
    async (name: string) => {
      setValidations((prev) => ({ ...prev, [name]: { kind: 'loading' } }))
      try {
        const result = await api.validateIngest(name)
        setValidations((prev) => ({ ...prev, [name]: deriveValidation(result) }))
      } catch (err) {
        setValidations((prev) => ({
          ...prev,
          [name]: { kind: 'error', text: errorMessage(err) },
        }))
      }
    },
    [api],
  )

  const handlePreview = useCallback(
    async (name: string) => {
      setPreview({ name, loading: true, result: null, error: null })
      try {
        const result = await api.previewIngest(name, 10)
        setPreview({ name, loading: false, result, error: null })
      } catch (err) {
        setPreview({ name, loading: false, result: null, error: errorMessage(err) })
      }
    },
    [api],
  )

  async function confirmDeleteIngest() {
    if (!deleteIngestTarget) return
    setConfirmBusy(true)
    setConfirmError(null)
    try {
      await api.deleteIngest(deleteIngestTarget.name)
      setDeleteIngestTarget(null)
      await loadIngests()
    } catch (err) {
      setConfirmError(errorMessage(err))
    } finally {
      setConfirmBusy(false)
    }
  }

  async function confirmDeleteSchedule() {
    if (!deleteScheduleTarget) return
    setConfirmBusy(true)
    setConfirmError(null)
    try {
      await api.deleteIngestSchedule(deleteScheduleTarget.id)
      setDeleteScheduleTarget(null)
      await loadSchedules()
    } catch (err) {
      setConfirmError(errorMessage(err))
    } finally {
      setConfirmBusy(false)
    }
  }

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      <div className="shrink-0">
        <PageHeader title="Ingest">
          <Button
            variant="outline"
            size="sm"
            onClick={() => setEditDialog({ open: true })}
          >
            <Plus className="h-3.5 w-3.5" />
            New ingest
          </Button>
          <Button
            variant="default"
            size="sm"
            onClick={() => void handleRunAll()}
            disabled={running}
          >
            {running ? (
              <RefreshCw className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Play className="h-3.5 w-3.5" />
            )}
            {running ? 'Queuing…' : 'Run all'}
          </Button>
        </PageHeader>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {(runNote || runError) && (
          <div className="px-4 py-2 text-[11px]">
            {runError ? (
              <span className="text-danger-soft">{runError}</span>
            ) : (
              <span className="text-text-muted">{runNote}</span>
            )}
          </div>
        )}

        {/* Sources */}
        <Section title="Ingest sources" count={loading ? undefined : ingests.length}>
          <div className="px-4 pb-3">
            {loading && (
              <div className="space-y-2">
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-7 w-5/6" />
              </div>
            )}

            {!loading && loadError && (
              <p className="text-[13px] text-danger-soft">{loadError}</p>
            )}

            {!loading && !loadError && ingests.length === 0 && (
              <p className="text-[13px] text-text-muted">No ingest sources configured.</p>
            )}

            {!loading && !loadError && ingests.length > 0 && (
              <div className="overflow-hidden rounded-md border border-border">
                <table className="w-full border-collapse">
                  <thead>
                    <tr className="border-b border-border-soft bg-panel-2/50">
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Name</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Kind</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Enabled</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Source root</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Namespace</th>
                      <th className={`px-4 py-2 text-right ${COLUMN_LABEL}`}>Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-soft">
                    {ingests.map((ingest) => {
                      const validation = validations[ingest.name]
                      return (
                        <tr key={ingest.name} className="hover:bg-panel-hover">
                          <td className="px-4 py-2 align-top text-[13px] font-medium text-text">
                            {ingest.name}
                          </td>
                          <td className="px-4 py-2 align-top text-[13px] text-text-muted">
                            {ingest.kind}
                          </td>
                          <td className="px-4 py-2 align-top">
                            <button
                              type="button"
                              onClick={() => void handleToggleEnabled(ingest)}
                              className={`text-[11px] font-semibold transition hover:underline ${
                                ingest.enabled ? 'text-status-indexed' : 'text-text-subtle'
                              }`}
                              title="Toggle enabled"
                            >
                              {ingest.enabled ? 'Enabled' : 'Disabled'}
                            </button>
                          </td>
                          <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                            {ingest.source_root}
                          </td>
                          <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                            {ingest.namespace}
                          </td>
                          <td className="px-4 py-2 align-top">
                            <div className="flex flex-col items-end gap-1.5">
                              <div className="flex flex-wrap justify-end gap-1.5">
                                <Button
                                  size="xs"
                                  variant="outline"
                                  onClick={() => void handleRunOne(ingest.name)}
                                >
                                  Run
                                </Button>
                                <Button
                                  size="xs"
                                  variant="outline"
                                  onClick={() => void handleValidate(ingest.name)}
                                  disabled={validation?.kind === 'loading'}
                                >
                                  Validate
                                </Button>
                                <Button
                                  size="xs"
                                  variant="outline"
                                  onClick={() => void handlePreview(ingest.name)}
                                >
                                  Preview
                                </Button>
                                <Button
                                  size="xs"
                                  variant="outline"
                                  onClick={() => setEditDialog({ open: true, ingest })}
                                >
                                  <Pencil className="h-3 w-3" />
                                </Button>
                                <Button
                                  size="xs"
                                  variant="destructive"
                                  onClick={() => {
                                    setConfirmError(null)
                                    setDeleteIngestTarget(ingest)
                                  }}
                                >
                                  <Trash2 className="h-3 w-3" />
                                </Button>
                              </div>
                              {validation && <ValidationResult state={validation} />}
                            </div>
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </Section>

        {/* Runs */}
        <Section
          title="Recent runs"
          count={runsLoading ? undefined : runs.length}
          action={
            <Button
              variant="outline"
              size="xs"
              onClick={() => void loadRuns()}
              disabled={runsLoading}
            >
              <RefreshCw className={`h-3 w-3 ${runsLoading ? 'animate-spin' : ''}`} />
              Refresh
            </Button>
          }
        >
          <div className="px-4 pb-3">
            {runsLoading ? (
              <div className="space-y-2">
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-7 w-5/6" />
              </div>
            ) : runs.length === 0 ? (
              <p className="text-[13px] text-text-muted">No runs yet.</p>
            ) : (
              <div className="overflow-hidden rounded-md border border-border">
                <table className="w-full border-collapse">
                  <thead>
                    <tr className="border-b border-border-soft bg-panel-2/50">
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Ingest</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Status</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Started</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Finished</th>
                      <th className={`px-4 py-2 text-right ${COLUMN_LABEL}`}>Ins/Upd/Skip</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Error</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-soft">
                    {runs.map((run) => (
                      <tr key={run.id} className="hover:bg-panel-hover">
                        <td className="px-4 py-2 align-top text-[13px] text-text">{run.name}</td>
                        <td
                          className={`px-4 py-2 align-top text-[11px] font-semibold uppercase tracking-[.1em] ${runStatusClass(
                            run.status,
                          )}`}
                        >
                          {run.status}
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                          {orDash(run.started_at)}
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                          {orDash(run.finished_at)}
                        </td>
                        <td className="px-4 py-2 text-right align-top font-mono text-[12px] text-text-muted">
                          {run.inserted}/{run.updated}/{run.skipped}
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-danger-soft">
                          {run.error ? truncate(run.error, 80) : ''}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </Section>

        {/* Schedules */}
        <Section
          title="Schedules"
          count={schedulesLoading ? undefined : schedules.length}
          action={
            <Button
              variant="outline"
              size="xs"
              onClick={() => setScheduleDialog({ open: true })}
              disabled={ingests.length === 0}
              title={ingests.length === 0 ? 'Create an ingest source first' : undefined}
            >
              <Plus className="h-3 w-3" />
              New schedule
            </Button>
          }
        >
          <div className="px-4 pb-3">
            {schedulesLoading ? (
              <div className="space-y-2">
                <Skeleton className="h-7 w-full" />
                <Skeleton className="h-7 w-5/6" />
              </div>
            ) : schedules.length === 0 ? (
              <p className="text-[13px] text-text-muted">No schedules configured.</p>
            ) : (
              <div className="overflow-hidden rounded-md border border-border">
                <table className="w-full border-collapse">
                  <thead>
                    <tr className="border-b border-border-soft bg-panel-2/50">
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Ingest</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Cron</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Enabled</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Last run</th>
                      <th className={`px-4 py-2 text-left ${COLUMN_LABEL}`}>Next run</th>
                      <th className={`px-4 py-2 text-right ${COLUMN_LABEL}`}>Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-soft">
                    {schedules.map((schedule) => (
                      <tr key={schedule.id} className="hover:bg-panel-hover">
                        <td className="px-4 py-2 align-top text-[13px] text-text">
                          {schedule.ingest_name}
                        </td>
                        <td className="px-4 py-2 align-top font-mono text-[12px] text-text-muted">
                          {schedule.cron_expr}
                        </td>
                        <td className="px-4 py-2 align-top">
                          <span
                            className={`text-[11px] font-semibold ${
                              schedule.enabled ? 'text-status-indexed' : 'text-text-subtle'
                            }`}
                          >
                            {schedule.enabled ? 'Enabled' : 'Disabled'}
                          </span>
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                          {orDash(schedule.last_run)}
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                          {orDash(schedule.next_run)}
                        </td>
                        <td className="px-4 py-2 align-top">
                          <div className="flex justify-end gap-1.5">
                            <Button
                              size="xs"
                              variant="outline"
                              onClick={() => setScheduleDialog({ open: true, schedule })}
                            >
                              <Pencil className="h-3 w-3" />
                            </Button>
                            <Button
                              size="xs"
                              variant="destructive"
                              onClick={() => {
                                setConfirmError(null)
                                setDeleteScheduleTarget(schedule)
                              }}
                            >
                              <Trash2 className="h-3 w-3" />
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </Section>
      </div>

      <Dialog
        open={preview !== null}
        onOpenChange={(open) => {
          if (!open) setPreview(null)
        }}
      >
        <DialogContent className="sm:max-w-lg">
          <DialogTitle className="text-base font-semibold text-text">
            Preview: {preview?.name ?? ''}
          </DialogTitle>
          {preview && <PreviewBody state={preview} />}
        </DialogContent>
      </Dialog>

      <IngestEditDialog
        open={editDialog.open}
        ingest={editDialog.ingest}
        onClose={() => setEditDialog({ open: false })}
        onSaved={() => void loadIngests()}
      />

      <IngestScheduleDialog
        open={scheduleDialog.open}
        schedule={scheduleDialog.schedule}
        ingests={ingests}
        onClose={() => setScheduleDialog({ open: false })}
        onSaved={() => void loadSchedules()}
      />

      <ConfirmDialog
        open={deleteIngestTarget !== null}
        title="Delete ingest source"
        body={`Delete "${deleteIngestTarget?.name ?? ''}"? This rewrites the ingest config.`}
        error={confirmError}
        busy={confirmBusy}
        onConfirm={() => void confirmDeleteIngest()}
        onClose={() => {
          setDeleteIngestTarget(null)
          setConfirmError(null)
        }}
      />

      <ConfirmDialog
        open={deleteScheduleTarget !== null}
        title="Delete schedule"
        body={`Delete the schedule for "${deleteScheduleTarget?.ingest_name ?? ''}"?`}
        error={confirmError}
        busy={confirmBusy}
        onConfirm={() => void confirmDeleteSchedule()}
        onClose={() => {
          setDeleteScheduleTarget(null)
          setConfirmError(null)
        }}
      />
    </div>
  )
}
