// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'

import { videoFixture } from './fixtures'
import { VideoRenderer } from './video-renderer'

afterEach(cleanup)

describe('VideoRenderer', () => {
  it('renders a permission-minimal trusted YouTube iframe directly in the detail hero', () => {
    const parentNavigation = vi.fn()
    const { container } = render(
      <div onClick={parentNavigation}>
        <VideoRenderer item={videoFixture} presentation="detail" />
      </div>,
    )

    expect(screen.queryByRole('img', { name: /poster/i })).toBeNull()
    expect(parentNavigation).not.toHaveBeenCalled()

    const frame = screen.getByTitle(`YouTube video: ${videoFixture.display.title.value}`)
    expect(frame.getAttribute('src')).toBe(
      'https://www.youtube-nocookie.com/embed/3RmtNXqnreI?rel=0&playsinline=1&start=12',
    )
    expect(frame.getAttribute('src')).not.toContain('autoplay')
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts allow-same-origin allow-presentation')
    expect(frame.getAttribute('allow')).toBe('encrypted-media; picture-in-picture; fullscreen')
    expect(frame.hasAttribute('allowfullscreen')).toBe(true)
    expect(container.innerHTML).not.toContain('youtube.com/watch')
    expect(container.innerHTML).not.toContain('evil.example')
    expect(screen.getByRole('link', { name: 'Open transcript' }).getAttribute('href')).toContain(
      '/v1/media/variants/youtube-transcript/content',
    )
  })

  it('opens a large trusted player, handles Escape, and restores expand-button focus', async () => {
    render(<VideoRenderer item={videoFixture} presentation="detail" />)
    const expand = screen.getByRole('button', { name: 'Expand video' })
    fireEvent.click(expand)
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))
    const frame = within(dialog).getByTitle(`YouTube video: ${videoFixture.display.title.value}`)
    expect(frame.hasAttribute('allowfullscreen')).toBe(true)

    fireEvent.keyDown(dialog, { key: 'Escape', code: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(expand))
  })

  it('does not use an external stream, source URL, oEmbed URL, or spoofed provider ID', () => {
    const item = {
      ...videoFixture,
      playback: {
        kind: 'external_stream',
        provider: 'youtube',
        provider_item_id: 'https://youtu.be/3RmtNXqnreI',
        url: 'https://evil.example/oembed-player',
      },
      media: videoFixture.media.map((media) => ({
        ...media,
        variants: media.variants.filter((variant) => variant.kind !== 'poster'),
      })),
    }
    const { container } = render(<VideoRenderer item={item} presentation="detail" />)
    expect(screen.queryByTitle(/YouTube video/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Load trusted YouTube player' })).toBeNull()
    expect(screen.getByText('Trusted playback unavailable')).toBeTruthy()
    expect(container.innerHTML).not.toContain('evil.example')
    expect(container.innerHTML).not.toContain('youtu.be')
  })

  it('keeps an independently pending transcript from breaking playback', () => {
    const item = {
      ...videoFixture,
      media: videoFixture.media.map((media) =>
        media.kind === 'timed_text'
          ? {
              ...media,
              variants: media.variants.map((variant) => ({
                ...variant,
                content_href: undefined,
                acquisition_state: 'pending' as const,
              })),
            }
          : media,
      ),
    }
    render(<VideoRenderer item={item} presentation="detail" />)
    expect(screen.getByText('This representation is still being acquired.')).toBeTruthy()
    expect(screen.getByTitle(/YouTube video/)).toBeTruthy()
  })

  it.each([
    ['pending', undefined, 'pending'],
    ['failed', undefined, 'failed'],
    ['available', 0, 'unavailable'],
  ] as const)('does not let an available poster promote %s subtitles to a useful transcript', (state, byteSize, expected) => {
    const video = videoFixture.media[0]
    const item = {
      ...videoFixture,
      media: [{
        ...video,
        variants: [
          ...video.variants.filter((variant) => variant.kind === 'poster'),
          {
            asset_variant_id: `youtube-subtitles-${state}`,
            kind: 'subtitles' as const,
            custody: 'mirror' as const,
            acquisition_state: state,
            byte_size: byteSize,
            content_href: state === 'available'
              ? '/v1/media/variants/youtube-subtitles-empty/content?fragment_id=fragment-reader&revision_id=revision-reader-1'
              : undefined,
            failure: state === 'failed'
              ? { code: 'captions_failed', message: 'Captions failed.', retryable: true }
              : undefined,
          },
        ],
      }],
    }
    const { container } = render(<VideoRenderer item={item} presentation="detail" />)

    expect(container.querySelector('[data-transcript-state]')?.getAttribute('data-transcript-state')).toBe(expected)
    expect(screen.queryByRole('link', { name: 'Open transcript' })).toBeNull()
    expect(screen.getByTitle(/YouTube video/)).toBeTruthy()
  })
})
