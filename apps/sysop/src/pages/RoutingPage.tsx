import { useCallback, useEffect, useState } from 'react'
import { Download, Eye, Play, Plus, RefreshCw } from 'lucide-react'
import { ListPageLayout, PageHeader, Button, Skeleton } from '@hollis-labs/sysop-ui'
import { DestinationCreateDialog, RouteCreateDialog } from '@/components/domain/routing-dialogs'
import { useApi } from '@/hooks/useApi'
import type { QueueStats, RouteMaterializeResult, RoutePreviewResult } from '@/lib/api'
import type { Destination, Route } from '@/lib/types'

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

function describeMatch(route: Route): string {
  const parts: string[] = []
  if (route.match_source) parts.push(`source=${route.match_source}`)
  if (route.match_type) parts.push(`type=${route.match_type}`)
  if (route.match_entity_kind) {
    parts.push(`entity ${route.match_entity_kind}:${route.match_entity_value || '*'}`)
  }
  return parts.length > 0 ? parts.join(' · ') : 'any fragment'
}

export default function RoutingPage() {
  const api = useApi()
  const [destinations, setDestinations] = useState<Destination[]>([])
  const [routes, setRoutes] = useState<Route[]>([])
  const [queue, setQueue] = useState<QueueStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [draining, setDraining] = useState(false)
  const [destCreateOpen, setDestCreateOpen] = useState(false)
  const [routeCreateOpen, setRouteCreateOpen] = useState(false)
  const [routeActionBusy, setRouteActionBusy] = useState<string | null>(null)
  const [routePreview, setRoutePreview] = useState<RoutePreviewResult | null>(null)
  const [routeMaterialize, setRouteMaterialize] = useState<RouteMaterializeResult | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      const [d, r, q] = await Promise.all([
        api.fetchDestinations(),
        api.fetchRoutes(),
        api.fetchQueueStatus().catch(() => null),
      ])
      setDestinations(d)
      setRoutes(r)
      setQueue(q)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load routing data')
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => {
    void load()
  }, [load])

  async function handleDrain() {
    setDraining(true)
    try {
      setQueue(await api.drainQueue({}))
    } catch {
      // surfaced on next load; keep the button responsive
    } finally {
      setDraining(false)
    }
  }

  async function handlePreviewRoute(routeId: string) {
    setRouteActionBusy(`preview:${routeId}`)
    try {
      setRouteMaterialize(null)
      setRoutePreview(await api.fetchRoutePreview({ routeId }))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to preview route')
    } finally {
      setRouteActionBusy(null)
    }
  }

  async function handleMaterializeRoute(routeId: string) {
    setRouteActionBusy(`materialize:${routeId}`)
    try {
      setRoutePreview(null)
      setRouteMaterialize(await api.materializeRoute({ routeId, limit: 100 }))
      await load()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to materialize route')
    } finally {
      setRouteActionBusy(null)
    }
  }

  const destName = (id: string) => destinations.find((d) => d.id === id)?.name ?? id

  return (
    <ListPageLayout
      header={
        <PageHeader title="Routing">
          <Button variant="outline" size="sm" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </PageHeader>
      }
    >
      <>
        {loading ? (
          <div className="flex flex-col gap-2 p-4">
            {Array.from({ length: 6 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full rounded-md" />
            ))}
          </div>
        ) : error ? (
          <p className="p-4 text-sm text-danger-soft">{error}</p>
        ) : (
          <>
            {/* Delivery queue */}
            <Section title="Delivery queue">
              <div className="flex flex-wrap items-center gap-4 px-4 pb-3 text-[13px]">
                <span className="flex items-center gap-2">
                  <span className="size-1.5 rounded-full bg-status-inbox" />
                  <span className="uppercase tracking-[.14em] text-text-subtle">Pending</span>
                  <span className="font-mono font-semibold text-text">{queue?.pending ?? '—'}</span>
                </span>
                <span className="flex items-center gap-2">
                  <span className="size-1.5 rounded-full bg-status-blocked" />
                  <span className="uppercase tracking-[.14em] text-text-subtle">Failed</span>
                  <span className="font-mono font-semibold text-text">{queue?.failed ?? '—'}</span>
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  className="ml-auto"
                  onClick={handleDrain}
                  disabled={draining}
                >
                  <Play className={`h-3.5 w-3.5 ${draining ? 'animate-pulse' : ''}`} />
                  {draining ? 'Draining…' : 'Drain queue'}
                </Button>
              </div>
            </Section>

            {/* Destinations */}
            <Section
              title="Destinations"
              count={destinations.length}
              action={
                <Button variant="outline" size="xs" onClick={() => setDestCreateOpen(true)}>
                  <Plus className="h-3 w-3" />
                  New
                </Button>
              }
            >
              {destinations.length === 0 ? (
                <p className="px-4 pb-3 text-[13px] text-text-subtle">
                  No destinations. Add one with{' '}
                  <code className="text-text-soft">fragments-engine route destination-add</code>.
                </p>
              ) : (
                <table className="w-full text-[13px]">
                  <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
                    <tr className="border-b border-border-soft">
                      <th className="px-4 py-1.5 text-left font-medium">Name</th>
                      <th className="px-4 py-1.5 text-left font-medium">Kind</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-soft">
                    {destinations.map((d) => (
                      <tr key={d.id}>
                        <td className="px-4 py-1.5 text-text">{d.name}</td>
                        <td className="px-4 py-1.5 uppercase tracking-[.12em] text-text-soft">
                          {d.kind}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </Section>

            {/* Routes */}
            <Section
              title="Routes"
              count={routes.length}
              action={
                <Button
                  variant="outline"
                  size="xs"
                  onClick={() => setRouteCreateOpen(true)}
                  disabled={destinations.length === 0}
                  title={destinations.length === 0 ? 'Create a destination first' : undefined}
                >
                  <Plus className="h-3 w-3" />
                  New
                </Button>
              }
            >
              {routes.length === 0 ? (
                <p className="px-4 pb-3 text-[13px] text-text-subtle">
                  No routes. Add one with{' '}
                  <code className="text-text-soft">fragments-engine route add</code>.
                </p>
              ) : (
                <table className="w-full text-[13px]">
                  <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
                    <tr className="border-b border-border-soft">
                      <th className="px-4 py-1.5 text-left font-medium">Name</th>
                      <th className="px-4 py-1.5 text-left font-medium">Match</th>
                      <th className="px-4 py-1.5 text-left font-medium">Destination</th>
                      <th className="px-4 py-1.5 text-left font-medium">Auto</th>
                      <th className="px-4 py-1.5 text-left font-medium">Actions</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-soft">
                    {routes.map((r) => (
                      <tr key={r.id}>
                        <td className="px-4 py-1.5 text-text">{r.name}</td>
                        <td className="px-4 py-1.5 text-text-soft">{describeMatch(r)}</td>
                        <td className="px-4 py-1.5 text-text-soft">{destName(r.destination_id)}</td>
                        <td className="px-4 py-1.5">
                          <span
                            className={`text-[11px] uppercase tracking-[.12em] ${
                              r.auto_route ? 'text-status-routed' : 'text-text-subtle'
                            }`}
                          >
                            {r.auto_route ? 'auto' : 'manual'}
                          </span>
                        </td>
                        <td className="px-4 py-1.5">
                          <div className="flex items-center gap-2">
                            <Button
                              variant="outline"
                              size="xs"
                              onClick={() => void handlePreviewRoute(r.id)}
                              disabled={routeActionBusy !== null}
                            >
                              <Eye className="h-3 w-3" />
                              Preview
                            </Button>
                            <Button
                              variant="outline"
                              size="xs"
                              onClick={() => void handleMaterializeRoute(r.id)}
                              disabled={routeActionBusy !== null}
                            >
                              <Download className="h-3 w-3" />
                              Materialize
                            </Button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {(routePreview || routeMaterialize) && (
                <div className="border-t border-border-soft px-4 py-3 text-[12px] text-text-soft">
                  {routePreview && (
                    <div>
                      Preview for <span className="font-mono">{routePreview.route_id}</span>: matched{' '}
                      {routePreview.matched_count}
                    </div>
                  )}
                  {routeMaterialize && (
                    <div>
                      Materialized for <span className="font-mono">{routeMaterialize.route_id}</span>:
                      {' '}matched {routeMaterialize.matched_count} · saved{' '}
                      {routeMaterialize.materialized_count} · failed {routeMaterialize.failed_count}
                    </div>
                  )}
                </div>
              )}
            </Section>
          </>
        )}

        <DestinationCreateDialog
          open={destCreateOpen}
          onClose={() => setDestCreateOpen(false)}
          onCreated={() => void load()}
        />
        <RouteCreateDialog
          open={routeCreateOpen}
          onClose={() => setRouteCreateOpen(false)}
          destinations={destinations}
          onCreated={() => void load()}
        />
      </>
    </ListPageLayout>
  )
}
