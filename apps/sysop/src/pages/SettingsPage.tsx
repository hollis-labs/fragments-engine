import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { RefreshCw } from 'lucide-react'
import { ListPageLayout, PageHeader, Button, Skeleton, ThemeSwitcher } from '@hollis-labs/sysop-ui'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { JsonObject, JsonValue } from '@/lib/types'

const LABEL_CLASS = 'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'
const FIELD =
  'h-8 w-full max-w-xs rounded-md border border-border bg-bg px-2 text-sm text-text outline-none transition focus:border-border-strong'

/** Bordered section with an uppercase label header and a content body. */
function Section({ label, children }: { label: string; children: ReactNode }) {
  return (
    <section className="border-b border-border-strong">
      <header className="px-4 py-2">
        <h2 className={LABEL_CLASS}>{label}</h2>
      </header>
      <div>{children}</div>
    </section>
  )
}

/* ───────────────────────────── nested-path helpers ───────────────────────────── */

function isObject(value: unknown): value is JsonObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

/** Reads a dotted path (e.g. "queue.batch_size") out of a config object. */
function getPath(config: JsonObject, path: string): JsonValue | undefined {
  let cursor: JsonValue | undefined = config
  for (const segment of path.split('.')) {
    if (!isObject(cursor)) return undefined
    cursor = cursor[segment]
  }
  return cursor
}

/** Returns a deep clone with a dotted path set to `value`, creating objects as needed. */
function setPath(config: JsonObject, path: string, value: JsonValue): JsonObject {
  const next = structuredClone(config)
  const segments = path.split('.')
  let cursor: JsonObject = next
  for (let i = 0; i < segments.length - 1; i += 1) {
    const seg = segments[i]
    const existing = cursor[seg]
    if (!isObject(existing)) {
      cursor[seg] = {}
    }
    cursor = cursor[seg] as JsonObject
  }
  cursor[segments[segments.length - 1]] = value
  return next
}

/** Render any config value defensively for read-only display. */
function formatValue(value: unknown): string {
  if (value === null || value === undefined) return '—'
  if (typeof value === 'boolean') return value ? 'yes' : 'no'
  if (typeof value === 'string' || typeof value === 'number') return String(value)
  try {
    return JSON.stringify(value)
  } catch {
    return String(value)
  }
}

/* ───────────────────────────── editable field schema ───────────────────────────── */

type FieldKind = 'boolean' | 'number' | 'text'
type ConfigSection = 'Queue' | 'Recall' | 'Attachment analysis'

interface ConfigField {
  path: string
  label: string
  kind: FieldKind
  section: ConfigSection
}

/** Editable sections, rendered in order from the field schema below. */
const EDITABLE_SECTIONS: ConfigSection[] = ['Queue', 'Recall', 'Attachment analysis']

/** The SAFE editable subset — everything else is read-only. */
const EDITABLE_FIELDS: ConfigField[] = [
  { path: 'queue.auto_drain', label: 'Auto drain', kind: 'boolean', section: 'Queue' },
  {
    path: 'queue.poll_interval_seconds',
    label: 'Poll interval (s)',
    kind: 'number',
    section: 'Queue',
  },
  { path: 'queue.batch_size', label: 'Batch size', kind: 'number', section: 'Queue' },
  {
    path: 'queue.replay_cooldown_seconds',
    label: 'Replay cooldown (s)',
    kind: 'number',
    section: 'Queue',
  },
  {
    path: 'queue.max_replays_per_hour',
    label: 'Max replays / hour',
    kind: 'number',
    section: 'Queue',
  },
  {
    path: 'queue.alert_pending_threshold',
    label: 'Alert pending threshold',
    kind: 'number',
    section: 'Queue',
  },
  {
    path: 'queue.alert_dead_letter_threshold',
    label: 'Alert dead-letter threshold',
    kind: 'number',
    section: 'Queue',
  },
  { path: 'recall.backend', label: 'Recall backend', kind: 'text', section: 'Recall' },
  {
    path: 'recall.vanta.embedding_provider',
    label: 'Embedding provider',
    kind: 'text',
    section: 'Recall',
  },
  {
    path: 'recall.vanta.embedding_model',
    label: 'Embedding model',
    kind: 'text',
    section: 'Recall',
  },
  {
    path: 'analysis.attachments.backend',
    label: 'Attachment backend',
    kind: 'text',
    section: 'Attachment analysis',
  },
  {
    path: 'analysis.attachments.fallback_backend',
    label: 'Attachment fallback backend',
    kind: 'text',
    section: 'Attachment analysis',
  },
  {
    path: 'analysis.attachments.min_confidence',
    label: 'Min confidence',
    kind: 'number',
    section: 'Attachment analysis',
  },
]

/** Read-only config paths shown for context only. */
const READ_ONLY_PATHS: string[] = ['database.path', 'ingests', 'delivery']

/* ───────────────────────────── editable form row ───────────────────────────── */

interface FormRowProps {
  field: ConfigField
  /** Current string-backed value for text/number fields, ignored for boolean. */
  value: string
  checked: boolean
  onChange: (next: string) => void
  onToggle: (next: boolean) => void
}

function FormRow({ field, value, checked, onChange, onToggle }: FormRowProps) {
  return (
    <div className="flex items-center justify-between gap-4 px-4 py-2">
      <label htmlFor={`cfg-${field.path}`} className="text-[13px] text-text-soft">
        {field.label}
        <span className="ml-2 font-mono text-[10px] text-text-subtle/70">{field.path}</span>
      </label>
      {field.kind === 'boolean' ? (
        <input
          id={`cfg-${field.path}`}
          type="checkbox"
          checked={checked}
          onChange={(e) => onToggle(e.target.checked)}
          className="size-3.5 accent-[var(--color-text-soft)]"
        />
      ) : (
        <input
          id={`cfg-${field.path}`}
          className={FIELD}
          value={value}
          inputMode={field.kind === 'number' ? 'decimal' : undefined}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
    </div>
  )
}

/* ───────────────────────────────── page ───────────────────────────────── */

export default function SettingsPage() {
  const api = useApi()

  const [config, setConfig] = useState<JsonObject | null>(null)
  const [configPath, setConfigPath] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Draft string-backed values keyed by field path; booleans tracked separately.
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [bools, setBools] = useState<Record<string, boolean>>({})

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  const seedDrafts = useCallback((next: JsonObject) => {
    const nextDrafts: Record<string, string> = {}
    const nextBools: Record<string, boolean> = {}
    for (const field of EDITABLE_FIELDS) {
      const raw = getPath(next, field.path)
      if (field.kind === 'boolean') {
        nextBools[field.path] = raw === true
      } else if (typeof raw === 'string' || typeof raw === 'number') {
        nextDrafts[field.path] = String(raw)
      } else {
        nextDrafts[field.path] = ''
      }
    }
    setDrafts(nextDrafts)
    setBools(nextBools)
  }, [])

  const loadConfig = useCallback(async () => {
    setLoading(true)
    setError(null)
    setSaveError(null)
    setSaved(false)
    try {
      const result = await api.fetchEngineConfig()
      setConfig(result.config)
      setConfigPath(result.path)
      seedDrafts(result.config)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Failed to load engine config.')
      setConfig(null)
    } finally {
      setLoading(false)
    }
  }, [api, seedDrafts])

  useEffect(() => {
    void loadConfig()
  }, [loadConfig])

  /** Merges the edited subset back into the full config object. */
  const buildMergedConfig = useCallback((): JsonObject | null => {
    if (!config) return null
    let merged = config
    for (const field of EDITABLE_FIELDS) {
      if (field.kind === 'boolean') {
        merged = setPath(merged, field.path, bools[field.path] ?? false)
        continue
      }
      const raw = (drafts[field.path] ?? '').trim()
      // A blank field keeps the originally-loaded value rather than writing
      // null / "" — config.Validate() would reject those and an empty knob
      // could silently break the engine.
      if (raw === '') {
        continue
      }
      if (field.kind === 'number') {
        merged = setPath(merged, field.path, Number(raw))
      } else {
        merged = setPath(merged, field.path, raw)
      }
    }
    return merged
  }, [config, drafts, bools])

  /** Updates a draft text/number field and clears any stale save banner. */
  const handleDraftChange = useCallback((path: string, next: string) => {
    setDrafts((prev) => ({ ...prev, [path]: next }))
    setSaved(false)
    setSaveError(null)
  }, [])

  /** Toggles a draft boolean field and clears any stale save banner. */
  const handleBoolToggle = useCallback((path: string, next: boolean) => {
    setBools((prev) => ({ ...prev, [path]: next }))
    setSaved(false)
    setSaveError(null)
  }, [])

  async function handleSave() {
    if (!config) return
    // Validate numeric fields before sending the whole object.
    for (const field of EDITABLE_FIELDS) {
      if (field.kind !== 'number') continue
      const raw = (drafts[field.path] ?? '').trim()
      if (raw !== '' && !Number.isFinite(Number(raw))) {
        setSaveError(`${field.label} must be a number.`)
        return
      }
    }
    const merged = buildMergedConfig()
    if (!merged) return

    setSaving(true)
    setSaveError(null)
    setSaved(false)
    try {
      await api.updateEngineConfig(merged)
      setConfig(merged)
      seedDrafts(merged)
      setSaved(true)
    } catch (err) {
      setSaveError(
        err instanceof ApiError || err instanceof Error
          ? err.message
          : 'Failed to save config.',
      )
    } finally {
      setSaving(false)
    }
  }

  const readOnlyEntries = useMemo(() => {
    if (!config) return []
    return READ_ONLY_PATHS.map((path) => ({
      path,
      value: getPath(config, path),
    }))
  }, [config])

  return (
    <ListPageLayout
      header={
        <PageHeader title="Settings">
          <Button
            variant="outline"
            size="sm"
            onClick={() => void loadConfig()}
            disabled={loading || saving}
          >
            <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
          <Button size="sm" onClick={() => void handleSave()} disabled={loading || saving || !config}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </PageHeader>
      }
    >
      <>
        {saved && (
          <div className="border-b border-status-indexed/40 bg-status-indexed/10 px-4 py-2">
            <p className="text-[12px] font-medium text-status-indexed">
              Config saved — restart the engine to apply.
            </p>
          </div>
        )}
        {saveError && (
          <div className="border-b border-danger-soft/40 bg-danger-soft/10 px-4 py-2">
            <p className="text-[12px] text-danger-soft">{saveError}</p>
          </div>
        )}

        {loading ? (
          <div className="flex flex-col gap-2 px-4 py-3">
            <Skeleton className="h-5 w-full rounded-md" />
            <Skeleton className="h-5 w-full rounded-md" />
            <Skeleton className="h-5 w-2/3 rounded-md" />
          </div>
        ) : error ? (
          <p className="px-4 py-3 text-[13px] text-danger-soft">{error}</p>
        ) : config ? (
          <>
            {EDITABLE_SECTIONS.map((section) => (
              <Section key={section} label={section}>
                <div className="divide-y divide-border-soft">
                  {EDITABLE_FIELDS.filter((f) => f.section === section).map((field) => (
                    <FormRow
                      key={field.path}
                      field={field}
                      value={drafts[field.path] ?? ''}
                      checked={bools[field.path] ?? false}
                      onChange={(next) => handleDraftChange(field.path, next)}
                      onToggle={(next) => handleBoolToggle(field.path, next)}
                    />
                  ))}
                </div>
              </Section>
            ))}

            <Section label="Read-only">
              <dl className="divide-y divide-border-soft">
                {readOnlyEntries.map(({ path, value }) => (
                  <div
                    key={path}
                    className="flex justify-between gap-4 px-4 py-2 text-[13px]"
                  >
                    <dt className="font-mono text-text-subtle">{path}</dt>
                    <dd className="break-all text-right text-text-soft">{formatValue(value)}</dd>
                  </div>
                ))}
              </dl>
              <p className="px-4 py-2 text-[11px] text-text-subtle">
                These values are managed outside the UI and shown for reference only.
              </p>
            </Section>
          </>
        ) : null}

        <Section label="Appearance">
          <div className="flex items-center justify-between gap-4 px-4 py-3">
            <span className="text-[13px] text-text-soft">Theme palette</span>
            <ThemeSwitcher />
          </div>
        </Section>

        <Section label="About">
          <p className="px-4 py-3 text-[13px] text-text-muted">
            Editing the queue, recall, and attachment-analysis settings rewrites{' '}
            <code className="text-text-soft">{configPath || 'the engine config file'}</code>.
            Saved changes take effect after the engine is restarted. Destination retry and queue
            policies are managed per-destination on the Routing page.
          </p>
        </Section>
      </>
    </ListPageLayout>
  )
}
