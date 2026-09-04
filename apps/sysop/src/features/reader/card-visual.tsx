import { useRef, useState } from 'react'
import { ImageIcon, Images, Play, Video } from 'lucide-react'
import { cn } from '@hollis-labs/sysop-ui'

import { readerControlClass, stopReaderNavigation } from './interaction'
import {
  availableVariantHref,
  imageVariants,
  mediaState,
  orderedMedia,
  trustedYouTubeEmbedURL,
  type ResourceState,
} from './media'
import { MediaDialog } from './media-dialog'
import { ResourceStatePanel } from './resource-state'
import type { ReaderMediaItem, ReaderRenderableItem } from './types'

const GALLERY_ROLES = new Set(['primary', 'gallery_item', 'hero', 'inline'])

interface CardVisual {
  href?: string
  largeHref?: string
  alt: string
  label: string
  state: ResourceState
}

function imageHref(media: ReaderMediaItem): string | undefined {
  const preview = imageVariants(media).preview
  return preview && availableVariantHref(preview)
}

function posterHref(media: ReaderMediaItem): string | undefined {
  for (const kind of ['poster', 'preview', 'thumbnail'] as const) {
    const variant = media.variants.find((candidate) => candidate.kind === kind)
    const href = variant && availableVariantHref(variant)
    if (href) return href
  }
  return undefined
}

function visualAlt(media: ReaderMediaItem, fallback: string): string {
  return media.alt_text?.trim() || media.attachment.caption?.trim() || fallback
}

function unavailableState(media: ReaderMediaItem[]): ResourceState {
  const states = media.map(mediaState)
  if (states.includes('pending')) return 'pending'
  if (states.includes('failed')) return 'failed'
  if (states.includes('reference_only')) return 'reference_only'
  return 'unavailable'
}

function selectCardVisual(item: ReaderRenderableItem): CardVisual | undefined {
  const media = orderedMedia(item.media)
  const title = item.display.title.value.trim() || 'Captured item'

  if (item.renderer === 'image') {
    const candidate = media.find((entry) => entry.kind === 'image')
    if (!candidate) {
      return { alt: '', label: 'Image', state: 'unavailable' }
    }
    const variants = imageVariants(candidate)
    const href = variants.preview && availableVariantHref(variants.preview)
    const largeHref = variants.large && availableVariantHref(variants.large)
    return {
      href,
      largeHref,
      alt: visualAlt(candidate, `${title} image`),
      label: 'Image',
      state: href ? 'available' : unavailableState([candidate]),
    }
  }

  if (item.renderer === 'gallery') {
    const candidates = media.filter(
      (entry) =>
        (entry.kind === 'image' || entry.kind === 'video') &&
        GALLERY_ROLES.has(entry.attachment.role),
    )
    const available = candidates
      .map((candidate) => ({
        media: candidate,
        href: candidate.kind === 'image' ? imageHref(candidate) : posterHref(candidate),
        largeHref:
          candidate.kind === 'image'
            ? (() => {
                const large = imageVariants(candidate).large
                return large && availableVariantHref(large)
              })()
            : posterHref(candidate),
      }))
      .find((candidate) => candidate.href)
    const selected = available?.media ?? candidates[0]
    const countLabel = candidates.length === 1 ? '1 item' : `${candidates.length} items`
    return {
      href: available?.href,
      largeHref: available?.largeHref,
      alt: selected ? visualAlt(selected, `${title} gallery preview`) : '',
      label: `Gallery · ${countLabel}`,
      state: available?.href ? 'available' : unavailableState(candidates),
    }
  }

  if (item.renderer === 'video') {
    const videos = media.filter((entry) => entry.kind === 'video')
    const videoPoster = videos
      .map((candidate) => ({ media: candidate, href: posterHref(candidate) }))
      .find((candidate) => candidate.href)
    const posterImage = media
      .filter((entry) => entry.kind === 'image' && entry.attachment.role === 'poster')
      .map((candidate) => ({ media: candidate, href: imageHref(candidate) }))
      .find((candidate) => candidate.href)
    const selected = videoPoster ?? posterImage
    return {
      href: selected?.href,
      alt: selected ? visualAlt(selected.media, `${title} poster`) : '',
      label: 'Video poster',
      state: selected?.href ? 'available' : unavailableState(videos),
    }
  }

  return undefined
}

function VisualIcon({ renderer }: { renderer: string }) {
  const className = 'h-3.5 w-3.5'
  if (renderer === 'gallery') return <Images className={className} aria-hidden="true" />
  if (renderer === 'video') return <Video className={className} aria-hidden="true" />
  return <ImageIcon className={className} aria-hidden="true" />
}

/** An authorized card preview that opens media without navigating away from Reader. */
export function ReaderCardVisual({
  item,
  className,
}: {
  item: ReaderRenderableItem
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const visual = selectCardVisual(item)
  if (!visual) return null

  if (!visual.href) {
    const state = visual.state === 'available' ? 'unavailable' : visual.state
    return (
      <ResourceStatePanel
        className={cn('min-h-24 w-full', className)}
        state={state}
        compact
        label={state === 'reference_only' ? 'No captured visual' : visual.label}
      />
    )
  }

  const embedURL = item.renderer === 'video' ? trustedYouTubeEmbedURL(item.playback) : undefined
  const dialogHref = visual.largeHref ?? visual.href
  const dialogTitle = item.display.title.value || (item.renderer === 'video' ? 'Captured video' : 'Captured image')

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={cn(
          readerControlClass,
          'group relative block aspect-[16/10] max-h-40 w-full overflow-hidden rounded-sm border border-border bg-panel-2 text-left',
          className,
        )}
        data-reader-card-visual
        data-reader-nav-exclude
        aria-label={item.renderer === 'video' ? `Play video: ${dialogTitle}` : `View larger image: ${dialogTitle}`}
        onClick={(event) => {
          stopReaderNavigation(event)
          setOpen(true)
        }}
        onKeyDown={stopReaderNavigation}
      >
        <img
          src={visual.href}
          alt={visual.alt}
          loading="lazy"
          className="pointer-events-none h-full w-full object-cover transition-opacity group-hover:opacity-90 motion-reduce:transition-none"
        />
        <span className="absolute inset-x-0 bottom-0 flex items-center gap-1.5 bg-panel-overlay-strong/95 px-2.5 py-1.5 text-[11px] font-medium text-text">
          {item.renderer === 'video' ? <Play className="h-3.5 w-3.5" aria-hidden="true" /> : <VisualIcon renderer={item.renderer} />}
          {item.renderer === 'video' ? 'Play video' : visual.label}
        </span>
      </button>

      <MediaDialog
        open={open}
        onOpenChange={setOpen}
        returnFocusRef={triggerRef}
        title={dialogTitle}
        description={item.renderer === 'video' ? 'Video player' : 'Larger image preview'}
        minimal
      >
        <div className="flex max-h-[calc(100dvh-1rem)] min-h-0 items-center justify-center overflow-auto bg-bg">
          {embedURL ? (
            <iframe
              className="reader-frame aspect-video max-h-[calc(100dvh-1rem)] w-full border-0 bg-bg"
              src={embedURL}
              title={`YouTube video: ${dialogTitle}`}
              sandbox="allow-scripts allow-same-origin allow-presentation"
              allow="encrypted-media; picture-in-picture; fullscreen"
              allowFullScreen
              referrerPolicy="strict-origin-when-cross-origin"
            />
          ) : (
            <img
              src={dialogHref}
              alt={visual.alt}
              className="max-h-[calc(100dvh-2rem)] max-w-full object-contain"
            />
          )}
        </div>
      </MediaDialog>
    </>
  )
}
