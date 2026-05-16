import { useEffect, useState } from 'react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/useApi'
import { ApiError, type RouteApplyResult } from '@/lib/api'
import type { Route } from '@/lib/types'

export interface EntityRef {
  kind: string
  value: string
}

interface ApplyRouteDialogProps {
  open: boolean
  onClose: () => void
  /**
   * Entity facets the route can be applied to. The backend routes by entity
   * (`apply-entity`), so applying touches every inbox fragment carrying the
   * chosen entity — not just one fragment.
   */
  entityOptions: EntityRef[]
  /** Called after a successful apply so callers can refresh their data. */
  onApplied?: () => void
}

const fieldClass =
  'h-8 w-full rounded-md border border-border bg-bg px-2 text-sm text-text outline-none transition focus:border-border-strong'

/** Applies a route to inbox fragments carrying a chosen entity. */
export function ApplyRouteDialog({ open, onClose, entityOptions, onApplied }: ApplyRouteDialogProps) {
  const api = useApi()
  const [routes, setRoutes] = useState<Route[]>([])
  const [routeId, setRouteId] = useState('')
  const [entityIdx, setEntityIdx] = useState(0)
  const [applying, setApplying] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<RouteApplyResult | null>(null)

  useEffect(() => {
    if (!open) return
    setError(null)
    setResult(null)
    setEntityIdx(0)
    void api
      .fetchRoutes()
      .then((r) => {
        setRoutes(r)
        setRouteId(r[0]?.id ?? '')
      })
      .catch((err: unknown) => {
        setRoutes([])
        setError(err instanceof Error ? err.message : 'Failed to load routes')
      })
  }, [open, api])

  const entity = entityOptions[entityIdx] ?? null

  async function handleApply() {
    if (!routeId || !entity) return
    setApplying(true)
    setError(null)
    try {
      const res = await api.applyRouteEntity({ routeId, kind: entity.kind, value: entity.value })
      setResult(res)
      onApplied?.()
    } catch (err) {
      setError(
        err instanceof ApiError || err instanceof Error ? err.message : 'Failed to apply route',
      )
    } finally {
      setApplying(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          Apply route
        </DialogTitle>

        {entityOptions.length === 0 ? (
          <p className="mt-3 text-sm text-text-soft">
            This fragment has no entities, so there's nothing to route by.
          </p>
        ) : (
          <div className="mt-3 flex flex-col gap-3">
            <label className="flex flex-col gap-1">
              <span className="text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle">
                Route
              </span>
              <select
                className={fieldClass}
                value={routeId}
                onChange={(e) => setRouteId(e.target.value)}
                disabled={routes.length === 0}
              >
                {routes.length === 0 && <option value="">No routes defined</option>}
                {routes.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name}
                  </option>
                ))}
              </select>
            </label>

            <label className="flex flex-col gap-1">
              <span className="text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle">
                Entity
              </span>
              <select
                className={fieldClass}
                value={entityIdx}
                onChange={(e) => setEntityIdx(Number(e.target.value))}
                disabled={entityOptions.length === 1}
              >
                {entityOptions.map((opt, i) => (
                  <option key={`${opt.kind}:${opt.value}`} value={i}>
                    {opt.kind}: {opt.value}
                  </option>
                ))}
              </select>
            </label>

            <p className="text-[11px] leading-5 text-text-subtle">
              Routes every staged fragment carrying{' '}
              <span className="text-text-soft">
                {entity ? `${entity.kind}: ${entity.value}` : '—'}
              </span>
              .
            </p>

            {error && <p className="text-sm text-danger-soft">{error}</p>}

            {result && (
              <div className="rounded-md border border-border bg-bg p-2 text-[12px] text-text-muted">
                Matched {result.matched_count} · routed {result.routed_count} · failed{' '}
                {result.failed_count}
              </div>
            )}

            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={onClose}>
                {result ? 'Close' : 'Cancel'}
              </Button>
              <Button
                size="sm"
                onClick={handleApply}
                disabled={applying || !routeId || !entity}
              >
                {applying ? 'Applying…' : 'Apply route'}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
