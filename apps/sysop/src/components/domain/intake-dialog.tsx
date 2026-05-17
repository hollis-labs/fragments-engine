import { useEffect, useState } from 'react'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { CopyableId } from './copyable-id'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import type { Fragment } from '@/lib/types'

interface IntakeDialogProps {
  open: boolean
  onClose: () => void
  /** Called after a fragment is successfully created. */
  onCreated?: (fragment: Fragment) => void
}

const FIELD =
  'w-full rounded-md border border-border bg-bg px-2 py-1.5 text-sm text-text outline-none transition focus:border-border-strong'
const LABEL = 'text-[10px] font-semibold uppercase tracking-[.18em] text-text-subtle'

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

/** Splits a comma/space separated tag string into a clean list. */
function parseTags(raw: string): string[] {
  return raw
    .split(/[,\n]/)
    .map((t) => t.trim())
    .filter(Boolean)
}

/** Manual intake — POSTs free-form content to /v1/intake to create a fragment. */
export function IntakeDialog({ open, onClose, onCreated }: IntakeDialogProps) {
  const api = useApi()

  const [content, setContent] = useState('')
  const [title, setTitle] = useState('')
  const [sourceType, setSourceType] = useState('')
  const [tags, setTags] = useState('')

  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [created, setCreated] = useState<Fragment | null>(null)

  useEffect(() => {
    if (!open) return
    setContent('')
    setTitle('')
    setSourceType('')
    setTags('')
    setSubmitting(false)
    setError(null)
    setCreated(null)
  }, [open])

  async function handleSubmit() {
    if (!content.trim()) {
      setError('Content is required.')
      return
    }
    setSubmitting(true)
    setError(null)
    try {
      const fragment = await api.createIntake({
        content: content.trim(),
        title,
        sourceType,
        tags: parseTags(tags),
      })
      setCreated(fragment)
      onCreated?.(fragment)
    } catch (err) {
      setError(errMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o: boolean) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogTitle className="text-base font-semibold tracking-tight text-text">
          Manual intake
        </DialogTitle>

        {created ? (
          <div className="mt-3 flex flex-col gap-3">
            <div className="rounded-md border border-status-indexed/40 bg-status-indexed/10 px-3 py-2">
              <p className="text-[11px] font-semibold uppercase tracking-[.14em] text-status-indexed">
                Fragment created
              </p>
              <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[12px] text-text-muted">
                <span className="font-mono text-text-subtle/80">id:</span>
                <CopyableId id={created.id} />
              </div>
              {created.title && (
                <p className="mt-1 truncate text-[13px] text-text" title={created.title}>
                  {created.title}
                </p>
              )}
            </div>
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={() => setCreated(null)}>
                Add another
              </Button>
              <Button size="sm" onClick={onClose}>
                Done
              </Button>
            </div>
          </div>
        ) : (
          <div className="mt-3 flex flex-col gap-3">
            <Field label="Content">
              <textarea
                className={`${FIELD} min-h-32 resize-y leading-5`}
                value={content}
                onChange={(e) => setContent(e.target.value)}
                placeholder="Paste or type the fragment content…"
              />
            </Field>
            <Field label="Title">
              <input
                className={FIELD}
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder="optional"
              />
            </Field>
            <Field label="Source type">
              <input
                className={FIELD}
                value={sourceType}
                onChange={(e) => setSourceType(e.target.value)}
                placeholder="optional — e.g. note, clip"
              />
            </Field>
            <Field label="Tags">
              <input
                className={FIELD}
                value={tags}
                onChange={(e) => setTags(e.target.value)}
                placeholder="optional — comma separated"
              />
            </Field>
            {error && <p className="text-sm text-danger-soft">{error}</p>}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" size="sm" onClick={onClose}>
                Cancel
              </Button>
              <Button size="sm" onClick={handleSubmit} disabled={submitting}>
                {submitting ? 'Creating…' : 'Create fragment'}
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
