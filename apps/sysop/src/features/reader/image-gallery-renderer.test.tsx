// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'

import { GalleryRenderer } from './gallery-renderer'
import { ImageRenderer } from './image-renderer'
import { imageFixture, mixedGalleryFixture } from './fixtures'

afterEach(cleanup)

describe('ImageRenderer', () => {
  it('opens an accessible large view, contains parent navigation, and restores focus on Escape', async () => {
    const parentNavigation = vi.fn()
    render(
      <div onClick={parentNavigation}>
        <ImageRenderer item={imageFixture} presentation="card" />
      </div>,
    )
    const trigger = screen.getByRole('button', { name: /view larger image/i })
    expect(screen.getByRole('img', { name: 'Gallery item 1' }).getAttribute('src')).toContain('/v1/media/variants/')

    fireEvent.click(trigger)
    const dialog = await screen.findByRole('dialog')
    expect(parentNavigation).not.toHaveBeenCalled()
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))
    expect(within(dialog).getByRole('img', { name: 'Gallery item 1' })).toBeTruthy()

    fireEvent.keyDown(dialog, { key: 'Escape', code: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(document.activeElement).toBe(trigger))
  })

  it('renders explicit state rather than a source URL when no local image is available', () => {
    const item = {
      ...imageFixture,
      media: [{
        ...imageFixture.media[0],
        variants: [{
          asset_variant_id: 'reference-image',
          kind: 'original' as const,
          custody: 'reference' as const,
          acquisition_state: 'reference_only' as const,
          source_url: 'https://evil.example/image.jpg',
        }],
      }],
    }
    const { container } = render(<ImageRenderer item={item} presentation="card" />)
    expect(screen.getByText('Image is not available')).toBeTruthy()
    expect(container.innerHTML).not.toContain('evil.example')
  })
})

describe('GalleryRenderer', () => {
  it('preserves ordered renderable image slots while excluding auxiliary media', () => {
    render(<GalleryRenderer item={mixedGalleryFixture} presentation="detail" />)
    expect(screen.getAllByText('1 of 3').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: /select image 1 of 3: available/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: /select image 2 of 3: media unavailable/i })).toBeTruthy()
    expect(screen.getByRole('button', { name: /select image 3 of 3: available/i })).toBeTruthy()
    expect(screen.queryByText('Auxiliary poster')).toBeNull()
    expect(screen.queryByText(/transcript available/i)).toBeNull()
  })

  it('supports Arrow keys and Home/End, closes on Escape, and returns focus', async () => {
    const parentNavigation = vi.fn()
    render(
      <div onClick={parentNavigation}>
        <GalleryRenderer item={mixedGalleryFixture} presentation="detail" />
      </div>,
    )
    const trigger = screen.getByRole('button', { name: 'Open gallery at image 1 of 3' })
    fireEvent.click(trigger)
    const dialog = await screen.findByRole('dialog')
    expect(parentNavigation).not.toHaveBeenCalled()
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true))

    fireEvent.keyDown(dialog, { key: 'ArrowRight', code: 'ArrowRight' })
    expect(within(dialog).getByText('Image 2 is unavailable')).toBeTruthy()
    fireEvent.keyDown(dialog, { key: 'End', code: 'End' })
    expect(within(dialog).getByRole('img', { name: 'Gallery item 9' })).toBeTruthy()
    fireEvent.keyDown(dialog, { key: 'Home', code: 'Home' })
    expect(within(dialog).getByRole('img', { name: 'Gallery item 1' })).toBeTruthy()
    fireEvent.keyDown(dialog, { key: 'ArrowLeft', code: 'ArrowLeft' })
    expect(within(dialog).getByRole('img', { name: 'Gallery item 1' })).toBeTruthy()

    fireEvent.keyDown(dialog, { key: 'Escape', code: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => {
      expect(document.activeElement?.isConnected).toBe(true)
      expect(document.activeElement?.getAttribute('aria-label')).toBe('Open gallery at image 1 of 3')
    })
  })
})
