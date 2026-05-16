import { useEffect, useState } from 'react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { Destination } from '@/lib/types'

const FIELD =
  'h-8 w-full rounded-md border border-border bg-bg px-2 text-sm text-text outline-none transition focus:border-border-strong'
const LABEL = 'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'

const DESTINATION_KINDS = ['file', 'mcp', 'api', 'cli'] as const

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className={LABEL}>{label}</span>
      {children}
    </label>
  )
}

function errMessage(err: unknown): string {
  return err instanceof ApiError || err instanceof Error ? err.message : 'Request failed'
}

/* ─────────────────────────── Destination create ─────────────────────────── */

interface DestinationCreateDialogProps {
  open: boolean
  onClose: () => void
  onCreated: () => void
}

export function DestinationCreateDialog({ open, onClose, onCreated }: DestinationCreateDialogProps) {
  const api = useApi()
  const [name, setName] = useState('')
  const [kind, setKind] = useState<string>('file')
  const [root, setRoot] = useState('')
  const [configJson, setConfigJson] = useState('{}')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName('')
    setKind('file')
    setRoot('')
    setConfigJson('{}')
    setSubmitting(false)
    setError(null)
  }, [open])

  async function handleSubmit() {
    if (!name.trim()) {
      setError('Name is required.')
      return
    }
    let payload: string
    if (kind === 'file') {
      if (!root.trim()) {
        setError('Root directory is required for a file destination.')
        return
      }
      payload = JSON.stringify({ root: root.trim() })
    } else {
      try {
        JSON.parse(configJson || '{}')
      } catch {
        setError('Config must be valid JSON.')
        return
      }
      payload = configJson || '{}'
    }
    setSubmitting(true)
    setError(null)
    try {
      await api.createDestination({ name: name.trim(), kind, configJson: payload })
      onCreated()
      onClose()
    } catch (err) {
      setError(errMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          New destination
        </DialogTitle>
        <div className="mt-3 flex flex-col gap-3">
          <Field label="Name">
            <input
              className={FIELD}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="local-files"
            />
          </Field>
          <Field label="Kind">
            <select className={FIELD} value={kind} onChange={(e) => setKind(e.target.value)}>
              {DESTINATION_KINDS.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </select>
          </Field>
          {kind === 'file' ? (
            <Field label="Root directory">
              <input
                className={FIELD}
                value={root}
                onChange={(e) => setRoot(e.target.value)}
                placeholder="/Users/you/fragments-out"
              />
            </Field>
          ) : (
            <Field label="Config (JSON)">
              <textarea
                className="min-h-24 w-full rounded-md border border-border bg-bg px-2 py-1.5 font-mono text-[12px] text-text outline-none transition focus:border-border-strong"
                value={configJson}
                onChange={(e) => setConfigJson(e.target.value)}
                spellCheck={false}
              />
            </Field>
          )}
          {error && <p className="text-sm text-danger-soft">{error}</p>}
          <div className="flex justify-end gap-2">
            <Button variant="ghost" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button size="sm" onClick={handleSubmit} disabled={submitting}>
              {submitting ? 'Creating…' : 'Create destination'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

/* ────────────────────────────── Route create ────────────────────────────── */

interface RouteCreateDialogProps {
  open: boolean
  onClose: () => void
  destinations: Destination[]
  onCreated: () => void
}

export function RouteCreateDialog({
  open,
  onClose,
  destinations,
  onCreated,
}: RouteCreateDialogProps) {
  const api = useApi()
  const [name, setName] = useState('')
  const [destinationId, setDestinationId] = useState('')
  const [matchSource, setMatchSource] = useState('')
  const [matchType, setMatchType] = useState('')
  const [matchEntityKind, setMatchEntityKind] = useState('')
  const [matchEntityValue, setMatchEntityValue] = useState('')
  const [autoRoute, setAutoRoute] = useState(false)
  const [confidenceMin, setConfidenceMin] = useState('0')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setName('')
    setDestinationId(destinations[0]?.id ?? '')
    setMatchSource('')
    setMatchType('')
    setMatchEntityKind('')
    setMatchEntityValue('')
    setAutoRoute(false)
    setConfidenceMin('0')
    setSubmitting(false)
    setError(null)
  }, [open, destinations])

  async function handleSubmit() {
    if (!name.trim()) {
      setError('Name is required.')
      return
    }
    if (!destinationId) {
      setError('Pick a destination.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      await api.createRoute({
        name: name.trim(),
        destinationId,
        matchSource: matchSource.trim(),
        matchType: matchType.trim(),
        matchEntityKind: matchEntityKind.trim(),
        matchEntityValue: matchEntityValue.trim(),
        autoRoute,
        confidenceMin: Number(confidenceMin) || 0,
      })
      onCreated()
      onClose()
    } catch (err) {
      setError(errMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          New route
        </DialogTitle>
        {destinations.length === 0 ? (
          <p className="mt-3 text-sm text-text-soft">
            Create a destination first — a route needs somewhere to deliver to.
          </p>
        ) : (
          <div className="mt-3 flex flex-col gap-3">
            <Field label="Name">
              <input
                className={FIELD}
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="claude-chats → local"
              />
            </Field>
            <Field label="Destination">
              <select
                className={FIELD}
                value={destinationId}
                onChange={(e) => setDestinationId(e.target.value)}
              >
                {destinations.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name} ({d.kind})
                  </option>
                ))}
              </select>
            </Field>
            <div className="grid grid-cols-2 gap-2">
              <Field label="Match source">
                <input
                  className={FIELD}
                  value={matchSource}
                  onChange={(e) => setMatchSource(e.target.value)}
                  placeholder="claude"
                />
              </Field>
              <Field label="Match type">
                <input
                  className={FIELD}
                  value={matchType}
                  onChange={(e) => setMatchType(e.target.value)}
                  placeholder="chat"
                />
              </Field>
              <Field label="Entity kind">
                <input
                  className={FIELD}
                  value={matchEntityKind}
                  onChange={(e) => setMatchEntityKind(e.target.value)}
                  placeholder="project"
                />
              </Field>
              <Field label="Entity value">
                <input
                  className={FIELD}
                  value={matchEntityValue}
                  onChange={(e) => setMatchEntityValue(e.target.value)}
                  placeholder="sysop"
                />
              </Field>
            </div>
            <div className="flex items-center gap-4">
              <label className="flex items-center gap-2 text-sm text-text-soft">
                <input
                  type="checkbox"
                  checked={autoRoute}
                  onChange={(e) => setAutoRoute(e.target.checked)}
                  className="h-3.5 w-3.5"
                />
                Auto-route
              </label>
              <label className="flex items-center gap-2 text-sm text-text-soft">
                Min confidence
                <input
                  type="number"
                  min={0}
                  max={1}
                  step={0.05}
                  className={`${FIELD} w-20`}
                  value={confidenceMin}
                  onChange={(e) => setConfidenceMin(e.target.value)}
                />
              </label>
            </div>
            <p className="text-[11px] leading-5 text-text-subtle">
              Leave match fields blank to match any fragment. All filled fields must match.
            </p>
            {error && <p className="text-sm text-danger-soft">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={onClose}>
                Cancel
              </Button>
              <Button size="sm" onClick={handleSubmit} disabled={submitting}>
                {submitting ? 'Creating…' : 'Create route'}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
