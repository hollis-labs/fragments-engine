import { cn } from '@hollis-labs/sysop-ui'
import { readerPlainTextExcerpt } from '@/lib/reader'

import { serverResourceHref } from './media'
import type { ReaderRendererProps } from './types'

function articlePreview(item: ReaderRendererProps['item'], limit: number): string {
  return readerPlainTextExcerpt(
    item.article.preview_markdown.trim() || item.display.summary.value.trim(),
    limit,
  )
}

export function ArticleRenderer({ item, presentation, className }: ReaderRendererProps) {
  const preview = articlePreview(item, presentation === 'card' ? 480 : 2400)
  const contentHref = item.article.full_content_available
    ? serverResourceHref(item.article.full_content_href, 'article')
    : undefined

  if (presentation === 'card') {
    return (
      <div
        className={cn('min-w-0', className)}
        data-reader-renderer="article"
        data-reader-presentation="card"
      >
        <p className="line-clamp-5 text-sm leading-6 text-text-muted">
          {preview || 'No article preview was captured.'}
        </p>
        {item.article.full_content_available && (
          <p className="mt-3 text-xs leading-5 text-text-subtle">
            {contentHref ? 'Full article available' : 'Full article resource unavailable'}
          </p>
        )}
      </div>
    )
  }

  if (!contentHref) {
    return (
      <section
        className={cn('mx-auto w-full max-w-[78ch]', className)}
        data-reader-renderer="article"
        data-reader-presentation="detail"
      >
        <p className="text-base leading-7 text-text-muted">
          {preview || 'No readable article content was captured.'}
        </p>
        {item.article.full_content_available && (
          <p className="mt-4 text-xs leading-5 text-text-subtle" role="status">
            The full article resource is not available from this Reader location.
          </p>
        )}
      </section>
    )
  }

  return (
    <section
      className={cn('mx-auto w-full max-w-[78ch]', className)}
      data-reader-renderer="article"
      data-reader-presentation="detail"
    >
      <iframe
        className="reader-frame min-h-[32rem] w-full rounded-md border border-border bg-panel-2 sm:h-[min(72vh,56rem)]"
        src={contentHref}
        title={`Article: ${item.display.title.value || 'Untitled item'}`}
        sandbox=""
        referrerPolicy="no-referrer"
        loading="lazy"
      />
    </section>
  )
}
