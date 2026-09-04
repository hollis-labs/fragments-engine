import { ImageIcon, Images, Video } from 'lucide-react'
import { cn } from '@hollis-labs/sysop-ui'

import {
  availableVariantHref,
  imageVariants,
  mediaState,
  orderedMedia,
  type ResourceState,
} from './media'
import { ResourceStatePanel } from './resource-state'
import type { ReaderMediaItem, ReaderRenderableItem } from './types'

const GALLERY_ROLES = new Set(['primary', 'gallery_item', 'hero', 'inline'])

interface CardVisual {
  href?: string
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
    const href = imageHref(candidate)
    return {
      href,
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
      }))
      .find((candidate) => candidate.href)
    const selected = available?.media ?? candidates[0]
    const countLabel = candidates.length === 1 ? '1 item' : `${candidates.length} items`
    return {
      href: available?.href,
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

/** A non-interactive, authorized visual summary for Reader list cards. */
export function ReaderCardVisual({
  item,
  className,
}: {
  item: ReaderRenderableItem
  className?: string
}) {
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

  return (
    <figure
      className={cn(
        'relative aspect-[16/10] max-h-40 w-full overflow-hidden rounded-sm border border-border bg-panel-2',
        className,
      )}
      data-reader-card-visual
      aria-label={visual.label}
    >
      <img
        src={visual.href}
        alt={visual.alt}
        loading="lazy"
        className="pointer-events-none h-full w-full object-cover"
      />
      <figcaption className="absolute inset-x-0 bottom-0 flex items-center gap-1.5 bg-panel-overlay-strong/95 px-2.5 py-1.5 text-[11px] font-medium text-text">
        <VisualIcon renderer={item.renderer} />
        {visual.label}
      </figcaption>
    </figure>
  )
}
