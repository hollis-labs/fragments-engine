import { useState, type FormEvent } from 'react'
import { Check, LoaderCircle, Plus, RotateCcw, X } from 'lucide-react'
import { Input, cn } from '@hollis-labs/sysop-ui'

import { createReaderIntentIdentity, hasReaderCommand, readerCommandBase } from '@/lib/reader-actions'
import type { ReaderCommand, ReaderItem } from '@/lib/types'
import { useReaderCommandExecution } from './useReaderCommandExecution'

interface ReaderTagsProps {
  item: ReaderItem
  onItemChange: (item: ReaderItem) => void
  compact?: boolean
}

export function ReaderTags({ item, onItemChange, compact = false }: ReaderTagsProps) {
  const [adding, setAdding] = useState(false)
  const [draft, setDraft] = useState('')
  const { execute, feedback, pending, retry, retryAvailable, resetFeedback } =
    useReaderCommandExecution(item, onItemChange)
  const canAdd = hasReaderCommand(item, 'add_tag')
  const canRemove = hasReaderCommand(item, 'remove_tag')

  async function addTag(event: FormEvent) {
    event.preventDefault()
    const tag = draft.trim()
    if (!tag || pending || !canAdd) return
    const identity = createReaderIntentIdentity()
    const command: ReaderCommand = {
      ...readerCommandBase(item, 'add_tag', identity),
      command: 'add_tag',
      tag,
    }
    if (await execute(command)) {
      setDraft('')
      setAdding(false)
    }
  }

  function removeTag(tag: string) {
    if (pending || !canRemove) return
    const identity = createReaderIntentIdentity()
    void execute({
      ...readerCommandBase(item, 'remove_tag', identity),
      command: 'remove_tag',
      tag,
    })
  }

  if (item.tags.combined.length === 0 && !canAdd) return null

  return (
    <div
      className="flex min-w-0 flex-wrap items-center gap-1.5"
      data-reader-tags
      data-reader-nav-exclude
      onClick={(event) => event.stopPropagation()}
      onKeyDown={(event) => event.stopPropagation()}
    >
      <span className="sr-only">Tags</span>
      {item.tags.combined.map((tag) => (
        <span
          key={tag}
          className={cn(
            'group/tag inline-flex items-center rounded-full bg-panel-2 text-text-muted',
            compact ? 'min-h-7 pl-2.5 text-[11px]' : 'min-h-8 pl-3 text-[12px]',
            !canRemove && (compact ? 'pr-2.5' : 'pr-3'),
          )}
        >
          #{tag}
          {canRemove && (
            <button
              type="button"
              className={cn(
                'ml-0.5 inline-flex items-center justify-center rounded-full text-text-subtle opacity-0 outline-none transition-opacity hover:bg-bg hover:text-text focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring group-hover/tag:opacity-100 motion-reduce:transition-none',
                compact ? 'h-7 w-7' : 'h-8 w-8',
              )}
              aria-label={`Remove tag ${tag}`}
              onClick={() => removeTag(tag)}
              disabled={pending}
            >
              <X className="h-3 w-3" aria-hidden="true" />
            </button>
          )}
        </span>
      ))}

      {adding ? (
        <form className="flex items-center gap-1" onSubmit={(event) => void addTag(event)}>
          <Input
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              event.stopPropagation()
              if (event.key === 'Escape') {
                setDraft('')
                setAdding(false)
                resetFeedback()
              }
            }}
            maxLength={128}
            className={cn('w-32 rounded-full px-3 text-[12px]', compact ? 'h-7 min-h-7' : 'h-8 min-h-8')}
            placeholder="New tag"
            aria-label="New tag"
            autoFocus
          />
          <button
            type="submit"
            className={cn(
              'inline-flex items-center justify-center rounded-full bg-panel-2 text-text-muted outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-ring',
              compact ? 'h-7 w-7' : 'h-8 w-8',
            )}
            aria-label="Save tag"
            disabled={!draft.trim() || pending}
          >
            {pending ? (
              <LoaderCircle className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
            ) : (
              <Check className="h-3.5 w-3.5" aria-hidden="true" />
            )}
          </button>
        </form>
      ) : canAdd ? (
        <button
          type="button"
          className={cn(
            'inline-flex items-center gap-1 rounded-full bg-panel-2 px-2.5 text-text-subtle outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-ring',
            compact ? 'min-h-7 text-[11px]' : 'min-h-8 text-[12px]',
          )}
          aria-label="Add tag"
          onClick={() => {
            setAdding(true)
            resetFeedback()
          }}
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          Tag
        </button>
      ) : null}

      {feedback.state !== 'idle' && feedback.state !== 'success' && (
        <span
          className={cn(
            'inline-flex items-center gap-1 text-[11px]',
            feedback.state === 'failure' ? 'text-danger-soft' : 'text-status-paused',
          )}
          role="status"
          aria-live="polite"
        >
          {feedback.message}
          {feedback.state === 'uncertain' && retryAvailable && (
            <button
              type="button"
              className="inline-flex h-7 items-center gap-1 rounded-full px-2 text-text outline-none hover:bg-panel-2 focus-visible:ring-2 focus-visible:ring-ring"
              onClick={() => void retry()}
            >
              <RotateCcw className="h-3 w-3" aria-hidden="true" />
              Retry
            </button>
          )}
        </span>
      )}
    </div>
  )
}
