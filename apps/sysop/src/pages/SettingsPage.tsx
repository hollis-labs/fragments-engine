import { useCallback, useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { RefreshCw } from 'lucide-react'
import { PageHeader } from '@/components/domain/page-header'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import ThemeSwitcher from '@/components/theme-switcher'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { RecallStatus } from '@/lib/api'

const LABEL_CLASS =
  'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'

/** Bordered section with an uppercase label header and a content body. */
function Section({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <section className="border-b border-border-strong">
      <header className="px-4 py-2">
        <h2 className={LABEL_CLASS}>{label}</h2>
      </header>
      <div>{children}</div>
    </section>
  )
}

/** "indexed_count" -> "Indexed count" */
function humanizeKey(key: string): string {
  return key
    .replace(/_/g, ' ')
    .split(' ')
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ')
}

/** Render any status value defensively for display. */
function formatValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? 'yes' : 'no'
  if (typeof value === 'string' || typeof value === 'number') {
    return String(value)
  }
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

export default function SettingsPage() {
  const api = useApi()

  const [status, setStatus] = useState<RecallStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const loadStatus = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const next = await api.fetchRecallStatus()
      setStatus(next)
    } catch (err) {
      const message =
        err instanceof ApiError
          ? err.message
          : 'Failed to load engine status.'
      setError(message)
      setStatus(null)
    } finally {
      setLoading(false)
    }
  }, [api])

  useEffect(() => {
    void loadStatus()
  }, [loadStatus])

  const entries = status ? Object.entries(status) : []

  return (
    <div className="flex h-full min-h-0 w-full flex-col">
      <div className="shrink-0">
        <PageHeader title="Settings">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void loadStatus()}
            disabled={loading}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            Refresh
          </Button>
        </PageHeader>
      </div>

      <div className="min-h-0 flex-1 overflow-auto">
        <Section label="Engine status">
          {loading ? (
            <div className="flex flex-col gap-2 px-4 py-3">
              <Skeleton className="h-5 w-full rounded-md" />
              <Skeleton className="h-5 w-full rounded-md" />
              <Skeleton className="h-5 w-2/3 rounded-md" />
            </div>
          ) : error ? (
            <p className="px-4 py-3 text-[13px] text-danger-soft">{error}</p>
          ) : entries.length === 0 ? (
            <p className="px-4 py-3 text-[13px] text-text-muted">
              No status reported.
            </p>
          ) : (
            <dl className="divide-y divide-border-soft">
              {entries.map(([key, value]) => (
                <div
                  key={key}
                  className="flex justify-between gap-4 px-4 py-2 text-[13px]"
                >
                  <dt className="text-text-subtle">{humanizeKey(key)}</dt>
                  <dd className="text-text-soft text-right break-all">
                    {formatValue(value)}
                  </dd>
                </div>
              ))}
            </dl>
          )}
        </Section>

        <Section label="Appearance">
          <div className="flex items-center justify-between gap-4 px-4 py-3">
            <span className="text-[13px] text-text-soft">Theme palette</span>
            <ThemeSwitcher />
          </div>
        </Section>

        <Section label="About">
          <p className="px-4 py-3 text-[13px] text-text-muted">
            Engine configuration &mdash; ingest sources, recall backend, and
            embeddings &mdash; is defined in{' '}
            <code className="text-text-soft">fragments.example.yaml</code> and
            is not editable from the UI. Destination retry and queue policies
            are managed per-destination on the Routing page.
          </p>
        </Section>
      </div>
    </div>
  )
}
