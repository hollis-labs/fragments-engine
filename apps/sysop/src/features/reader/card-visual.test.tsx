// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'

import { ReaderCardVisual } from './card-visual'
import { imageFixture, mixedGalleryFixture, videoFixture } from './fixtures'

afterEach(cleanup)

describe('ReaderCardVisual', () => {
  it.each([
    ['image', imageFixture, 'Image', 'image-0-preview'],
    ['gallery', mixedGalleryFixture, 'Gallery · 4 items', 'image-0-preview'],
    ['video', videoFixture, 'Video poster', 'youtube-poster'],
  ])('renders one bounded authorized %s visual with no detail interactions', (_kind, item, label, href) => {
    const { container } = render(<ReaderCardVisual item={item} />)

    const figure = screen.getByLabelText(label)
    expect(figure.className).toContain('max-h-40')
    expect(container.querySelectorAll('img')).toHaveLength(1)
    expect(container.querySelector('img')?.getAttribute('src')).toContain(href)
    expect(container.querySelector('button')).toBeNull()
    expect(container.querySelector('iframe')).toBeNull()
    expect(container.querySelector('[role="dialog"]')).toBeNull()
    expect(container.querySelector('[data-transcript-state]')).toBeNull()
    expect(container.innerHTML).not.toContain('untrusted.example')
    expect(container.innerHTML).not.toContain('youtube.com/watch')
  })

  it('states that a reference-only visual was not captured without using its source URL', () => {
    const item = {
      ...imageFixture,
      media: [
        {
          ...imageFixture.media[0],
          variants: [
            {
              asset_variant_id: 'reference-image',
              kind: 'original' as const,
              custody: 'reference' as const,
              acquisition_state: 'reference_only' as const,
              source_url: 'https://evil.example/not-authorized.jpg',
            },
          ],
        },
      ],
    }
    const { container } = render(<ReaderCardVisual item={item} />)

    expect(screen.getByText('No captured visual')).toBeTruthy()
    expect(container.querySelector('img')).toBeNull()
    expect(container.innerHTML).not.toContain('evil.example')
  })
})
