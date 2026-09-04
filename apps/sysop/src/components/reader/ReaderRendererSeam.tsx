import {
  AlignLeft,
  AudioLines,
  CircleHelp,
  File,
  FileText,
  GalleryHorizontal,
  Image,
  Video,
} from 'lucide-react'
import { acquisitionPresentation } from '@/lib/reader'
import type { ReaderItem, ReaderRenderer } from '@/lib/types'

const RENDERER_LABELS: Record<ReaderRenderer, string> = {
  article: 'Article',
  image: 'Image',
  gallery: 'Gallery',
  video: 'Video',
  audio: 'Audio',
  document: 'Document',
  text: 'Text',
  unknown: 'Fragment',
}

function RendererIcon({ renderer, className }: { renderer: ReaderRenderer; className: string }) {
  const props = { className, 'aria-hidden': true as const }
  switch (renderer) {
    case 'article':
      return <FileText {...props} />
    case 'image':
      return <Image {...props} />
    case 'gallery':
      return <GalleryHorizontal {...props} />
    case 'video':
      return <Video {...props} />
    case 'audio':
      return <AudioLines {...props} />
    case 'document':
      return <File {...props} />
    case 'text':
      return <AlignLeft {...props} />
    case 'unknown':
      return <CircleHelp {...props} />
  }
}

export function ReaderCardMediaSeam({ item }: { item: ReaderItem }) {
  const mediaState = acquisitionPresentation(item.operations.acquisition)
  return (
    <div
      className="flex min-w-[8.5rem] items-center gap-2 rounded-sm border border-border-soft bg-panel-2/35 px-3 py-2"
      data-reader-media-slot
      data-reader-nav-exclude
    >
      <RendererIcon renderer={item.renderer} className="h-4 w-4 shrink-0 text-text-soft" />
      <div className="min-w-0">
        <p className="text-[12px] font-medium leading-4 text-text-muted">
          {RENDERER_LABELS[item.renderer]}
        </p>
        <p className="truncate text-[11px] leading-4 text-text-subtle">{mediaState.value}</p>
      </div>
    </div>
  )
}

export function ReaderRendererPlaceholder({ item }: { item: ReaderItem }) {
  const mediaState = acquisitionPresentation(item.operations.acquisition)
  const hasContent = item.article.full_content_available || item.article.preview_markdown.trim() !== ''
  const description =
    item.media.length > 0
      ? `${item.media.length} media ${item.media.length === 1 ? 'item' : 'items'} · ${mediaState.value}`
      : hasContent
        ? item.article.full_content_available
          ? 'Full text is available.'
          : 'A bounded text preview is available.'
        : 'No renderable content is available yet.'

  return (
    <section
      className="flex min-h-44 items-center gap-5 border-y border-border-soft bg-panel-2/20 px-5 py-8 sm:px-7"
      aria-label={`${RENDERER_LABELS[item.renderer]} content`}
      data-reader-renderer-slot
    >
      <div className="flex h-12 w-12 shrink-0 items-center justify-center rounded-sm border border-border bg-bg">
        <RendererIcon renderer={item.renderer} className="h-5 w-5 text-text-soft" />
      </div>
      <div>
        <h2 className="text-[16px] font-semibold text-text">
          {RENDERER_LABELS[item.renderer]} content
        </h2>
        <p className="mt-1 max-w-xl text-[13px] leading-5 text-text-soft">{description}</p>
      </div>
    </section>
  )
}
