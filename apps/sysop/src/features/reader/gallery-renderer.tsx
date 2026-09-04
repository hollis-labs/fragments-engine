import { useRef, useState, type KeyboardEvent } from 'react'
import { ChevronLeft, ChevronRight, Expand } from 'lucide-react'
import { Button, cn } from '@hollis-labs/sysop-ui'

import { readerControlClass, stopReaderNavigation } from './interaction'
import { ReaderCardVisual } from './card-visual'
import {
  availableVariantHref,
  imageVariants,
  mediaState,
  orderedMedia,
  stateLabel,
  type ResourceState,
} from './media'
import { MediaDialog } from './media-dialog'
import { ResourceStatePanel } from './resource-state'
import type { ReaderMediaItem, ReaderRendererProps } from './types'

interface GallerySlot {
  media: ReaderMediaItem
  previewHref?: string
  largeHref?: string
}

function slotKind(slot: GallerySlot): 'Image' | 'Video' {
  return slot.media.kind === 'video' ? 'Video' : 'Image'
}

function slotState(slot: GallerySlot): ResourceState {
  if (slot.media.kind === 'image') return mediaState(slot.media)

  const originals = slot.media.variants.filter((variant) => variant.kind === 'original')
  if (originals.some((variant) => availableVariantHref(variant) !== undefined)) return 'available'
  if (originals.some((variant) => variant.acquisition_state === 'pending')) return 'pending'
  if (originals.some((variant) => variant.acquisition_state === 'failed')) return 'failed'
  if (originals.some((variant) => variant.acquisition_state === 'reference_only')) return 'reference_only'
  return 'unavailable'
}

function slotAlt(slot: GallerySlot, title: string, index: number): string {
  return (
    slot.media.alt_text?.trim() ||
    slot.media.attachment.caption?.trim() ||
    `${title.trim() || 'Captured gallery'} ${slot.media.kind === 'video' ? 'video poster' : 'image'} ${index + 1}`
  )
}

function gallerySlots(media: ReaderMediaItem[]): GallerySlot[] {
  return orderedMedia(media).filter((item) =>
    (item.kind === 'image' || item.kind === 'video') &&
    ['primary', 'gallery_item', 'hero', 'inline'].includes(item.attachment.role),
  ).map((item) => {
    const variants = item.kind === 'image'
      ? imageVariants(item)
      : {
          preview: item.variants.find(
            (variant) =>
              ['poster', 'preview', 'thumbnail'].includes(variant.kind) &&
              availableVariantHref(variant) !== undefined,
          ),
          large: item.variants.find(
            (variant) =>
              ['poster', 'preview', 'thumbnail'].includes(variant.kind) &&
              availableVariantHref(variant) !== undefined,
          ),
        }
    return {
      media: item,
      previewHref: variants.preview && availableVariantHref(variants.preview),
      largeHref: variants.large && availableVariantHref(variants.large),
    }
  })
}

export function GalleryRenderer({ item, presentation, className }: ReaderRendererProps) {
  const slots = gallerySlots(item.media)
  const [selected, setSelected] = useState(0)
  const [open, setOpen] = useState(false)
  const triggerRef = useRef<HTMLButtonElement>(null)

  if (presentation === 'card') return <ReaderCardVisual item={item} className={className} />

  if (slots.length === 0) {
    return (
      <ResourceStatePanel
        className={className}
        state="unavailable"
        label="No gallery items were captured"
      />
    )
  }

  const boundedSelected = Math.min(selected, slots.length - 1)
  const current = slots[boundedSelected]
  const currentState = slotState(current)
  const currentAlt = slotAlt(current, item.display.title.value, boundedSelected)
  const currentKind = slotKind(current)

  function moveTo(index: number): void {
    setSelected(Math.max(0, Math.min(index, slots.length - 1)))
  }

  function handleDialogKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (event.key === 'ArrowLeft') {
      event.preventDefault()
      moveTo(boundedSelected - 1)
    } else if (event.key === 'ArrowRight') {
      event.preventDefault()
      moveTo(boundedSelected + 1)
    } else if (event.key === 'Home') {
      event.preventDefault()
      moveTo(0)
    } else if (event.key === 'End') {
      event.preventDefault()
      moveTo(slots.length - 1)
    }
  }

  const stage = current.largeHref ?? current.previewHref

  return (
    <div
      className={cn('min-w-0', className)}
      data-reader-renderer="gallery"
      data-reader-presentation={presentation}
    >
      <div className="overflow-hidden rounded-md border border-border bg-panel-2">
        {stage ? (
          <button
            ref={triggerRef}
            type="button"
            className={`${readerControlClass} group relative flex min-h-72 max-h-[64vh] w-full items-center justify-center overflow-hidden rounded-none text-left`}
            onClick={(event) => {
              stopReaderNavigation(event)
              setOpen(true)
            }}
            onKeyDown={stopReaderNavigation}
            aria-label={`Open gallery at item ${boundedSelected + 1} of ${slots.length}`}
          >
            <img
              src={stage}
              alt={currentAlt}
              loading="lazy"
              className="reader-motion h-full max-h-[64vh] w-full object-contain transition-opacity group-hover:opacity-90 motion-reduce:transition-none"
            />
            <span className="absolute bottom-2 right-2 inline-flex min-h-11 items-center gap-2 rounded-md border border-border-strong bg-panel-overlay-strong/95 px-3 text-xs font-medium text-text">
              <Expand className="h-4 w-4" aria-hidden="true" />
              View gallery
            </span>
            <span className="absolute left-2 top-2 rounded bg-panel-overlay-strong/95 px-2 py-1 text-[10px] font-medium uppercase tracking-wide text-text">
              {currentKind}
            </span>
          </button>
        ) : (
          <ResourceStatePanel
            state={currentState === 'available' ? 'unavailable' : currentState}
            className="m-4 min-h-56"
            label={`${currentKind} ${boundedSelected + 1} is ${stateLabel(currentState).toLowerCase()}`}
          />
        )}
      </div>

      <div className="mt-2 flex items-center justify-between gap-3">
        <p className="font-mono text-xs tabular-nums text-text-soft" aria-live="polite">
          {boundedSelected + 1} of {slots.length}
        </p>
        <p className="truncate text-xs text-text-subtle">
          {current.media.attachment.caption || `${currentKind} · ${stateLabel(currentState)}`}
        </p>
      </div>

      <ol
        className="mt-3 flex snap-x gap-2 overflow-x-auto pb-1"
        aria-label="Gallery items in source order"
      >
        {slots.map((slot, index) => {
          const selectedSlot = index === boundedSelected
          const state = slotState(slot)
          const kind = slotKind(slot)
          return (
            <li key={slot.media.attachment.attachment_id} className="shrink-0 snap-start">
              <button
                type="button"
                className={`${readerControlClass} relative h-16 w-20 overflow-hidden border bg-panel-2 text-left sm:h-20 sm:w-24 ${
                  selectedSlot ? 'border-primary' : 'border-border hover:border-border-strong'
                }`}
                onClick={(event) => {
                  stopReaderNavigation(event)
                  moveTo(index)
                }}
                onKeyDown={stopReaderNavigation}
                aria-label={`Select item ${index + 1} of ${slots.length}: ${kind}, ${stateLabel(state)}`}
                aria-current={selectedSlot ? 'true' : undefined}
              >
                {slot.previewHref ? (
                  <img
                    src={slot.previewHref}
                    alt=""
                    aria-hidden="true"
                    loading="lazy"
                    className="h-full w-full object-cover"
                  />
                ) : (
                  <span className="flex h-full items-center justify-center px-1 text-center text-[10px] leading-4 text-text-subtle">
                    {stateLabel(state)}
                  </span>
                )}
                <span className="absolute left-1 top-1 rounded bg-panel-overlay-strong/95 px-1.5 py-0.5 font-mono text-[10px] tabular-nums text-text">
                  {index + 1}
                </span>
                {slot.media.kind === 'video' && (
                  <span className="absolute bottom-1 right-1 rounded bg-panel-overlay-strong/95 px-1.5 py-0.5 text-[9px] font-medium uppercase tracking-wide text-text">
                    Video
                  </span>
                )}
              </button>
            </li>
          )
        })}
      </ol>

      <MediaDialog
        open={open}
        onOpenChange={setOpen}
        returnFocusRef={triggerRef}
        title={item.display.title.value || 'Captured gallery'}
        description={`${currentKind} ${boundedSelected + 1} of ${slots.length}`}
        onKeyDown={handleDialogKeyDown}
      >
        <div className="grid min-h-0 grid-rows-[1fr_auto] bg-bg">
          <div className="flex min-h-0 items-center justify-center overflow-auto p-3 sm:p-6">
            {stage ? (
              <img
                src={stage}
                alt={currentAlt}
                className="max-h-[calc(100dvh-14rem)] max-w-full object-contain"
              />
            ) : (
              <ResourceStatePanel
                state={currentState === 'available' ? 'unavailable' : currentState}
                label={`${currentKind} ${boundedSelected + 1} is unavailable`}
              />
            )}
          </div>
          <div className="flex items-center justify-center gap-3 border-t border-border px-3 py-2">
            <Button
              type="button"
              variant="outline"
              className={readerControlClass}
              onClick={() => moveTo(boundedSelected - 1)}
              disabled={boundedSelected === 0}
              aria-label="Previous gallery item"
            >
              <ChevronLeft className="h-4 w-4" aria-hidden="true" />
              Previous
            </Button>
            <span className="min-w-16 text-center font-mono text-xs tabular-nums text-text-soft" aria-live="polite">
              {boundedSelected + 1} of {slots.length}
            </span>
            <Button
              type="button"
              variant="outline"
              className={readerControlClass}
              onClick={() => moveTo(boundedSelected + 1)}
              disabled={boundedSelected === slots.length - 1}
              aria-label="Next gallery item"
            >
              Next
              <ChevronRight className="h-4 w-4" aria-hidden="true" />
            </Button>
          </div>
        </div>
      </MediaDialog>
    </div>
  )
}
