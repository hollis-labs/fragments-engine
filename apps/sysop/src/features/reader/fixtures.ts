import type { ReaderAssetVariant, ReaderMediaItem, ReaderRenderableItem } from './types'

function attachment(position: number, role: ReaderMediaItem['attachment']['role'] = 'gallery_item') {
  return {
    attachment_id: `attachment-${position}`,
    fragment_revision_id: 'revision-reader-1',
    media_asset_id: `asset-${position}`,
    role,
    position,
  } as const
}

export function availableImageVariant(
  id: string,
  kind: ReaderAssetVariant['kind'] = 'preview',
): ReaderAssetVariant {
  return {
    asset_variant_id: id,
    kind,
    custody: 'mirror',
    acquisition_state: 'available',
    mime_type: 'image/jpeg',
    content_href: `/v1/media/variants/${id}/content?fragment_id=fragment-reader&revision_id=revision-reader-1`,
  }
}

export function imageMedia(position: number, state: ReaderAssetVariant['acquisition_state'] = 'available'): ReaderMediaItem {
  return {
    attachment: attachment(position),
    media_asset_id: `asset-${position}`,
    kind: 'image',
    alt_text: `Gallery item ${position + 1}`,
    variants: state === 'available'
      ? [availableImageVariant(`image-${position}-preview`), availableImageVariant(`image-${position}-original`, 'original')]
      : [{
          asset_variant_id: `image-${position}-original`,
          kind: 'original',
          custody: state === 'reference_only' ? 'reference' : 'mirror',
          acquisition_state: state,
          source_url: 'https://untrusted.example/source.jpg',
          failure: state === 'failed'
            ? { code: 'acquire_failed', message: 'Provider details stay out of the renderer.', retryable: true }
            : undefined,
        }],
  }
}

export function readerFixture(
  overrides: Partial<ReaderRenderableItem> = {},
): ReaderRenderableItem {
  return {
    fragment_id: 'fragment-reader',
    fragment_revision_id: 'revision-reader-1',
    renderer: 'article',
    display: {
      title: { value: 'How durable capture changes reading', source: 'source' },
      description: { value: 'A captured browser observation.', source: 'source' },
      byline: { value: 'Hollis Labs', source: 'provider' },
      published_at: '2026-09-03T18:42:00Z',
      summary: { value: 'A bounded summary of the captured page.', source: 'deterministic' },
    },
    article: {
      preview_markdown: 'Useful page text remains separate from provider media.',
      full_content_available: true,
      full_content_href: '/v1/reader/items/fragment-reader/content?revision_id=revision-reader-1',
    },
    media: [],
    tags: { combined: ['capture', 'reader'] },
    ...overrides,
  }
}

export const articleFixture = readerFixture()

export const imageFixture = readerFixture({
  renderer: 'image',
  media: [imageMedia(0)],
})

export const mixedGalleryFixture = readerFixture({
  renderer: 'gallery',
  media: [
    imageMedia(8),
    {
      attachment: {
        ...attachment(3, 'poster'),
        attachment_id: 'poster-attachment',
        media_asset_id: 'poster-asset',
      },
      media_asset_id: 'poster-asset',
      kind: 'image',
      alt_text: 'Auxiliary poster',
      variants: [availableImageVariant('auxiliary-poster', 'poster')],
    },
    imageMedia(2, 'failed'),
    {
      attachment: {
        ...attachment(4, 'transcript'),
        attachment_id: 'transcript-attachment',
        media_asset_id: 'transcript-asset',
      },
      media_asset_id: 'transcript-asset',
      kind: 'timed_text',
      variants: [{
        asset_variant_id: 'transcript-variant',
        kind: 'transcript',
        custody: 'mirror',
        acquisition_state: 'available',
        mime_type: 'text/vtt',
        content_href: '/v1/media/variants/transcript-variant/content?fragment_id=fragment-reader&revision_id=revision-reader-1',
      }],
    },
    imageMedia(0),
    {
      attachment: {
        ...attachment(6, 'other'),
        attachment_id: 'other-attachment',
        media_asset_id: 'other-asset',
      },
      media_asset_id: 'other-asset',
      kind: 'other',
      variants: [],
    },
  ],
})

export const videoFixture = readerFixture({
  renderer: 'video',
  playback: {
    kind: 'provider_embed',
    provider: 'youtube',
    provider_item_id: '3RmtNXqnreI',
    start_seconds: 12.8,
  },
  media: [
    {
      attachment: {
        ...attachment(0, 'primary'),
        media_asset_id: 'video-asset',
      },
      media_asset_id: 'video-asset',
      provider_media_id: '3RmtNXqnreI',
      kind: 'video',
      variants: [
        availableImageVariant('youtube-poster', 'poster'),
        {
          asset_variant_id: 'youtube-original',
          kind: 'original',
          custody: 'reference',
          acquisition_state: 'reference_only',
          mime_type: 'video/mp4',
          source_url: 'https://www.youtube.com/watch?v=3RmtNXqnreI',
        },
      ],
    },
    {
      attachment: {
        ...attachment(1, 'transcript'),
        media_asset_id: 'transcript-asset',
      },
      media_asset_id: 'transcript-asset',
      kind: 'timed_text',
      variants: [{
        asset_variant_id: 'youtube-transcript',
        kind: 'transcript',
        custody: 'mirror',
        acquisition_state: 'available',
        mime_type: 'text/vtt',
        content_href: '/v1/media/variants/youtube-transcript/content?fragment_id=fragment-reader&revision_id=revision-reader-1',
      }],
    },
  ],
})
