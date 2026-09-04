import { cn } from '@hollis-labs/sysop-ui'

import { serverResourceHref } from './media'
import type { ReaderRendererProps } from './types'

function articlePreview(item: ReaderRendererProps['item']): string {
  return item.article.preview_markdown.trim() || item.display.summary.value.trim()
}

export function ArticleRenderer({ item, presentation, className }: ReaderRendererProps) {
  const preview = articlePreview(item)
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
        <p className="line-clamp-6 whitespace-pre-wrap text-sm leading-6 text-text-muted">
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
        className={cn('mx-auto w-full max-w-[72ch]', className)}
        data-reader-renderer="article"
        data-reader-presentation="detail"
      >
        <p className="whitespace-pre-wrap text-base leading-7 text-text-muted">
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
      className={cn('w-full', className)}
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
