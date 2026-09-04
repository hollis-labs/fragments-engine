import { describe, expect, it } from 'vitest'

import {
  availableVariantHref,
  mediaState,
  orderedMedia,
  serverResourceHref,
  transcriptResource,
  trustedYouTubeEmbedURL,
} from './media'
import { imageMedia, mixedGalleryFixture, videoFixture } from './fixtures'

describe('serverResourceHref', () => {
  it.each([
    ['article', '/v1/reader/items/fragment-1/content?revision_id=revision-1'],
    ['media', '/v1/media/variants/variant-1/content?fragment_id=fragment-1&revision_id=revision-1'],
  ] as const)('accepts an exact %s content route', (family, href) => {
    expect(serverResourceHref(href, family)).toBe(href)
  })

  it.each([
    ['article', 'https://evil.example/v1/reader/items/f/content'],
    ['article', '//evil.example/v1/reader/items/f/content'],
    ['article', '/v1/reader/items/f/commands'],
    ['article', '/v1/reader/items/f/content/more'],
    ['article', '/v1/reader/items/f/../content'],
    ['article', '/v1/reader/items/f%2Fother/content'],
    ['article', '/v1/reader/items/f/content#payload'],
    ['media', '/v1/media/variants/v/status'],
    ['media', '/v1/media/variants/v/content/more'],
    ['media', '/v1/media/variants/v/../content'],
    ['media', '/v1/media/variants/v%5Cother/content'],
    ['media', '/v1/reader/items/v/content'],
  ] as const)('rejects a non-resource or unsafe %s route: %s', (family, href) => {
    expect(serverResourceHref(href, family)).toBeUndefined()
  })
})

describe('media selection', () => {
  it('uses only available variants with authorized content hrefs', () => {
    const media = imageMedia(0)
    expect(availableVariantHref(media.variants[0])).toContain('/v1/media/variants/')
    media.variants[0] = {
      ...media.variants[0],
      content_href: undefined,
      source_url: 'https://untrusted.example/image.jpg',
    }
    expect(availableVariantHref(media.variants[0])).toBeUndefined()
    expect(mediaState(media)).toBe('available')
  })

  it('sorts attachment positions stably without mutating the projection', () => {
    const input = [imageMedia(5), imageMedia(1), imageMedia(1)]
    const ordered = orderedMedia(input)
    expect(ordered.map((item) => item.attachment.position)).toEqual([1, 1, 5])
    expect(input.map((item) => item.attachment.position)).toEqual([5, 1, 1])
    expect(ordered[0]).toBe(input[1])
    expect(ordered[1]).toBe(input[2])
  })

  it('reports an authorized transcript without consulting source URLs', () => {
    expect(transcriptResource(videoFixture.media)).toEqual({
      state: 'available',
      href: '/v1/media/variants/youtube-transcript/content?fragment_id=fragment-reader&revision_id=revision-reader-1',
      label: 'Transcript available.',
    })

    const media = mixedGalleryFixture.media.map((item) =>
      item.kind === 'timed_text'
        ? {
            ...item,
            variants: item.variants.map((variant) => ({
              ...variant,
              acquisition_state: 'reference_only' as const,
              content_href: undefined,
              source_url: 'https://untrusted.example/transcript.vtt',
            })),
          }
        : item,
    )
    expect(transcriptResource(media).state).toBe('reference_only')
  })
})

describe('trustedYouTubeEmbedURL', () => {
  it('constructs a youtube-nocookie URL from an exact provider spec', () => {
    expect(trustedYouTubeEmbedURL(videoFixture.playback)).toBe(
      'https://www.youtube-nocookie.com/embed/3RmtNXqnreI?rel=0&playsinline=1&start=12',
    )
  })

  it.each([
    undefined,
    { kind: 'external_stream', provider: 'youtube', provider_item_id: '3RmtNXqnreI', url: 'https://evil.example' },
    { kind: 'provider_embed', provider: 'vimeo', provider_item_id: '3RmtNXqnreI' },
    { kind: 'provider_embed', provider: 'youtube', provider_item_id: 'https://youtu.be/3RmtNXqnreI' },
    { kind: 'provider_embed', provider: 'youtube', provider_item_id: 'short' },
    { kind: 'provider_embed', provider: 'youtube', provider_item_id: '3RmtNXqnreI', start_seconds: -1 },
    { kind: 'provider_embed', provider: 'youtube', provider_item_id: '3RmtNXqnreI', start_seconds: Number.NaN },
  ])('rejects untrusted or malformed playback %#', (spec) => {
    expect(trustedYouTubeEmbedURL(spec)).toBeUndefined()
  })
})
