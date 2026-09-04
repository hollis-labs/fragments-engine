import type { ReactNode } from 'react'
import { ArrowLeft, ChevronLeft, ChevronRight } from 'lucide-react'

interface ReaderDetailHeaderProps {
  title: string
  readingState?: ReactNode
  onBack: () => void
  onPrevious?: () => void
  onNext?: () => void
  hasPrevious?: boolean
  hasNext?: boolean
  actions: ReactNode
}

export function ReaderDetailHeader({
  title,
  readingState,
  onBack,
  onPrevious,
  onNext,
  hasPrevious = false,
  hasNext = false,
  actions,
}: ReaderDetailHeaderProps) {
  return (
    <header className="border-b border-border-strong bg-bg">
      <div className="flex items-center justify-between border-b border-border-soft px-4 py-1.5 sm:px-6">
        <button
          type="button"
          onClick={onBack}
          aria-label="Back to Reader inbox"
          className="inline-flex min-h-11 items-center gap-1.5 rounded-sm text-[13px] font-medium text-text-subtle outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-ring sm:min-h-8"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
          Reader
        </button>
        <nav className="flex items-center gap-1" aria-label="Inbox item navigation">
          <button
            type="button"
            onClick={onPrevious}
            disabled={!hasPrevious}
            className="inline-flex h-9 w-9 items-center justify-center rounded-full text-text-subtle outline-none hover:bg-panel-hover hover:text-text focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:opacity-30"
            aria-label="Previous inbox item"
            title="Previous inbox item (Left arrow)"
          >
            <ChevronLeft className="h-4 w-4" aria-hidden="true" />
          </button>
          <button
            type="button"
            onClick={onNext}
            disabled={!hasNext}
            className="inline-flex h-9 w-9 items-center justify-center rounded-full text-text-subtle outline-none hover:bg-panel-hover hover:text-text focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default disabled:opacity-30"
            aria-label="Next inbox item"
            title="Next inbox item (Right arrow)"
          >
            <ChevronRight className="h-4 w-4" aria-hidden="true" />
          </button>
        </nav>
      </div>
      <div className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-start sm:justify-between sm:px-6">
        <div className="min-w-0">
          <h1 className="line-clamp-2 text-[20px] font-semibold leading-tight text-text sm:text-[22px]">
            {title}
          </h1>
          {readingState && <div className="mt-1 text-[12px] text-text-subtle">{readingState}</div>}
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:shrink-0 sm:justify-end">{actions}</div>
      </div>
    </header>
  )
}
