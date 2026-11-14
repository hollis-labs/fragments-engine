// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'

import { ArticleFeaturedImage } from './article-featured'
import { ReaderCardVisual } from './card-visual'
import { articleFixture, imageMedia, readerFixture } from './fixtures'
import type { ReaderMediaItem } from './types'
import { featuredArticleImage } from './media'

afterEach(cleanup)

function articleWithImages(count: number) {
  const media = Array.from({ length: count }, (_, position) => imageMedia(position))
  // The hero is the last image by position, so featured must not be "first".
  const hero = { ...media[count - 1], attachment: { ...media[count - 1].attachment, role: 'hero' as const } }
  return readerFixture({
    renderer: 'article',
    display: {
      ...articleFixture.display,
      featured_media_id: hero.media_asset_id,
      image_count: count,
    },
    media: [...media.slice(0, count - 1), hero],
  })
}

describe('ArticleFeaturedImage', () => {
  it('shows the featured image and a gallery icon with the count that opens every image', async () => {
    const item = articleWithImages(2)
    const { container } = render(<ArticleFeaturedImage item={item} />)

    const featured = container.querySelector('[data-reader-featured-image] > img')
    expect(featured?.getAttribute('src')).toContain('image-1-')
    const trigger = screen.getByRole('button', { name: 'Open gallery of 2 images' })
    expect(trigger.textContent).toBe('2')
    expect(container.querySelector('[role="dialog"]')).toBeNull()

    fireEvent.click(trigger)
    const dialog = await screen.findByRole('dialog')
    const sources = Array.from(dialog.querySelectorAll('img')).map((image) => image.getAttribute('src') ?? '')
    expect(sources).toHaveLength(2)
    expect(sources[0]).toContain('image-0-')
    expect(sources[1]).toContain('image-1-')
  })

  it('shows no gallery icon for a single image and nothing without a featured image', () => {
    const single = articleWithImages(1)
    const { container, rerender } = render(<ArticleFeaturedImage item={single} />)
    expect(container.querySelector('[data-reader-featured-image] > img')).not.toBeNull()
    expect(screen.queryByRole('button')).toBeNull()

    rerender(<ArticleFeaturedImage item={articleFixture} />)
    expect(container.querySelector('[data-reader-featured-image]')).toBeNull()
  })

  it('uses the featured image as the list card preview of an article', () => {
    const item = articleWithImages(3)
    expect(featuredArticleImage(item)?.media_asset_id).toBe('asset-2')
    const { container } = render(<ReaderCardVisual item={item} />)
    expect(container.querySelector('img')?.getAttribute('src')).toContain('image-2-')
    expect(screen.getByRole('button', { name: /View larger image:/ })).toBeTruthy()
  })

  it('counts an asset attached twice once and lists it once', async () => {
    const base = articleWithImages(2)
    const duplicate: ReaderMediaItem = {
      ...base.media[1],
      attachment: { ...base.media[1].attachment, attachment_id: 'attachment-dup', role: 'inline', position: 9 },
    }
    const item = { ...base, media: [...base.media, duplicate] }
    render(<ArticleFeaturedImage item={item} />)

    fireEvent.click(screen.getByRole('button', { name: 'Open gallery of 2 images' }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog.querySelectorAll('img')).toHaveLength(2)
  })

  it('keeps the gallery reachable when the featured image is unavailable', async () => {
    const base = articleWithImages(3)
    const media = base.media.map((entry) =>
      entry.media_asset_id === base.display.featured_media_id ? imageMedia(2, 'failed') : entry,
    )
    const { container } = render(<ArticleFeaturedImage item={{ ...base, media }} />)

    expect(container.querySelector('[data-reader-featured-image] > img')).toBeNull()
    const trigger = screen.getByRole('button', { name: 'Open gallery of 3 images' })
    fireEvent.click(trigger)
    const dialog = await screen.findByRole('dialog')
    expect(dialog.querySelectorAll('img')).toHaveLength(2)
  })
})
