import type { ReactNode } from 'react'

import { ArticleRenderer } from './article-renderer'
import { AudioRenderer, DocumentRenderer, TextRenderer, UnknownRenderer } from './fallback-renderer'
import { GalleryRenderer } from './gallery-renderer'
import { ImageRenderer } from './image-renderer'
import type { ReaderRendererProps } from './types'
import { VideoRenderer } from './video-renderer'

function MediaArticleDetail({
  props,
  children,
}: {
  props: ReaderRendererProps
  children: ReactNode
}) {
  const hasArticle = props.item.article.full_content_available ||
    props.item.article.preview_markdown.trim() !== ''
  return (
    <>
      {children}
      {props.presentation === 'detail' && hasArticle && (
        <div className="mt-5 border-t border-border pt-5">
          <h3 className="mb-3 text-sm font-semibold leading-6 text-text">Captured page</h3>
          <ArticleRenderer item={props.item} presentation="detail" />
        </div>
      )}
    </>
  )
}

export function ReaderContentRenderer(props: ReaderRendererProps) {
  switch (props.item.renderer) {
    case 'article':
      return <ArticleRenderer {...props} />
    case 'image':
      return <MediaArticleDetail props={props}><ImageRenderer {...props} /></MediaArticleDetail>
    case 'gallery':
      return <MediaArticleDetail props={props}><GalleryRenderer {...props} /></MediaArticleDetail>
    case 'video':
      return <VideoRenderer {...props} />
    case 'audio':
      return <AudioRenderer {...props} />
    case 'document':
      return <DocumentRenderer {...props} />
    case 'text':
      return <TextRenderer {...props} />
    default:
      return <UnknownRenderer {...props} />
  }
}
