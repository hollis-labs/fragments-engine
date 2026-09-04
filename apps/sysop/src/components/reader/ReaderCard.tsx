import type { ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'
import { ReaderCardMediaSeam } from './ReaderRendererSeam'
import { ReaderProvenanceSpine } from './ReaderProvenanceSpine'
import { ReaderQuickActionSeam } from './ReaderQuickActionSeam'
import { ReaderStateSummary } from './ReaderStateSummary'
import {
  boundedReaderText,
  readingStateLabel,
  safeReaderSourceHref,
  sourceHost,
  sourceLabel,
} from '@/lib/reader'
import { hasReaderCardInteraction, hasReaderCardSelection } from '@/lib/reader-navigation'
import type { ReaderItem } from '@/lib/types'

interface ReaderCardProps {
  item: ReaderItem
  onOpen: (fragmentId: string) => void
  mediaSlot?: ReactNode
  actionSlot?: ReactNode
}

function publishedLabel(value: string | undefined): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return undefined
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
  }).format(date)
}

export function ReaderCard({ item, onOpen, mediaSlot, actionSlot }: ReaderCardProps) {
  const source = sourceLabel(item)
  const host = sourceHost(item)
  const published = publishedLabel(item.display.published_at)
  const summary = boundedReaderText(item.display.summary.value)
  const sourceHref = safeReaderSourceHref(item)

  function openFromPointer(event: React.MouseEvent<HTMLElement>) {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    if (hasReaderCardInteraction(event.target, event.currentTarget)) return
    if (hasReaderCardSelection(event.currentTarget)) return
    onOpen(item.fragment_id)
  }

  function openFromKeyboard(event: React.KeyboardEvent<HTMLElement>) {
    if (event.target !== event.currentTarget) return
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    onOpen(item.fragment_id)
  }

  return (
    <article
      className="group relative cursor-pointer rounded-sm border border-border-soft bg-bg py-5 pl-7 pr-5 outline-none hover:bg-panel-hover-soft focus-visible:ring-2 focus-visible:ring-ring motion-reduce:transition-none sm:py-6 sm:pl-8 sm:pr-6"
      role="link"
      tabIndex={0}
      aria-label={`Open ${item.display.title.value || 'untitled fragment'}`}
      data-testid="reader-card"
      data-fragment-id={item.fragment_id}
      onClick={openFromPointer}
      onKeyDown={openFromKeyboard}
    >
      <ReaderProvenanceSpine item={item} />

      <div className="flex flex-col gap-5 lg:flex-row lg:items-start">
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] leading-4 text-text-subtle">
            <span className="font-medium text-text-soft">{source}</span>
            {host && <span>{host}</span>}
            {published && <time dateTime={item.display.published_at}>{published}</time>}
            <span>{item.capture_count === 1 ? 'Captured once' : `Captured ${item.capture_count} times`}</span>
            <span>{readingStateLabel(item.reading_state.state)}</span>
          </div>

          <h2 className="mt-2 line-clamp-2 text-[18px] font-semibold leading-6 text-text sm:text-[19px]">
            {item.display.title.value || 'Untitled fragment'}
          </h2>

          <p className="mt-2 line-clamp-3 max-w-[70ch] text-[14px] leading-[1.6] text-text-soft">
            {summary || 'No summary is available yet.'}
          </p>

          <p className="mt-2 text-[11px] leading-4 text-text-subtle">
            Title from {item.display.title.source}; summary from {item.display.summary.source}
          </p>

          {item.tags.combined.length > 0 && (
            <ul className="mt-3 flex flex-wrap gap-x-3 gap-y-1" aria-label="Tags">
              {item.tags.combined.slice(0, 4).map((tag) => (
                <li key={tag} className="text-[12px] text-text-muted">
                  #{tag}
                </li>
              ))}
              {item.tags.combined.length > 4 && (
                <li className="text-[12px] text-text-subtle">+{item.tags.combined.length - 4} more</li>
              )}
            </ul>
          )}
        </div>

        <div className="flex shrink-0 flex-wrap items-start gap-2 lg:max-w-[17rem] lg:justify-end">
          <div data-reader-media-slot data-reader-nav-exclude>
            {mediaSlot ?? <ReaderCardMediaSeam item={item} />}
          </div>
          <ReaderQuickActionSeam>{actionSlot}</ReaderQuickActionSeam>
        </div>
      </div>

      <div className="mt-5 border-t border-border-soft pt-4">
        <ReaderStateSummary item={item} compact />
      </div>

      {sourceHref && (
        <a
          className="mt-4 inline-flex min-h-11 items-center gap-1.5 text-[12px] text-text-subtle underline-offset-4 hover:text-text hover:underline focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:min-h-8"
          href={sourceHref}
          target="_blank"
          rel="noreferrer"
          data-reader-nav-exclude
        >
          View source
          <ExternalLink className="h-3 w-3" aria-hidden="true" />
        </a>
      )}
    </article>
  )
}
