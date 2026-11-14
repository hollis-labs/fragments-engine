import { useRef, useState } from 'react'
import { Images } from 'lucide-react'
import { cn } from '@hollis-labs/sysop-ui'

import { readerControlClass, stopReaderNavigation } from './interaction'
import {
  articleImages,
  availableVariantHref,
  featuredArticleImage,
  imageVariants,
  mediaState,
} from './media'
import { MediaDialog } from './media-dialog'
import { ResourceStatePanel } from './resource-state'
import type { ReaderMediaItem, ReaderRenderableItem } from './types'

function imageSrc(media: ReaderMediaItem): string | undefined {
  const variants = imageVariants(media)
  return (
    (variants.large && availableVariantHref(variants.large)) ||
    (variants.preview && availableVariantHref(variants.preview)) ||
    undefined
  )
}

function imageAlt(media: ReaderMediaItem, fallback: string): string {
  return media.alt_text?.trim() || media.attachment.caption?.trim() || fallback
}

/**
 * The featured image at the top of an article, with a gallery icon showing
 * the image count that opens every image of the item in the media dialog.
 */
export function ArticleFeaturedImage({
  item,
  className,
}: {
  item: ReaderRenderableItem
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const featured = featuredArticleImage(item)
  if (!featured) return null

  const title = item.display.title.value.trim() || 'Captured article'
  const images = articleImages(item.media)
  const count = item.display.image_count ?? images.length
  const src = imageSrc(featured)

  const featuredState = mediaState(featured)

  return (
    <figure
      className={cn('relative min-w-0 overflow-hidden rounded-sm border border-border bg-panel-2', className)}
      data-reader-featured-image
    >
      {src ? (
        <img
          src={src}
          alt={imageAlt(featured, `${title} featured image`)}
          loading="lazy"
          className="max-h-[64vh] w-full object-cover"
        />
      ) : (
        // The gallery control below stays available so the item's other
        // images remain reachable when the featured one is not.
        <ResourceStatePanel
          className="min-h-24 w-full"
          state={featuredState === 'available' ? 'unavailable' : featuredState}
          compact
          label="Featured image"
        />
      )}
      {count > 1 && (
        <>
          <button
            ref={triggerRef}
            type="button"
            className={`${readerControlClass} absolute bottom-2 right-2 inline-flex min-h-11 items-center gap-2 rounded-md border border-border-strong bg-panel-overlay-strong/95 px-3 text-xs font-medium text-text`}
            data-reader-gallery-trigger
            aria-label={`Open gallery of ${count} images`}
            onClick={(event) => {
              stopReaderNavigation(event)
              setOpen(true)
            }}
            onKeyDown={stopReaderNavigation}
          >
            <Images className="h-4 w-4" aria-hidden="true" />
            <span>{count}</span>
          </button>
          <MediaDialog
            open={open}
            onOpenChange={setOpen}
            returnFocusRef={triggerRef}
            title={title}
            description={`All ${count} images`}
          >
            <div className="flex max-h-[calc(100dvh-6rem)] min-h-0 flex-col gap-3 overflow-auto bg-bg p-3">
              {images.map((image, index) => {
                const href = imageSrc(image)
                const state = mediaState(image)
                return href ? (
                  <img
                    key={image.attachment.attachment_id}
                    src={href}
                    alt={imageAlt(image, `${title} image ${index + 1}`)}
                    loading="lazy"
                    className="max-w-full object-contain"
                  />
                ) : (
                  <ResourceStatePanel
                    key={image.attachment.attachment_id}
                    state={state === 'available' ? 'unavailable' : state}
                    compact
                    label={`Image ${index + 1}`}
                  />
                )
              })}
            </div>
          </MediaDialog>
        </>
      )}
    </figure>
  )
}
