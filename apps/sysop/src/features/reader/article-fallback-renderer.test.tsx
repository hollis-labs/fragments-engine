// @vitest-environment jsdom

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'

import { ArticleRenderer } from './article-renderer'
import { AudioRenderer, DocumentRenderer, UnknownRenderer } from './fallback-renderer'
import { availableImageVariant, readerFixture } from './fixtures'

afterEach(cleanup)

describe('ArticleRenderer', () => {
  it('renders preview Markdown as bounded text instead of executable HTML', () => {
    const item = readerFixture({
      article: {
        preview_markdown: '<img src=x onerror="globalThis.pwned=true"><script>alert(1)</script>',
        full_content_available: false,
      },
    })
    const { container } = render(<ArticleRenderer item={item} presentation="card" />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('script')).toBeNull()
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeTruthy()
    expect(container.querySelector('p')?.className).toContain('line-clamp-6')
  })

  it('loads full content only through an empty-sandbox server article resource', () => {
    const item = readerFixture()
    render(<ArticleRenderer item={item} presentation="detail" />)
    const frame = screen.getByTitle(`Article: ${item.display.title.value}`)
    expect(frame.getAttribute('src')).toBe(item.article.full_content_href)
    expect(frame.getAttribute('sandbox')).toBe('')
    expect(frame.getAttribute('srcdoc')).toBeNull()
    expect(frame.getAttribute('referrerpolicy')).toBe('no-referrer')
  })

  it('falls back to inert text when the projected full href is external or near-miss', () => {
    const item = readerFixture({
      article: {
        preview_markdown: 'Safe fallback text',
        full_content_available: true,
        full_content_href: 'https://evil.example/v1/reader/items/f/content',
      },
    })
    render(<ArticleRenderer item={item} presentation="detail" />)
    expect(screen.queryByTitle(/Article:/)).toBeNull()
    expect(screen.getByText('Safe fallback text')).toBeTruthy()
  })
})

describe('fallback renderers', () => {
  it('keeps unknown content inert and bounded on cards', () => {
    const item = readerFixture({
      renderer: 'unknown',
      article: { preview_markdown: '<iframe src="https://evil.example"></iframe>', full_content_available: false },
    })
    const { container } = render(<UnknownRenderer item={item} presentation="card" />)
    expect(container.querySelector('iframe')).toBeNull()
    expect(screen.getByText(/<iframe/)).toBeTruthy()
  })

  it.each([
    ['audio', AudioRenderer, 'Inline audio playback is an extension point'],
    ['document', DocumentRenderer, 'Inline page rendering is an extension point'],
  ] as const)('exposes an explicit %s extension state and only an authorized link', (kind, Renderer, message) => {
    const item = readerFixture({
      renderer: kind,
      media: [{
        attachment: {
          attachment_id: `${kind}-attachment`,
          fragment_revision_id: 'revision-reader-1',
          media_asset_id: `${kind}-asset`,
          role: 'primary',
          position: 0,
        },
        media_asset_id: `${kind}-asset`,
        kind,
        variants: [{
          ...availableImageVariant(`${kind}-variant`, 'original'),
          mime_type: kind === 'audio' ? 'audio/mpeg' : 'application/pdf',
          source_url: 'https://evil.example/provider-resource',
        }],
      }],
    })
    const { container } = render(<Renderer item={item} presentation="detail" />)
    expect(screen.getByText(new RegExp(message))).toBeTruthy()
    expect(screen.getByRole('link').getAttribute('href')).toContain(`/v1/media/variants/${kind}-variant/content`)
    expect(container.innerHTML).not.toContain('evil.example')
  })

  it('shows pending and failed media state without throwing', () => {
    const pending = readerFixture({
      renderer: 'audio',
      media: [{
        attachment: {
          attachment_id: 'pending-audio',
          fragment_revision_id: 'revision-reader-1',
          media_asset_id: 'pending-audio',
          role: 'primary',
          position: 0,
        },
        media_asset_id: 'pending-audio',
        kind: 'audio',
        variants: [{
          asset_variant_id: 'pending-audio',
          kind: 'audio',
          custody: 'mirror',
          acquisition_state: 'pending',
        }],
      }],
    })
    const { rerender } = render(<AudioRenderer item={pending} presentation="card" />)
    expect(screen.getByText('Preparing media')).toBeTruthy()
    pending.media[0].variants[0] = {
      ...pending.media[0].variants[0],
      acquisition_state: 'failed',
      failure: { code: 'provider_failed', message: 'Untrusted detail', retryable: true },
    }
    rerender(<AudioRenderer item={pending} presentation="card" />)
    expect(screen.getByRole('alert').textContent).toContain('Media unavailable')
    expect(screen.getByRole('alert').textContent).not.toContain('Untrusted detail')
  })
})
