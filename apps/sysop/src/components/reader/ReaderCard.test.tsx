import { fireEvent, render, screen, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ReaderCard } from './ReaderCard'
import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient } from '@/lib/api'
import { readerItem } from '@/test/reader-fixture'

describe('ReaderCard', () => {
  it('opens from the card surface and keyboard', () => {
    const onOpen = vi.fn()
    render(<ReaderCard item={readerItem()} onOpen={onOpen} onItemChange={() => {}} />)
    const card = screen.getByRole('link', { name: 'Open A durable fragment' })

    fireEvent.click(card)
    fireEvent.keyDown(card, { key: 'Enter' })

    expect(onOpen).toHaveBeenCalledTimes(2)
    expect(onOpen).toHaveBeenLastCalledWith('fragment-1')
    expect(screen.getByRole('tab', { name: 'Content' }).getAttribute('aria-selected')).toBe('true')
  })

  it('excludes source links, media controls, action menus, and text selection', () => {
    const onOpen = vi.fn()
    render(
      <ReaderCard
        item={readerItem()}
        onOpen={onOpen}
        onItemChange={() => {}}
        mediaSlot={
          <div>
            <button type="button">Next image</button>
            <video data-testid="reader-player" controls />
          </div>
        }
      />,
    )

    fireEvent.click(screen.getByRole('link', { name: 'View source' }))
    fireEvent.click(screen.getByRole('button', { name: 'Next image' }))
    fireEvent.click(screen.getByTestId('reader-player'))
    expect(onOpen).not.toHaveBeenCalled()

    const summary = screen.getByText(/A compact summary/)
    const selection = window.getSelection()
    const range = document.createRange()
    range.selectNodeContents(summary)
    selection?.removeAllRanges()
    selection?.addRange(range)

    fireEvent.click(screen.getByTestId('reader-card'))
    expect(onOpen).not.toHaveBeenCalled()
  })

  it('shows each operational axis while preserving partial states', () => {
    render(
      <ReaderCard
        item={
          readerItem({
            operations: {
              triage: { case_ids: ['case-1'], unresolved_count: 1 },
              routing: { state: 'partial', references: ['route-1'] },
              materialization: { state: 'pending', references: ['job-1'] },
              enrichment: [
                { capability: 'summary', state: 'pending' },
                { capability: 'OCR', state: 'failed' },
              ],
              acquisition: { pending: 1, available: 0, reference_only: 0, failed: 1 },
            },
          })
        }
        onOpen={() => {}}
        onItemChange={() => {}}
      />,
    )

    const states = screen.getByLabelText('Fragment processing states')
    expect(states.textContent).toContain('Triage1 open')
    expect(states.textContent).toContain('RoutingPartial')
    expect(states.textContent).toContain('MaterializationPending')
    expect(states.textContent).toContain('Enrichment1 pending · 1 failed')
    expect(states.textContent).toContain('Media1 failed · 1 pending')
  })

  it('keeps the media preview in the same heading row as the title and reading controls', () => {
    const item = readerItem({
      actions: [
        {
          command: 'mark_read',
          input_schema:
            'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json#/$defs/MarkRead',
          expected_revision_required: true,
        },
        {
          command: 'set_reading_progress',
          input_schema:
            'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json#/$defs/SetReadingProgress',
          expected_revision_required: true,
        },
      ],
    })
    render(
      <ReaderCard
        item={item}
        onOpen={() => {}}
        onItemChange={() => {}}
        mediaSlot={<div data-testid="custom-card-media">Preview</div>}
      />,
    )

    const heading = screen.getByTestId('reader-card-heading')
    expect(within(heading).getByRole('heading', { name: 'A durable fragment' })).not.toBeNull()
    expect(within(heading).getByRole('button', { name: 'Reading position' })).not.toBeNull()
    expect(within(heading).getByTestId('custom-card-media')).not.toBeNull()
  })

  it('does not project an unsafe persisted source string as an anchor', () => {
    const item = readerItem({
      source: { ...readerItem().source, canonical_url: 'javascript:alert(document.cookie)' },
    })
    render(<ReaderCard item={item} onOpen={() => {}} onItemChange={() => {}} />)

    expect(screen.queryByRole('link', { name: 'View source' })).toBeNull()
  })

  it('keeps inline reading controls inside the card interaction fence', () => {
    const onOpen = vi.fn()
    const item = readerItem({
      actions: [
        {
          command: 'mark_read',
          input_schema:
            'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json#/$defs/MarkRead',
          expected_revision_required: true,
        },
      ],
    })
    render(
      <ApiProvider client={{ ...apiClient, executeReaderCommand: async () => item }}>
        <ReaderCard
          item={item}
          onOpen={onOpen}
          onItemChange={() => {}}
        />
      </ApiProvider>,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Mark as read' }))

    expect(onOpen).not.toHaveBeenCalled()
  })
})
