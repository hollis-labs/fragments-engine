import { useEffect, useState, type FocusEvent } from 'react'
import { LoaderCircle, RotateCcw } from 'lucide-react'
import { Textarea, cn } from '@hollis-labs/sysop-ui'

import { createReaderIntentIdentity, hasReaderCommand, readerCommandBase } from '@/lib/reader-actions'
import type { ReaderCommand, ReaderItem } from '@/lib/types'
import { useReaderCommandExecution } from './useReaderCommandExecution'

export type ReaderNoteKind = 'curated' | 'capture'

interface ReaderNoteEditorProps {
  item: ReaderItem
  onItemChange: (item: ReaderItem) => void
  kind: ReaderNoteKind
  compact?: boolean
}

export function ReaderNoteEditor({
  item,
  onItemChange,
  kind,
  compact = false,
}: ReaderNoteEditorProps) {
  const serverCuratedNote = item.curated_note?.body_markdown ?? ''
  const [draft, setDraft] = useState(kind === 'curated' ? serverCuratedNote : '')
  const [dirty, setDirty] = useState(false)
  const { execute, feedback, pending, retry, retryAvailable } =
    useReaderCommandExecution(item, onItemChange)
  const canSave = hasReaderCommand(
    item,
    kind === 'curated' ? 'update_curated_note' : 'append_capture_note',
  )

  useEffect(() => {
    if (
      kind !== 'curated' ||
      !canSave ||
      !dirty ||
      pending ||
      draft === serverCuratedNote
    ) {
      return
    }
    const timeout = window.setTimeout(() => {
      const identity = createReaderIntentIdentity()
      const command: ReaderCommand = {
        ...readerCommandBase(item, 'update_curated_note', identity),
        command: 'update_curated_note',
        expected_note_revision: item.curated_note?.revision ?? 0,
        body_markdown: draft,
      }
      void execute(command).then((saved) => {
        if (saved) setDirty(false)
      })
    }, 750)
    return () => window.clearTimeout(timeout)
  }, [canSave, dirty, draft, execute, item, kind, pending, serverCuratedNote])

  async function saveCaptureNote() {
    const text = draft.trim()
    if (kind !== 'capture' || !canSave || !dirty || !text || pending) return
    const identity = createReaderIntentIdentity()
    const command: ReaderCommand = {
      ...readerCommandBase(item, 'append_capture_note', identity),
      command: 'append_capture_note',
      annotation_id: `annotation-${identity.command_id}`,
      text,
    }
    if (await execute(command)) {
      setDraft('')
      setDirty(false)
    }
  }

  function handleBlur(event: FocusEvent<HTMLTextAreaElement>) {
    if (event.currentTarget.contains(event.relatedTarget)) return
    void saveCaptureNote()
  }

  const captureNotes = item.annotations
    .filter((annotation) => annotation.kind === 'capture_note')
    .toSorted((left, right) => right.captured_at.localeCompare(left.captured_at))

  return (
    <div
      className="min-w-0"
      data-reader-notes
      data-reader-nav-exclude
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => event.stopPropagation()}
    >
      <Textarea
        value={draft}
        onChange={(event) => {
          setDraft(event.target.value)
          setDirty(true)
        }}
        onBlur={handleBlur}
        onKeyDown={(event) => {
          event.stopPropagation()
          if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
            event.preventDefault()
            void saveCaptureNote()
          }
        }}
        maxLength={kind === 'curated' ? 524_288 : 65_536}
        className={cn(
          'w-full resize-y border-border-soft bg-transparent text-text placeholder:text-text-subtle focus-visible:ring-1',
          compact ? 'min-h-28 text-[13px] leading-5' : 'min-h-36 text-[14px] leading-6',
        )}
        placeholder={
          kind === 'curated'
            ? 'Keep the durable working note here'
            : 'Add context from this reading pass'
        }
        aria-label={kind === 'curated' ? 'Curated note' : 'Capture note'}
        disabled={!canSave}
      />
      <div className="mt-1.5 flex min-h-5 items-center justify-between gap-3 text-[11px] text-text-subtle">
        <span>
          {!canSave
            ? 'Editing is unavailable'
            : pending
              ? 'Saving…'
              : feedback.state === 'success'
                ? feedback.message
                : kind === 'curated'
                  ? 'Saves automatically'
                  : 'Saves when you leave the field'}
        </span>
        {pending && <LoaderCircle className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />}
        {(feedback.state === 'failure' || feedback.state === 'conflict' || feedback.state === 'uncertain') && (
          <span className={feedback.state === 'failure' ? 'text-danger-soft' : 'text-status-paused'} role="status">
            {feedback.message}
          </span>
        )}
        {feedback.state === 'uncertain' && retryAvailable && (
          <button
            type="button"
            className="inline-flex min-h-7 items-center gap-1 rounded-full px-2 text-text outline-none hover:bg-panel-2 focus-visible:ring-2 focus-visible:ring-ring"
            onClick={() => void retry()}
          >
            <RotateCcw className="h-3 w-3" aria-hidden="true" />
            Retry
          </button>
        )}
      </div>

      {kind === 'capture' && captureNotes.length > 0 && (
        <div className="mt-3 border-t border-border-soft pt-3">
          <p className="text-[11px] font-medium text-text-subtle">Recent capture notes</p>
          <ul className="mt-2 grid gap-2">
            {captureNotes.slice(0, compact ? 2 : 4).map((note) => (
              <li key={note.annotation_id} className="text-[12px] leading-5 text-text-muted">
                {note.text}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

export function ReaderNotes({
  item,
  onItemChange,
  compact = false,
}: Omit<ReaderNoteEditorProps, 'kind'>) {
  const [active, setActive] = useState<ReaderNoteKind>('curated')

  return (
    <section data-reader-notes-tabs data-reader-nav-exclude>
      <div className="mb-3 flex items-center gap-1 border-b border-border-soft" role="tablist" aria-label="Reader notes">
        {(['curated', 'capture'] as const).map((kind) => (
          <button
            key={kind}
            type="button"
            role="tab"
            aria-selected={active === kind}
            className={cn(
              'min-h-9 border-b px-3 text-[12px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring',
              active === kind
                ? 'border-primary text-text'
                : 'border-transparent text-text-subtle hover:text-text',
            )}
            onClick={() => setActive(kind)}
          >
            {kind === 'curated' ? 'Curated note' : 'Capture note'}
          </button>
        ))}
      </div>
      <ReaderNoteEditor
        key={`${active}-${active === 'curated' ? item.curated_note?.revision ?? 0 : 'append'}`}
        item={item}
        onItemChange={onItemChange}
        kind={active}
        compact={compact}
      />
    </section>
  )
}
