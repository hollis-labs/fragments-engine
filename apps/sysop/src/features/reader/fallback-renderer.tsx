import { FileAudio, FileQuestion, FileText } from 'lucide-react'
import { cn } from '@hollis-labs/sysop-ui'
import { readerPlainTextExcerpt } from '@/lib/reader'

import { ArticleRenderer } from './article-renderer'
import { readerControlClass, stopReaderNavigation } from './interaction'
import { availableVariantHref, mediaState, orderedMedia, type ResourceState } from './media'
import { ResourceStatePanel } from './resource-state'
import type { ReaderMediaItem, ReaderRendererProps } from './types'

function aggregateState(media: ReaderMediaItem[]): ResourceState {
  if (media.some((item) => mediaState(item) === 'available')) return 'available'
  if (media.some((item) => mediaState(item) === 'pending')) return 'pending'
  if (media.some((item) => mediaState(item) === 'failed')) return 'failed'
  if (media.some((item) => mediaState(item) === 'reference_only')) return 'reference_only'
  return 'unavailable'
}

function firstAuthorizedHref(media: ReaderMediaItem[]): string | undefined {
  for (const item of orderedMedia(media)) {
    for (const variant of item.variants) {
      const href = availableVariantHref(variant)
      if (href) return href
    }
  }
  return undefined
}

function ExtensionRenderer({
  item,
  presentation,
  className,
  kind,
}: ReaderRendererProps & { kind: 'audio' | 'document' }) {
  const relevant = item.media.filter((media) => media.kind === kind)
  const state = aggregateState(relevant)
  const href = firstAuthorizedHref(relevant)
  const Icon = kind === 'audio' ? FileAudio : FileText
  const noun = kind === 'audio' ? 'Audio' : 'Document'

  return (
    <section
      className={cn('rounded-md border border-border bg-panel-2/40 p-4', className)}
      data-reader-renderer={kind}
      data-reader-presentation={presentation}
    >
      <div className="flex items-start gap-3">
        <Icon className="mt-0.5 h-5 w-5 shrink-0 text-text-soft" aria-hidden="true" />
        <div className="min-w-0">
          <h3 className="text-sm font-semibold leading-6 text-text">{noun}</h3>
          <p className="text-xs leading-5 text-text-subtle">
            {kind === 'audio'
              ? 'Inline audio playback is an extension point and is not enabled in this Reader.'
              : 'Inline page rendering is an extension point and is not enabled in this Reader.'}
          </p>
        </div>
      </div>
      {state !== 'available' && (
        <ResourceStatePanel state={state} compact className="mt-3" />
      )}
      {href && (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          className={`${readerControlClass} mt-3 inline-flex items-center border border-border px-3 text-xs font-medium text-text hover:bg-panel-hover`}
          onClick={stopReaderNavigation}
          onKeyDown={stopReaderNavigation}
        >
          Open authorized {kind}
        </a>
      )}
    </section>
  )
}

export function TextRenderer(props: ReaderRendererProps) {
  return <ArticleRenderer {...props} />
}

export function AudioRenderer(props: ReaderRendererProps) {
  return <ExtensionRenderer {...props} kind="audio" />
}

export function DocumentRenderer(props: ReaderRendererProps) {
  return <ExtensionRenderer {...props} kind="document" />
}

export function UnknownRenderer({ item, presentation, className }: ReaderRendererProps) {
  const text = readerPlainTextExcerpt(
    item.article.preview_markdown.trim() || item.display.summary.value.trim(),
    presentation === 'card' ? 480 : 2400,
  )
  return (
    <section
      className={cn('py-2', className)}
      data-reader-renderer="unknown"
      data-reader-presentation={presentation}
    >
      <div className="flex items-start gap-3">
        <FileQuestion className="mt-0.5 h-5 w-5 shrink-0 text-text-subtle" aria-hidden="true" />
        <div className="min-w-0">
          <h3 className="text-sm font-semibold leading-6 text-text">Readable text</h3>
          <p
            className={cn(
              'text-sm leading-6 text-text-muted',
              presentation === 'card' && 'line-clamp-5',
            )}
          >
            {text || 'This item has no readable preview.'}
          </p>
        </div>
      </div>
    </section>
  )
}
