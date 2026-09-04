import type {
  AcquisitionState,
  ReaderAssetVariant,
  ReaderMediaItem,
  ReaderPlaybackSpec,
} from './types'

export type ReaderResourceFamily = 'article' | 'media'

export type ResourceState = AcquisitionState | 'unavailable'

const ENCODED_PATH_SEPARATOR = /%(?:2f|5c)/i
const YOUTUBE_ITEM_ID = /^[A-Za-z0-9_-]{11}$/
const ARTICLE_RESOURCE_PATH = /^\/v1\/reader\/items\/[^/]+\/content$/
const MEDIA_RESOURCE_PATH = /^\/v1\/media\/variants\/[^/]+\/content$/

function hasControlCharacter(value: string): boolean {
  for (const character of value) {
    const code = character.charCodeAt(0)
    if (code <= 31 || code === 127) return true
  }
  return false
}

export function serverResourceHref(
  href: string | undefined,
  family: ReaderResourceFamily,
): string | undefined {
  const value = href?.trim()
  if (!value || !value.startsWith('/') || value.startsWith('//')) return undefined
  if (value.includes('\\') || hasControlCharacter(value) || ENCODED_PATH_SEPARATOR.test(value)) {
    return undefined
  }

  let parsed: URL
  try {
    parsed = new URL(value, 'http://reader.invalid')
  } catch {
    return undefined
  }
  if (parsed.origin !== 'http://reader.invalid') return undefined
  if (parsed.hash) return undefined

  const expectedPath = family === 'article' ? ARTICLE_RESOURCE_PATH : MEDIA_RESOURCE_PATH
  if (!expectedPath.test(parsed.pathname)) return undefined
  return `${parsed.pathname}${parsed.search}`
}

export function availableVariantHref(variant: ReaderAssetVariant): string | undefined {
  if (variant.acquisition_state !== 'available') return undefined
  return serverResourceHref(variant.content_href, 'media')
}

const SMALL_IMAGE_ORDER = ['preview', 'thumbnail', 'poster', 'original'] as const
const LARGE_IMAGE_ORDER = ['original', 'preview', 'poster', 'thumbnail'] as const

function chooseVariant(
  media: ReaderMediaItem,
  order: readonly ReaderAssetVariant['kind'][],
): ReaderAssetVariant | undefined {
  for (const kind of order) {
    const found = media.variants.find(
      (variant) => variant.kind === kind && availableVariantHref(variant) !== undefined,
    )
    if (found) return found
  }
  return undefined
}

export function imageVariants(media: ReaderMediaItem): {
  preview?: ReaderAssetVariant
  large?: ReaderAssetVariant
} {
  return {
    preview: chooseVariant(media, SMALL_IMAGE_ORDER),
    large: chooseVariant(media, LARGE_IMAGE_ORDER),
  }
}

export function orderedMedia(media: ReaderMediaItem[]): ReaderMediaItem[] {
  return media
    .map((item, index) => ({ item, index }))
    .sort((left, right) =>
      left.item.attachment.position === right.item.attachment.position
        ? left.index - right.index
        : left.item.attachment.position - right.item.attachment.position,
    )
    .map(({ item }) => item)
}

export function mediaState(media: ReaderMediaItem): ResourceState {
  if (media.variants.some((variant) => availableVariantHref(variant) !== undefined)) {
    return 'available'
  }
  if (media.variants.some((variant) => variant.acquisition_state === 'pending')) return 'pending'
  if (media.variants.some((variant) => variant.acquisition_state === 'failed')) return 'failed'
  if (media.variants.some((variant) => variant.acquisition_state === 'reference_only')) {
    return 'reference_only'
  }
  return 'unavailable'
}

export function stateLabel(state: ResourceState): string {
  switch (state) {
    case 'available':
      return 'Available'
    case 'pending':
      return 'Preparing media'
    case 'reference_only':
      return 'Source reference only'
    case 'failed':
      return 'Media unavailable'
    default:
      return 'No readable media'
  }
}

export function stateDescription(state: ResourceState): string {
  switch (state) {
    case 'pending':
      return 'This representation is still being acquired.'
    case 'reference_only':
      return 'The source is recorded, but Reader has no authorized local representation.'
    case 'failed':
      return 'This representation could not be acquired. Other item content is still available.'
    case 'available':
      return 'An authorized local representation is ready.'
    default:
      return 'Reader does not have an authorized representation for this item.'
  }
}

export function trustedYouTubeEmbedURL(spec: ReaderPlaybackSpec | undefined): string | undefined {
  if (
    spec?.kind !== 'provider_embed' ||
    spec.provider !== 'youtube' ||
    !spec.provider_item_id ||
    !YOUTUBE_ITEM_ID.test(spec.provider_item_id)
  ) {
    return undefined
  }

  const start = spec.start_seconds ?? 0
  if (!Number.isFinite(start) || start < 0) return undefined
  const wholeStart = Math.floor(start)
  if (!Number.isSafeInteger(wholeStart)) return undefined

  const params = new URLSearchParams({ rel: '0', playsinline: '1' })
  if (wholeStart > 0) params.set('start', String(wholeStart))
  return `https://www.youtube-nocookie.com/embed/${spec.provider_item_id}?${params.toString()}`
}

export interface TranscriptResource {
  state: ResourceState
  href?: string
  label: string
}

export function transcriptResource(media: ReaderMediaItem[]): TranscriptResource {
  const candidates = orderedMedia(media).filter(
    (item) =>
      item.kind === 'timed_text' ||
      item.attachment.role === 'transcript' ||
      item.variants.some((variant) => variant.kind === 'transcript' || variant.kind === 'subtitles'),
  )
  if (candidates.length === 0) {
    return { state: 'unavailable', label: 'No transcript was captured.' }
  }

  for (const item of candidates) {
    const variant = item.variants.find(
      (entry) =>
        (entry.kind === 'transcript' || entry.kind === 'subtitles') &&
        availableVariantHref(entry) !== undefined,
    )
    const href = variant && availableVariantHref(variant)
    if (href) return { state: 'available', href, label: 'Transcript available.' }
  }

  const state = candidates.reduce<ResourceState>((current, item) => {
    const next = mediaState(item)
    if (next === 'pending') return 'pending'
    if (current === 'pending') return current
    if (next === 'failed') return 'failed'
    if (current === 'failed') return current
    if (next === 'reference_only') return 'reference_only'
    return current
  }, 'unavailable')
  return { state, label: stateDescription(state) }
}
