// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'

import { imageFixture, mixedGalleryFixture, readerFixture, videoFixture } from './fixtures'
import { ReaderContentRenderer } from './reader-content-renderer'

afterEach(cleanup)

describe('ReaderContentRenderer', () => {
  it.each([
    ['article', readerFixture({ renderer: 'article' }), 'article'],
    ['audio', readerFixture({ renderer: 'audio' }), 'audio'],
    ['document', readerFixture({ renderer: 'document' }), 'document'],
    ['text', readerFixture({ renderer: 'text' }), 'article'],
    ['future-kind', readerFixture({ renderer: 'future-kind' }), 'unknown'],
  ])('dispatches %s safely without shell or route dependencies', (_kind, item, expected) => {
    const { container } = render(<ReaderContentRenderer item={item} presentation="card" />)
    expect(container.querySelector(`[data-reader-renderer="${expected}"]`)).toBeTruthy()
  })

  it.each([
    ['image', imageFixture],
    ['gallery', mixedGalleryFixture],
  ])('keeps the revision-pinned sanitized article available in %s detail', (_kind, item) => {
    const { container } = render(<ReaderContentRenderer item={item} presentation="detail" />)
    const frame = screen.getByTitle(`Article: ${item.display.title.value}`)
    expect(frame.getAttribute('src')).toBe(item.article.full_content_href)
    expect(container.innerHTML).not.toContain('untrusted.example')
  })

  it.each([
    ['image', imageFixture],
    ['gallery', mixedGalleryFixture],
    ['video', videoFixture],
  ])('keeps %s cards static and bounded without loading detail interactions', (_kind, item) => {
    const { container } = render(<ReaderContentRenderer item={item} presentation="card" />)
    expect(container.querySelector('[data-reader-card-visual]')).toBeTruthy()
    expect(screen.queryByTitle(`Article: ${item.display.title.value}`)).toBeNull()
    expect(container.querySelector('button')).toBeNull()
    expect(container.querySelector('iframe')).toBeNull()
    expect(container.querySelector('[role="dialog"]')).toBeNull()
    expect(container.querySelector('[data-transcript-state]')).toBeNull()
  })

  it('does not append an empty article section to pure media detail', () => {
    const item = {
      ...imageFixture,
      article: { preview_markdown: '', full_content_available: false },
    }
    const { container } = render(<ReaderContentRenderer item={item} presentation="detail" />)
    expect(container.querySelector('[data-reader-renderer="image"]')).toBeTruthy()
    expect(screen.queryByText('Captured page')).toBeNull()
    expect(container.querySelector('[data-reader-renderer="article"]')).toBeNull()
  })
})
