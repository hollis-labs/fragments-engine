import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, Play, RefreshCw, XCircle } from 'lucide-react'
import { PageHeader } from '@/components/domain/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { IngestSummary } from '@/lib/api'

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

function summarizeRunReport(report: unknown): string {
  if (typeof report === 'string') return truncate(report, 80)
  if (isObject(report)) {
    const msg = report.message ?? report.summary ?? report.status
    if (typeof msg === 'string') return truncate(msg, 80)
    return truncate(JSON.stringify(report), 80)
  }
  return truncate(JSON.stringify(report ?? 'done'), 80)
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

export default function IngestPage() {
  const api = useApi()

  const [ingests, setIngests] = useState<IngestSummary[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)

  const [running, setRunning] = useState(false)
  const [runSummary, setRunSummary] = useState<string | null>(null)
  const [runError, setRunError] = useState<string | null>(null)

  const [validations, setValidations] = useState<Record<string, ValidationState>>({})
  const [preview, setPreview] = useState<PreviewState | null>(null)

  const loadIngests = useCallback(async () => {
    setLoading(true)
    setLoadError(null)
    try {
      const list = await api.fetchIngests()
      setIngests(list)
    } catch (err) {
      setLoadError(errorMessage(err))
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => {
    void loadIngests()
  }, [loadIngests])

  const handleRunAll = useCallback(async () => {
    setRunning(true)
    setRunError(null)
    try {
      const report = await api.runIngests()
      setRunSummary(summarizeRunReport(report))
      await loadIngests()
    } catch (err) {
      setRunError(errorMessage(err))
    } finally {
      setRunning(false)
    }
  }, [api, loadIngests])

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

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      <div className="shrink-0">
        <PageHeader title="Ingest">
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
            {running ? 'Running…' : 'Run all'}
          </Button>
        </PageHeader>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <div className="space-y-3 px-4 py-2">
          {(runSummary || runError) && (
            <div className="text-[11px]">
              {runError ? (
                <span className="text-danger-soft">{runError}</span>
              ) : (
                <span className="text-text-muted">Last run: {runSummary}</span>
              )}
            </div>
          )}

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
                          <span
                            className={`text-[11px] font-semibold ${
                              ingest.enabled ? 'text-status-indexed' : 'text-text-subtle'
                            }`}
                          >
                            {ingest.enabled ? 'Enabled' : 'Disabled'}
                          </span>
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                          {ingest.source_root}
                        </td>
                        <td className="px-4 py-2 align-top text-[12px] text-text-muted">
                          {ingest.namespace}
                        </td>
                        <td className="px-4 py-2 align-top">
                          <div className="flex flex-col items-end gap-1.5">
                            <div className="flex gap-1.5">
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
    </div>
  )
}
