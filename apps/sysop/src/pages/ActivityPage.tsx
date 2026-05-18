import { useCallback, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { RefreshCw } from 'lucide-react'
import { ListPageLayout, PageHeader, Button, Skeleton } from '@hollis-labs/sysop-ui'
import { ConfirmDialog } from '@/components/domain/ingest-dialogs'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type {
  IngestJobRecord,
  IngestJobs,
  QueueEvent,
  QueueFailedItem,
  QueuePendingItem,
  WorkersStatus,
} from '@/lib/api'

function errorMessage(err: unknown): string {
  if (err instanceof ApiError || err instanceof Error) return err.message
  return String(err)
}

function orDash(value: string | undefined): string {
  return value && value.trim() !== '' ? value : '—'
}

/** Renders a long id truncated to 12 chars with a full-value tooltip. */
function ShortId({ id }: { id?: string }) {
  if (!id || id.trim() === '') return <span className="text-text-subtle">—</span>
  return (
    <span title={id} className="font-mono text-[11px] text-text-muted">
      {id.length > 12 ? `${id.slice(0, 12)}…` : id}
    </span>
  )
}

/** Two-line loading placeholder used by every section while data loads. */
function SkeletonRows() {
  return (
    <div className="space-y-2 px-4 pb-3">
      <Skeleton className="h-7 w-full" />
      <Skeleton className="h-7 w-5/6" />
    </div>
  )
}

/** Bordered section with an uppercase label header and a content body. */
function Section({
  title,
  count,
  children,
}: {
  title: string
  count?: number
  children: ReactNode
}) {
  return (
    <section className="border-b border-border-strong">
      <div className="flex items-center justify-between px-4 py-2">
        <p className="text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle">
          {title}
          {count !== undefined && <span className="ml-2 text-text-subtle/70">{count}</span>}
        </p>
      </div>
      {children}
    </section>
  )
}

/** Maps a job status to a text colour class. */
function jobStatusClass(status: string): string {
  switch (status) {
    case 'done':
    case 'completed':
      return 'text-status-indexed'
    case 'failed':
      return 'text-danger-soft'
    case 'running':
    case 'processing':
      return 'text-status-routed'
    default:
      return 'text-text-subtle'
  }
}

function JobsTable({ jobs, showError }: { jobs: IngestJobRecord[]; showError: boolean }) {
  return (
    <table className="w-full text-[13px]">
      <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
        <tr className="border-b border-border-soft">
          <th className="px-4 py-1.5 text-left font-medium">Ingest</th>
          <th className="px-4 py-1.5 text-left font-medium">Status</th>
          <th className="px-4 py-1.5 text-right font-medium">Attempts</th>
          <th className="px-4 py-1.5 text-left font-medium">Enqueued</th>
          {showError && <th className="px-4 py-1.5 text-left font-medium">Last error</th>}
        </tr>
      </thead>
      <tbody className="divide-y divide-border-soft">
        {jobs.map((job) => (
          <tr key={job.id} className="hover:bg-panel-hover">
            <td className="px-4 py-1.5 align-top text-text">{job.ingest_name}</td>
            <td
              className={`px-4 py-1.5 align-top text-[11px] font-semibold uppercase tracking-[.1em] ${jobStatusClass(
                job.status,
              )}`}
            >
              {job.status}
            </td>
            <td className="px-4 py-1.5 text-right align-top font-mono text-[12px] text-text-muted">
              {job.attempts}/{job.max_attempts}
            </td>
            <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
              {orDash(job.enqueued_at)}
            </td>
            {showError && (
              <td className="px-4 py-1.5 align-top text-[12px] text-danger-soft">
                {job.last_error ?? ''}
              </td>
            )}
          </tr>
        ))}
      </tbody>
    </table>
  )
}

function DeliveryPendingTable({ items }: { items: QueuePendingItem[] }) {
  return (
    <table className="w-full text-[13px]">
      <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
        <tr className="border-b border-border-soft">
          <th className="px-4 py-1.5 text-left font-medium">Type</th>
          <th className="px-4 py-1.5 text-left font-medium">Fragment</th>
          <th className="px-4 py-1.5 text-left font-medium">Destination</th>
          <th className="px-4 py-1.5 text-right font-medium">Attempts</th>
          <th className="px-4 py-1.5 text-left font-medium">Available</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border-soft">
        {items.map((item) => (
          <tr key={item.id} className="hover:bg-panel-hover">
            <td className="px-4 py-1.5 align-top text-text">{item.type}</td>
            <td className="px-4 py-1.5 align-top">
              <ShortId id={item.fragment_id} />
            </td>
            <td className="px-4 py-1.5 align-top">
              <ShortId id={item.destination_id} />
            </td>
            <td className="px-4 py-1.5 text-right align-top font-mono text-[12px] text-text-muted">
              {item.attempts}/{item.max_tries}
            </td>
            <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
              {orDash(item.available_at)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

interface DeliveryFailedTableProps {
  items: QueueFailedItem[]
  /** Id of the job whose replay/purge action is in flight, if any. */
  busyJobId: number | null
  onReplay: (id: number) => void
  onPurge: (id: number) => void
}

function DeliveryFailedTable({ items, busyJobId, onReplay, onPurge }: DeliveryFailedTableProps) {
  return (
    <table className="w-full text-[13px]">
      <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
        <tr className="border-b border-border-soft">
          <th className="px-4 py-1.5 text-left font-medium">Type</th>
          <th className="px-4 py-1.5 text-left font-medium">Fragment</th>
          <th className="px-4 py-1.5 text-left font-medium">Destination</th>
          <th className="px-4 py-1.5 text-right font-medium">Attempts</th>
          <th className="px-4 py-1.5 text-left font-medium">Failed</th>
          <th className="px-4 py-1.5 text-left font-medium">Error</th>
          <th className="px-4 py-1.5 text-right font-medium">Actions</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border-soft">
        {items.map((item) => {
          const busy = busyJobId === item.id
          return (
            <tr key={item.id} className="hover:bg-panel-hover">
              <td className="px-4 py-1.5 align-top text-text">{item.type}</td>
              <td className="px-4 py-1.5 align-top">
                <ShortId id={item.fragment_id} />
              </td>
              <td className="px-4 py-1.5 align-top">
                <ShortId id={item.destination_id} />
              </td>
              <td className="px-4 py-1.5 text-right align-top font-mono text-[12px] text-text-muted">
                {item.attempts}
              </td>
              <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
                {orDash(item.failed_at)}
              </td>
              <td className="px-4 py-1.5 align-top text-[12px] text-danger-soft">{item.error}</td>
              <td className="px-4 py-1.5 align-top">
                <div className="flex justify-end gap-1.5">
                  <Button
                    size="xs"
                    variant="outline"
                    disabled={busy}
                    onClick={() => onReplay(item.id)}
                  >
                    Replay
                  </Button>
                  <Button
                    size="xs"
                    variant="destructive"
                    disabled={busy}
                    onClick={() => onPurge(item.id)}
                  >
                    Purge
                  </Button>
                </div>
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

function EventsTable({ items }: { items: QueueEvent[] }) {
  return (
    <table className="w-full text-[13px]">
      <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
        <tr className="border-b border-border-soft">
          <th className="px-4 py-1.5 text-left font-medium">Event</th>
          <th className="px-4 py-1.5 text-left font-medium">Fragment</th>
          <th className="px-4 py-1.5 text-left font-medium">Destination</th>
          <th className="px-4 py-1.5 text-left font-medium">When</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-border-soft">
        {items.map((event) => (
          <tr key={event.id} className="hover:bg-panel-hover">
            <td className="px-4 py-1.5 align-top text-[11px] font-semibold uppercase tracking-[.1em] text-text-soft">
              {event.event_type}
            </td>
            <td className="px-4 py-1.5 align-top">
              <ShortId id={event.fragment_id} />
            </td>
            <td className="px-4 py-1.5 align-top">
              <ShortId id={event.destination_id} />
            </td>
            <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
              {orDash(event.created_at)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export default function ActivityPage() {
  const api = useApi()

  const [jobs, setJobs] = useState<IngestJobs | null>(null)
  const [workers, setWorkers] = useState<WorkersStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [jobsError, setJobsError] = useState<string | null>(null)
  const [workersError, setWorkersError] = useState<string | null>(null)

  // Delivery queue deep-dive.
  const [pendingDeliveries, setPendingDeliveries] = useState<QueuePendingItem[] | null>(null)
  const [failedDeliveries, setFailedDeliveries] = useState<QueueFailedItem[] | null>(null)
  const [queueEvents, setQueueEvents] = useState<QueueEvent[] | null>(null)
  const [queueError, setQueueError] = useState<string | null>(null)
  // Id of the failed delivery job whose replay/purge is in flight.
  const [busyJobId, setBusyJobId] = useState<number | null>(null)
  // Failed delivery job pending purge confirmation.
  const [purgeTarget, setPurgeTarget] = useState<number | null>(null)
  const [purgeError, setPurgeError] = useState<string | null>(null)

  // Fetches every panel. The loading flag is owned by the caller so the
  // initial effect run never triggers a synchronous setState.
  const load = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const [jobsResult, workersResult, pendingResult, failedResult, eventsResult] =
          await Promise.allSettled([
            api.fetchIngestJobs({ signal }),
            api.fetchWorkersStatus({ signal }),
            api.fetchPendingQueue(),
            api.fetchFailedQueue(),
            api.fetchQueueEvents(),
          ])
        // Bail out if the component unmounted while requests were in flight.
        if (signal?.aborted) return

        if (jobsResult.status === 'fulfilled') {
          setJobs(jobsResult.value)
          setJobsError(null)
        } else {
          setJobs(null)
          setJobsError(errorMessage(jobsResult.reason))
        }
        if (workersResult.status === 'fulfilled') {
          setWorkers(workersResult.value)
          setWorkersError(null)
        } else {
          setWorkers(null)
          setWorkersError(errorMessage(workersResult.reason))
        }

        // The three delivery-queue panels share one backend, so a single
        // error line covers whichever of them failed.
        let qErr: string | null = null
        if (pendingResult.status === 'fulfilled') {
          setPendingDeliveries(pendingResult.value)
        } else {
          setPendingDeliveries(null)
          qErr = errorMessage(pendingResult.reason)
        }
        if (failedResult.status === 'fulfilled') {
          setFailedDeliveries(failedResult.value)
        } else {
          setFailedDeliveries(null)
          qErr = qErr ?? errorMessage(failedResult.reason)
        }
        if (eventsResult.status === 'fulfilled') {
          setQueueEvents(eventsResult.value)
        } else {
          setQueueEvents(null)
          qErr = qErr ?? errorMessage(eventsResult.reason)
        }
        setQueueError(qErr)
      } finally {
        if (!signal?.aborted) setLoading(false)
      }
    },
    [api],
  )

  // User-triggered refresh — shows the loading state, then refetches.
  const refresh = useCallback(() => {
    setLoading(true)
    void load()
  }, [load])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => {
      controller.abort()
    }
  }, [load])

  const handleReplay = useCallback(
    async (id: number) => {
      setBusyJobId(id)
      setQueueError(null)
      try {
        await api.replayQueue({ id })
        await load()
      } catch (err) {
        setQueueError(errorMessage(err))
      } finally {
        setBusyJobId(null)
      }
    },
    [api, load],
  )

  const confirmPurge = useCallback(async () => {
    if (purgeTarget === null) return
    setBusyJobId(purgeTarget)
    setPurgeError(null)
    try {
      await api.purgeQueue({ id: purgeTarget })
      setPurgeTarget(null)
      await load()
    } catch (err) {
      setPurgeError(errorMessage(err))
    } finally {
      setBusyJobId(null)
    }
  }, [api, load, purgeTarget])

  const pending = jobs?.pending ?? []
  const failed = jobs?.failed ?? []
  const pendingDeliv = pendingDeliveries ?? []
  const failedDeliv = failedDeliveries ?? []
  const events = queueEvents ?? []

  return (
    <ListPageLayout
      header={
        <PageHeader title="Activity">
          <Button variant="outline" size="sm" onClick={refresh} disabled={loading}>
            <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </PageHeader>
      }
    >
      <>
        {/* Workers */}
        <Section title="Workers" count={loading ? undefined : workers?.workers.length}>
          {loading ? (
            <SkeletonRows />
          ) : workersError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{workersError}</p>
          ) : !workers || workers.workers.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No workers reported.</p>
          ) : (
            <table className="w-full text-[13px]">
              <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
                <tr className="border-b border-border-soft">
                  <th className="px-4 py-1.5 text-left font-medium">Name</th>
                  <th className="px-4 py-1.5 text-left font-medium">Kind</th>
                  <th className="px-4 py-1.5 text-left font-medium">State</th>
                  <th className="px-4 py-1.5 text-left font-medium">Detail</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border-soft">
                {workers.workers.map((w) => (
                  <tr key={w.name} className="hover:bg-panel-hover">
                    <td className="px-4 py-1.5 align-top text-text">{w.name}</td>
                    <td className="px-4 py-1.5 align-top uppercase tracking-[.12em] text-text-soft">
                      {w.kind}
                    </td>
                    <td className="px-4 py-1.5 align-top">
                      <span
                        className={`flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[.1em] ${
                          w.running ? 'text-status-indexed' : 'text-text-subtle'
                        }`}
                      >
                        <span
                          className={`size-1.5 rounded-full ${
                            w.running ? 'bg-status-indexed' : 'bg-text-subtle'
                          }`}
                        />
                        {w.running ? 'Running' : 'Stopped'}
                      </span>
                    </td>
                    <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
                      {orDash(w.detail)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Section>

        {/* Scheduler */}
        <Section
          title="Scheduler"
          count={loading ? undefined : workers?.scheduler.schedules.length}
        >
          {loading ? (
            <SkeletonRows />
          ) : workersError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{workersError}</p>
          ) : !workers ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No scheduler state reported.</p>
          ) : (
            <>
              <div className="flex items-center gap-2 px-4 pb-2 text-[12px]">
                <span
                  className={`size-1.5 rounded-full ${
                    workers.scheduler.running ? 'bg-status-indexed' : 'bg-text-subtle'
                  }`}
                />
                <span className="uppercase tracking-[.12em] text-text-subtle">Scheduler</span>
                <span
                  className={`font-semibold ${
                    workers.scheduler.running ? 'text-status-indexed' : 'text-text-subtle'
                  }`}
                >
                  {workers.scheduler.running ? 'Running' : 'Stopped'}
                </span>
              </div>
              {workers.scheduler.schedules.length === 0 ? (
                <p className="px-4 pb-3 text-[13px] text-text-muted">No schedules registered.</p>
              ) : (
                <table className="w-full text-[13px]">
                  <thead className="text-[10px] uppercase tracking-[.2em] text-text-subtle">
                    <tr className="border-b border-border-soft">
                      <th className="px-4 py-1.5 text-left font-medium">Ingest</th>
                      <th className="px-4 py-1.5 text-left font-medium">Cron</th>
                      <th className="px-4 py-1.5 text-left font-medium">Enabled</th>
                      <th className="px-4 py-1.5 text-left font-medium">Last run</th>
                      <th className="px-4 py-1.5 text-left font-medium">Next run</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-soft">
                    {workers.scheduler.schedules.map((s) => (
                      <tr key={`${s.ingest_name}:${s.cron_expr}`} className="hover:bg-panel-hover">
                        <td className="px-4 py-1.5 align-top text-text">{s.ingest_name}</td>
                        <td className="px-4 py-1.5 align-top font-mono text-[12px] text-text-muted">
                          {s.cron_expr}
                        </td>
                        <td className="px-4 py-1.5 align-top">
                          <span
                            className={`text-[11px] font-semibold ${
                              s.enabled ? 'text-status-indexed' : 'text-text-subtle'
                            }`}
                          >
                            {s.enabled ? 'Enabled' : 'Disabled'}
                          </span>
                        </td>
                        <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
                          {orDash(s.last_run)}
                        </td>
                        <td className="px-4 py-1.5 align-top text-[12px] text-text-muted">
                          {orDash(s.next_run)}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </>
          )}
        </Section>

        {/* Pending ingest jobs */}
        <Section title="Pending ingest jobs" count={loading ? undefined : pending.length}>
          {loading ? (
            <SkeletonRows />
          ) : jobsError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{jobsError}</p>
          ) : pending.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No pending jobs.</p>
          ) : (
            <JobsTable jobs={pending} showError={false} />
          )}
        </Section>

        {/* Failed ingest jobs */}
        <Section title="Failed ingest jobs" count={loading ? undefined : failed.length}>
          {loading ? (
            <SkeletonRows />
          ) : jobsError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{jobsError}</p>
          ) : failed.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No failed jobs.</p>
          ) : (
            <JobsTable jobs={failed} showError />
          )}
        </Section>

        {/* Delivery queue — pending */}
        <Section
          title="Pending deliveries"
          count={loading ? undefined : pendingDeliv.length}
        >
          {loading ? (
            <SkeletonRows />
          ) : queueError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{queueError}</p>
          ) : pendingDeliv.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No pending deliveries.</p>
          ) : (
            <DeliveryPendingTable items={pendingDeliv} />
          )}
        </Section>

        {/* Delivery queue — dead-letter */}
        <Section
          title="Failed deliveries"
          count={loading ? undefined : failedDeliv.length}
        >
          {loading ? (
            <SkeletonRows />
          ) : queueError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{queueError}</p>
          ) : failedDeliv.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No failed deliveries.</p>
          ) : (
            <DeliveryFailedTable
              items={failedDeliv}
              busyJobId={busyJobId}
              onReplay={(id) => void handleReplay(id)}
              onPurge={(id) => {
                setPurgeError(null)
                setPurgeTarget(id)
              }}
            />
          )}
        </Section>

        {/* Delivery queue — events */}
        <Section title="Delivery events" count={loading ? undefined : events.length}>
          {loading ? (
            <SkeletonRows />
          ) : queueError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{queueError}</p>
          ) : events.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No delivery events.</p>
          ) : (
            <EventsTable items={events} />
          )}
        </Section>

      <ConfirmDialog
        open={purgeTarget !== null}
        title="Purge failed delivery"
        confirmLabel="Purge"
        body={`Discard failed delivery job #${purgeTarget ?? ''}? This permanently removes it from the dead-letter queue.`}
        error={purgeError}
        busy={busyJobId !== null && busyJobId === purgeTarget}
        onConfirm={() => void confirmPurge()}
        onClose={() => {
          setPurgeTarget(null)
          setPurgeError(null)
        }}
      />
      </>
    </ListPageLayout>
  )
}
