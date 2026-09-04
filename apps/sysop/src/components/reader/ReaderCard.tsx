import { useState, type ReactNode } from 'react'
import { ExternalLink } from 'lucide-react'
import { ReaderCardMediaSeam } from './ReaderRendererSeam'
import { ReaderProvenanceSpine } from './ReaderProvenanceSpine'
import { ReaderStateSummary } from './ReaderStateSummary'
import { ReaderEffectActions, ReaderReadingControls } from './ReaderInlineActions'
import { ReaderNoteEditor, type ReaderNoteKind } from './ReaderNotes'
import { ReaderTags } from './ReaderTags'
import {
  readerPlainTextExcerpt,
  safeReaderSourceHref,
  sourceHost,
  sourceLabel,
} from '@/lib/reader'
import { hasReaderCardInteraction, hasReaderCardSelection } from '@/lib/reader-navigation'
import type { ReaderItem } from '@/lib/types'

interface ReaderCardProps {
  item: ReaderItem
  onOpen: (fragmentId: string) => void
  onItemChange: (item: ReaderItem) => void
  mediaSlot?: ReactNode
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

type ReaderCardTab = 'content' | ReaderNoteKind

export function ReaderCard({ item, onOpen, onItemChange, mediaSlot }: ReaderCardProps) {
  const [activeTab, setActiveTab] = useState<ReaderCardTab>('content')
  const source = sourceLabel(item)
  const host = sourceHost(item)
  const published = publishedLabel(item.display.published_at)
  const summary = readerPlainTextExcerpt(item.display.summary.value)
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

      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] leading-4 text-text-subtle">
        <span className="font-medium text-text-soft">{source}</span>
        {host && <span>{host}</span>}
        {published && <time dateTime={item.display.published_at}>{published}</time>}
        <span>{item.capture_count === 1 ? 'Captured once' : `Captured ${item.capture_count} times`}</span>
      </div>

      <h2 className="mt-2 line-clamp-2 text-[18px] font-semibold leading-6 text-text sm:text-[19px]">
        {item.display.title.value || 'Untitled fragment'}
      </h2>

      <div className="mt-2">
        <ReaderReadingControls item={item} onItemChange={onItemChange} compact />
      </div>

      <div
        className="mt-4 flex items-center gap-1 border-b border-border-soft"
        role="tablist"
        aria-label={`Views for ${item.display.title.value || 'untitled fragment'}`}
        data-reader-nav-exclude
      >
        {(['content', 'curated', 'capture'] as const).map((tab) => (
          <button
            key={tab}
            type="button"
            role="tab"
            aria-selected={activeTab === tab}
            className={`min-h-9 border-b px-3 text-[12px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring ${
              activeTab === tab
                ? 'border-primary text-text'
                : 'border-transparent text-text-subtle hover:text-text'
            }`}
            onClick={(event) => {
              event.stopPropagation()
              setActiveTab(tab)
            }}
            onKeyDown={(event) => event.stopPropagation()}
          >
            {tab === 'content' ? 'Content' : tab === 'curated' ? 'Curated note' : 'Capture note'}
          </button>
        ))}
      </div>

      {activeTab === 'content' ? (
        <>
          <div className="mt-4 grid min-w-0 gap-5 lg:grid-cols-[minmax(0,1fr)_16rem] lg:items-start">
            <div className="min-w-0 flex-1">
          <p className="mt-2 line-clamp-3 max-w-[70ch] text-[14px] leading-[1.6] text-text-soft">
            {summary || 'No summary is available yet.'}
          </p>

          <p className="mt-2 text-[11px] leading-4 text-text-subtle">
            Title from {item.display.title.source}; summary from {item.display.summary.source}
          </p>

              <div className="mt-3">
                <ReaderTags item={item} onItemChange={onItemChange} compact />
              </div>
            </div>

            <div className="flex min-w-0 flex-col items-stretch gap-2 overflow-hidden">
              <div className="min-w-0 overflow-hidden" data-reader-media-slot data-reader-nav-exclude>
                {mediaSlot ?? <ReaderCardMediaSeam item={item} />}
              </div>
              <ReaderEffectActions item={item} onItemChange={onItemChange} includeMedia compact />
            </div>
          </div>

          <div className="mt-5 border-t border-border-soft pt-4">
            <ReaderStateSummary item={item} compact />
          </div>
        </>
      ) : (
        <div className="pt-4">
          <ReaderNoteEditor
            key={`${activeTab}-${activeTab === 'curated' ? item.curated_note?.revision ?? 0 : 'append'}`}
            item={item}
            onItemChange={onItemChange}
            kind={activeTab}
            compact
          />
        </div>
      )}

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
