// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'

import { imageFixture, mixedGalleryFixture, readerFixture, videoFixture } from './fixtures'
import { ReaderContentRenderer } from './reader-content-renderer'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

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
  ])('keeps the revision-pinned sanitized article available in %s detail', async (_kind, item) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      '<p>Theme-aware captured page</p>',
      { status: 200, headers: { 'content-type': 'text/html; charset=utf-8' } },
    )))
    const { container } = render(<ReaderContentRenderer item={item} presentation="detail" />)
    expect(await screen.findByText('Theme-aware captured page')).toBeTruthy()
    expect(container.querySelector('[data-reader-sanitized-content]')).toBeTruthy()
    expect(container.querySelector('iframe')).toBeNull()
    expect(container.innerHTML).not.toContain('untrusted.example')
  })

  it.each([
    ['image', imageFixture],
    ['gallery', mixedGalleryFixture],
    ['video', videoFixture],
  ])('opens %s card media in a focused preview without navigating', async (_kind, item) => {
    const { container } = render(<ReaderContentRenderer item={item} presentation="card" />)
    expect(container.querySelector('[data-reader-card-visual]')).toBeTruthy()
    const trigger = screen.getByRole('button', { name: _kind === 'video' ? /Play video:/ : /View larger image:/ })
    expect(container.querySelector('iframe')).toBeNull()
    expect(container.querySelector('[role="dialog"]')).toBeNull()
    expect(container.querySelector('[data-transcript-state]')).toBeNull()
    fireEvent.click(trigger)
    expect(await screen.findByRole('dialog')).toBeTruthy()
    if (_kind === 'video') expect(screen.getByTitle(/YouTube video:/)).toBeTruthy()
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
