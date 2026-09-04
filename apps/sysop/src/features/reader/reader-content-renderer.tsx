import { ArticleRenderer } from './article-renderer'
import { AudioRenderer, DocumentRenderer, TextRenderer, UnknownRenderer } from './fallback-renderer'
import { GalleryRenderer } from './gallery-renderer'
import { ImageRenderer } from './image-renderer'
import type { ReaderRendererProps } from './types'
import { VideoRenderer } from './video-renderer'

export function ReaderContentRenderer(props: ReaderRendererProps) {
  switch (props.item.renderer) {
    case 'article':
      return <ArticleRenderer {...props} />
    case 'image':
      return <ImageRenderer {...props} />
    case 'gallery':
      return <GalleryRenderer {...props} />
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
