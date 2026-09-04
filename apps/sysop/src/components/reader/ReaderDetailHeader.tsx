import type { ReactNode } from 'react'
import { ArrowLeft } from 'lucide-react'

interface ReaderDetailHeaderProps {
  title: string
  readingState?: string
  onBack: () => void
  actions: ReactNode
}

export function ReaderDetailHeader({
  title,
  readingState,
  onBack,
  actions,
}: ReaderDetailHeaderProps) {
  return (
    <header className="border-b border-border-strong bg-bg">
      <div className="border-b border-border-soft px-4 py-1.5 sm:px-6">
        <button
          type="button"
          onClick={onBack}
          className="inline-flex min-h-11 items-center gap-1.5 rounded-sm text-[13px] font-medium text-text-subtle outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-ring sm:min-h-8"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
          Reader
        </button>
      </div>
      <div className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-start sm:justify-between sm:px-6">
        <div className="min-w-0">
          <h1 className="line-clamp-2 text-[20px] font-semibold leading-tight text-text sm:text-[22px]">
            {title}
          </h1>
          {readingState && <p className="mt-1 text-[12px] text-text-subtle">{readingState}</p>}
        </div>
        <div className="flex flex-wrap items-center gap-2 sm:shrink-0 sm:justify-end">{actions}</div>
      </div>
    </header>
  )
}
