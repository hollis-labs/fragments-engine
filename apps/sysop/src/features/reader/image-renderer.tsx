import { useRef, useState } from 'react'
import { Expand } from 'lucide-react'
import { cn } from '@hollis-labs/sysop-ui'

import { readerControlClass, stopReaderNavigation } from './interaction'
import { ReaderCardVisual } from './card-visual'
import { availableVariantHref, imageVariants, mediaState } from './media'
import { MediaDialog } from './media-dialog'
import { ResourceStatePanel } from './resource-state'
import type { ReaderMediaItem, ReaderRendererProps } from './types'

function imageAlt(media: ReaderMediaItem, title: string): string {
  return (
    media.alt_text?.trim() ||
    media.attachment.caption?.trim() ||
    (title.trim() ? `${title} image` : 'Captured image')
  )
}

export function ImageRenderer({ item, presentation, className }: ReaderRendererProps) {
  const media = item.media.find((candidate) => candidate.kind === 'image')
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)

  if (presentation === 'card') return <ReaderCardVisual item={item} className={className} />

  if (!media) {
    return (
      <ResourceStatePanel
        className={className}
        state="unavailable"
        label="No image was captured"
      />
    )
  }

  const state = mediaState(media)
  const variants = imageVariants(media)
  const previewHref = variants.preview && availableVariantHref(variants.preview)
  const largeHref = variants.large && availableVariantHref(variants.large)
  const alt = imageAlt(media, item.display.title.value)
  const caption = media.attachment.caption?.trim()

  if (!previewHref || !largeHref) {
    return (
      <ResourceStatePanel
        className={className}
        state={state === 'available' ? 'unavailable' : state}
        label="Image is not available"
      />
    )
  }

  return (
    <div
      className={cn('min-w-0', className)}
      data-reader-renderer="image"
      data-reader-presentation={presentation}
    >
      <button
        ref={triggerRef}
        type="button"
        className={`${readerControlClass} group relative block w-full overflow-hidden border border-border bg-panel-2 text-left`}
        onClick={(event) => {
          stopReaderNavigation(event)
          setOpen(true)
        }}
        onKeyDown={stopReaderNavigation}
        aria-label={`View larger image: ${alt}`}
      >
        <span
          className="flex max-h-[68vh] min-h-64 w-full items-center justify-center overflow-hidden"
        >
          <img
            src={previewHref}
            alt={alt}
            loading="lazy"
            className={cn(
              'reader-motion h-full w-full object-contain transition-opacity group-hover:opacity-90 motion-reduce:transition-none',
              presentation === 'detail' && 'max-h-[68vh]',
            )}
          />
        </span>
        <span className="absolute bottom-2 right-2 inline-flex min-h-11 items-center gap-2 rounded-md border border-border-strong bg-panel-overlay-strong/95 px-3 text-xs font-medium text-text">
          <Expand className="h-4 w-4" aria-hidden="true" />
          View larger
        </span>
      </button>
      {caption && <p className="mt-2 text-xs leading-5 text-text-subtle">{caption}</p>}

      <MediaDialog
        open={open}
        onOpenChange={setOpen}
        returnFocusRef={triggerRef}
        title={item.display.title.value || 'Captured image'}
        description={caption || 'Larger authorized image representation'}
      >
        <div className="flex max-h-[calc(100dvh-6rem)] min-h-0 items-center justify-center overflow-auto bg-bg p-3 sm:p-6">
          <img
            src={largeHref}
            alt={alt}
            className="max-h-[calc(100dvh-9rem)] max-w-full object-contain"
          />
        </div>
      </MediaDialog>
    </div>
  )
}
