// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'

import { videoFixture } from './fixtures'
import { VideoRenderer } from './video-renderer'

afterEach(cleanup)

describe('VideoRenderer', () => {
  it('uses poster first, then builds only a permission-minimal trusted YouTube iframe', () => {
    const parentNavigation = vi.fn()
    const { container } = render(
      <div onClick={parentNavigation}>
        <VideoRenderer item={videoFixture} presentation="card" />
      </div>,
    )

    expect(screen.getByRole('img', { name: /poster/i }).getAttribute('src')).toContain('youtube-poster')
    expect(screen.queryByTitle(/YouTube video/)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Load trusted YouTube player' }))
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
    render(<VideoRenderer item={videoFixture} presentation="card" />)
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
    const { container } = render(<VideoRenderer item={item} presentation="card" />)
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
    render(<VideoRenderer item={item} presentation="card" />)
    expect(screen.getByText('This representation is still being acquired.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Load trusted YouTube player' })).toBeTruthy()
  })
})
