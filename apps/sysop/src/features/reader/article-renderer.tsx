import { useEffect, useState } from 'react'
import { LoaderCircle } from 'lucide-react'
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
  const [content, setContent] = useState<{ href: string; html?: string; error?: string }>()

  useEffect(() => {
    if (presentation !== 'detail' || !contentHref) {
      return
    }
    const controller = new AbortController()
    void fetch(contentHref, {
      headers: { Accept: 'text/html' },
      signal: controller.signal,
      credentials: 'same-origin',
    }).then(async (response) => {
      if (!response.ok) throw new Error(`Readable content returned ${response.status}`)
      const contentType = response.headers.get('content-type') ?? ''
      if (!contentType.toLocaleLowerCase().startsWith('text/html')) {
        throw new Error('Readable content returned an unexpected format')
      }
      const body = await response.text()
      if (!controller.signal.aborted) setContent({ href: contentHref, html: body })
    }).catch((reason: unknown) => {
      if (controller.signal.aborted) return
      setContent({
        href: contentHref,
        error: reason instanceof Error ? reason.message : 'Readable content could not load',
      })
    })
    return () => controller.abort()
  }, [contentHref, presentation])
  const html = content && content.href === contentHref ? content.html : undefined
  const contentError = content && content.href === contentHref ? content.error : undefined

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

  if (!html && !contentError) {
    return (
      <div className="flex min-h-48 items-center justify-center text-text-subtle" role="status" aria-label="Loading readable content">
        <LoaderCircle className="h-5 w-5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
      </div>
    )
  }

  if (contentError) {
    return (
      <section className={cn('mx-auto w-full max-w-[78ch]', className)} data-reader-renderer="article" data-reader-presentation="detail">
        <p className="text-base leading-7 text-text-muted">{preview || 'No readable article content was captured.'}</p>
        <p className="mt-4 text-xs leading-5 text-danger-soft" role="status">{contentError}</p>
      </section>
    )
  }

  return (
    <section
      className={cn('mx-auto w-full max-w-[78ch]', className)}
      data-reader-renderer="article"
      data-reader-presentation="detail"
    >
      <div
        className="reader-article-content text-text-muted"
        data-reader-sanitized-content
        // This same-origin resource is rendered and allowlist-sanitized by ReaderResourceService.
        dangerouslySetInnerHTML={{ __html: html ?? '' }}
      />
    </section>
  )
}
