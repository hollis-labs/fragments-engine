import { useCallback, useEffect, useMemo, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import {
  ListPageLayout,
  PageHeader,
  Button,
  Skeleton,
  Dialog,
  DialogContent,
  DialogTitle,
  StatusBadge,
} from '@hollis-labs/sysop-ui'
import { FragmentDetailDialog } from '@/components/domain/fragment-detail-dialog'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { EntityRecord } from '@/lib/api'
import type { SearchResult } from '@/lib/types'

const LABEL = 'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'

function TableSkeleton() {
  return (
    <div className="flex flex-col gap-2 p-4">
      {Array.from({ length: 8 }).map((_, i) => (
        <Skeleton key={i} className="h-9 w-full rounded-md" />
      ))}
    </div>
  )
}

interface SelectedEntity {
  kind: string
  value: string
}

function EntityFragmentsDialog({
  entity,
  onClose,
  onOpenFragment,
}: {
  entity: SelectedEntity | null
  onClose: () => void
  onOpenFragment: (fragmentId: string) => void
}) {
  const api = useApi()
  const [results, setResults] = useState<SearchResult[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!entity) return
    let cancelled = false
    setLoading(true)
    setError(null)
    setResults([])
    api
      .fetchEntityFragments({ kind: entity.kind, value: entity.value })
      .then((items) => {
        if (!cancelled) setResults(items)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(
          err instanceof ApiError ? err.message : 'Failed to load fragments.',
        )
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [api, entity])

  return (
    <Dialog
      open={entity != null}
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-lg overflow-hidden p-0">
        {entity ? (
          <div className="flex max-h-[70vh] flex-col">
            <div className="shrink-0 border-b border-border-soft px-4 py-3">
              <DialogTitle className="text-[13px] font-medium text-text">
                <span className="uppercase tracking-wider text-text-subtle">
                  {entity.kind}
                </span>
                <span className="text-text-soft">: </span>
                {entity.value}
              </DialogTitle>
            </div>
            <div className="min-h-0 flex-1 overflow-auto">
              {loading ? (
                <div className="flex flex-col gap-2 p-4">
                  {Array.from({ length: 4 }).map((_, i) => (
                    <Skeleton key={i} className="h-12 w-full rounded-md" />
                  ))}
                </div>
              ) : error ? (
                <div className="p-4 text-[13px] text-danger-soft">{error}</div>
              ) : results.length === 0 ? (
                <div className="p-4 text-[13px] text-text-muted">
                  No fragments carry this entity.
                </div>
              ) : (
                <ul className="divide-y divide-border-soft">
                  {results.map(({ fragment }) => (
                    <li key={fragment.id}>
                      <button
                        type="button"
                        onClick={() => onOpenFragment(fragment.id)}
                        className="flex w-full flex-col gap-1 px-4 py-2.5 text-left hover:bg-panel-hover/60"
                      >
                        <div className="flex items-center gap-2">
                          <span className="min-w-0 flex-1 truncate text-[13px] text-text">
                            {fragment.title}
                          </span>
                          <StatusBadge status={fragment.status} />
                        </div>
                        <span className="text-[11px] text-text-subtle">
                          {fragment.source} &middot; {fragment.source_type}
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

export default function EntitiesPage() {
  const api = useApi()
  const [entities, setEntities] = useState<EntityRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [activeKind, setActiveKind] = useState<string | null>(null)
  const [selectedEntity, setSelectedEntity] = useState<SelectedEntity | null>(
    null,
  )
  const [openFragmentId, setOpenFragmentId] = useState<string | null>(null)

  const load = useCallback(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    api
      .fetchEntities({ limit: 200 })
      .then((items) => {
        if (!cancelled) setEntities(items)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(
          err instanceof ApiError ? err.message : 'Failed to load entities.',
        )
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [api])

  useEffect(() => load(), [load])

  const kinds = useMemo(() => {
    const set = new Set<string>()
    for (const entity of entities) set.add(entity.kind)
    return Array.from(set).sort((a, b) => a.localeCompare(b))
  }, [entities])

  const visibleEntities = useMemo(() => {
    if (!activeKind) return entities
    return entities.filter((entity) => entity.kind === activeKind)
  }, [entities, activeKind])

  return (
    <ListPageLayout
      header={
        <PageHeader title="Entities">
          <Button
            variant="ghost"
            size="sm"
            onClick={load}
            disabled={loading}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            Refresh
          </Button>
        </PageHeader>
      }
    >
      <>
        {loading ? (
          <TableSkeleton />
        ) : error ? (
          <div className="p-4 text-[13px] text-danger-soft">{error}</div>
        ) : entities.length === 0 ? (
          <div className="p-4 text-[13px] text-text-muted">
            No entities extracted yet.
          </div>
        ) : (
          <div className="flex flex-col">
            <div className="flex flex-wrap items-center gap-1.5 border-b border-border-soft px-4 py-3">
              <button
                type="button"
                onClick={() => setActiveKind(null)}
                className={`rounded border px-2 py-0.5 text-[10px] uppercase tracking-wider ${
                  activeKind === null
                    ? 'border-status-routed bg-status-routed/10 text-text'
                    : 'border-border bg-panel-2/50 text-text-subtle hover:text-text-soft'
                }`}
              >
                All
              </button>
              {kinds.map((kind) => (
                <button
                  key={kind}
                  type="button"
                  onClick={() => setActiveKind(kind)}
                  className={`rounded border px-2 py-0.5 text-[10px] uppercase tracking-wider ${
                    activeKind === kind
                      ? 'border-status-routed bg-status-routed/10 text-text'
                      : 'border-border bg-panel-2/50 text-text-subtle hover:text-text-soft'
                  }`}
                >
                  {kind}
                </button>
              ))}
            </div>
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-border-soft">
                  <th className={`px-4 py-2 text-left ${LABEL}`}>Kind</th>
                  <th className={`px-4 py-2 text-left ${LABEL}`}>Value</th>
                  <th className={`px-4 py-2 text-right ${LABEL}`}>Fragments</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-soft">
                {visibleEntities.map((entity) => {
                  const open = () =>
                    setSelectedEntity({
                      kind: entity.kind,
                      value: entity.value,
                    })
                  return (
                    <tr
                      key={entity.id}
                      role="button"
                      tabIndex={0}
                      onClick={open}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' || e.key === ' ') {
                          e.preventDefault()
                          open()
                        }
                      }}
                      className="cursor-pointer hover:bg-panel-hover/60"
                    >
                      <td className="px-4 py-2 text-[11px] uppercase tracking-wider text-text-subtle">
                        {entity.kind}
                      </td>
                      <td className="px-4 py-2 text-[13px] text-text">
                        {entity.value}
                      </td>
                      <td className="px-4 py-2 text-right font-mono text-[13px] text-text-soft">
                        {entity.fragment_count}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}

        <EntityFragmentsDialog
          entity={selectedEntity}
          onClose={() => setSelectedEntity(null)}
          onOpenFragment={setOpenFragmentId}
        />

        <FragmentDetailDialog
          fragmentId={openFragmentId}
          onClose={() => setOpenFragmentId(null)}
        />
      </>
    </ListPageLayout>
  )
}
