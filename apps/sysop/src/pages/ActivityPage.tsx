import { useCallback, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { RefreshCw } from 'lucide-react'
import { PageHeader } from '@/components/domain/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { IngestJobRecord, IngestJobs, WorkersStatus } from '@/lib/api'

function errorMessage(err: unknown): string {
  if (err instanceof ApiError || err instanceof Error) return err.message
  return String(err)
}

function orDash(value: string | undefined): string {
  return value && value.trim() !== '' ? value : '—'
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

export default function ActivityPage() {
  const api = useApi()

  const [jobs, setJobs] = useState<IngestJobs | null>(null)
  const [workers, setWorkers] = useState<WorkersStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [jobsError, setJobsError] = useState<string | null>(null)
  const [workersError, setWorkersError] = useState<string | null>(null)

  // Fetches both panels. The loading flag is owned by the caller so the
  // initial effect run never triggers a synchronous setState.
  const load = useCallback(async () => {
    try {
      const [jobsResult, workersResult] = await Promise.allSettled([
        api.fetchIngestJobs(),
        api.fetchWorkersStatus(),
      ])
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
    } finally {
      setLoading(false)
    }
  }, [api])

  // User-triggered refresh — shows the loading state, then refetches.
  const refresh = useCallback(() => {
    setLoading(true)
    void load()
  }, [load])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      if (!cancelled) await load()
    })()
    return () => {
      cancelled = true
    }
  }, [load])

  const pending = jobs?.pending ?? []
  const failed = jobs?.failed ?? []

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      <div className="shrink-0">
        <PageHeader title="Activity">
          <Button variant="outline" size="sm" onClick={refresh} disabled={loading}>
            <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </PageHeader>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        {/* Workers */}
        <Section title="Workers" count={loading ? undefined : workers?.workers.length}>
          {loading ? (
            <div className="space-y-2 px-4 pb-3">
              <Skeleton className="h-7 w-full" />
              <Skeleton className="h-7 w-5/6" />
            </div>
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
            <div className="space-y-2 px-4 pb-3">
              <Skeleton className="h-7 w-full" />
              <Skeleton className="h-7 w-5/6" />
            </div>
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
            <div className="space-y-2 px-4 pb-3">
              <Skeleton className="h-7 w-full" />
              <Skeleton className="h-7 w-5/6" />
            </div>
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
            <div className="space-y-2 px-4 pb-3">
              <Skeleton className="h-7 w-full" />
              <Skeleton className="h-7 w-5/6" />
            </div>
          ) : jobsError ? (
            <p className="px-4 pb-3 text-[13px] text-danger-soft">{jobsError}</p>
          ) : failed.length === 0 ? (
            <p className="px-4 pb-3 text-[13px] text-text-muted">No failed jobs.</p>
          ) : (
            <JobsTable jobs={failed} showError />
          )}
        </Section>
      </div>
    </div>
  )
}
