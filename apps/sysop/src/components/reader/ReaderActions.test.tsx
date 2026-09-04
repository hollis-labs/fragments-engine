import { useState } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { ApiProvider } from '@/contexts/ApiContext'
import { ApiError, apiClient, type ApiClient } from '@/lib/api'
import type { ReaderCommandCapability, ReaderCommandName, ReaderItem } from '@/lib/types'
import { readerItem } from '@/test/reader-fixture'
import { ReaderActions } from './ReaderActions'

const schema =
  'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json'

function capability(command: ReaderCommandName): ReaderCommandCapability {
  return { command, input_schema: `${schema}#/$defs/${command}`, expected_revision_required: true }
}

function clientWithActions(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    ...apiClient,
    executeReaderCommand: async () => readerItem(),
    fetchReaderItem: async () => readerItem(),
    fetchRoutes: async () => [],
    fetchDestinations: async () => [],
    ...overrides,
  }
}

function renderActions(
  initial: ReaderItem,
  client: ApiClient,
  onItemChange = vi.fn<(item: ReaderItem) => void>(),
) {
  function Harness() {
    const [item, setItem] = useState(initial)
    return (
      <ReaderActions
        item={item}
        onItemChange={(updated) => {
          onItemChange(updated)
          setItem(updated)
        }}
      />
    )
  }
  render(
    <ApiProvider client={client}>
      <Harness />
    </ApiProvider>,
  )
  return onItemChange
}

async function openActions() {
  fireEvent.click(screen.getByRole('button', { name: 'Open Reader actions' }))
  await screen.findByText('Reader actions')
}

describe('ReaderActions', () => {
  it('renders only server-advertised frozen actions and never exposes triage', async () => {
    const invalid = {
      command: 'apply_triage_decision',
      input_schema: schema,
      expected_revision_required: true,
    } as unknown as ReaderCommandCapability
    renderActions(
      readerItem({ actions: [capability('add_tag'), invalid, capability('mark_read')] }),
      clientWithActions(),
    )

    await openActions()
    expect(screen.getByRole('button', { name: 'Add tag' })).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Mark read' })).not.toBeNull()
    expect(screen.queryByText(/triage/i)).toBeNull()
    expect(screen.queryByText(/disposition/i)).toBeNull()
  })

  it('applies a safe optimistic tag and reconciles from the returned Reader item', async () => {
    let resolveCommand!: (item: ReaderItem) => void
    const pending = new Promise<ReaderItem>((resolve) => {
      resolveCommand = resolve
    })
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(() => pending)
    const onItemChange = renderActions(
      readerItem({ actions: [capability('add_tag')] }),
      clientWithActions({ executeReaderCommand }),
    )

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Add tag' }))
    fireEvent.change(screen.getByPlaceholderText('reader'), { target: { value: 'optimistic' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save tag' }))

    expect(screen.getByText('Saving add tag…')).not.toBeNull()
    expect(onItemChange.mock.calls[0]![0].tags.combined).toContain('optimistic')
    const reconciled = readerItem({
      revision: 3,
      tags: {
        combined: ['systems', 'capture', 'optimistic'],
        attributed: [
          ...readerItem().tags.attributed,
          { value: 'optimistic', source: 'user', observation_id: 'observation-command' },
        ],
      },
      actions: [capability('add_tag')],
    })
    await act(async () => resolveCommand(reconciled))

    expect(await screen.findByText('Tag added.')).not.toBeNull()
    expect(onItemChange).toHaveBeenLastCalledWith(reconciled)
  })

  it('retries a network-uncertain outcome with the same command object and semantic payload', async () => {
    const reconciled = readerItem({ reading_state: { ...readerItem().reading_state, state: 'read', revision: 1 } })
    const executeReaderCommand = vi
      .fn<ApiClient['executeReaderCommand']>()
      .mockRejectedValueOnce(new TypeError('network disconnected'))
      .mockResolvedValueOnce(reconciled)
    renderActions(
      readerItem({ actions: [capability('mark_read')] }),
      clientWithActions({ executeReaderCommand }),
    )

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Mark read' }))
    expect(await screen.findByText(/network outcome is uncertain/i)).not.toBeNull()
    const originalCommand = executeReaderCommand.mock.calls[0]![0].command

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findByText('Marked read.')).not.toBeNull()
    const retriedCommand = executeReaderCommand.mock.calls[1]![0].command
    expect(retriedCommand).toBe(originalCommand)
    expect(JSON.stringify(retriedCommand)).toBe(JSON.stringify(originalCommand))
    expect(retriedCommand.expected_revision).toBe(0)
  })

  it('preserves a curated-note draft on conflict and refetches the authoritative item', async () => {
    const initial = readerItem({
      revision: 9,
      curated_note: { body_markdown: 'Earlier note', revision: 4, updated_at: '2026-09-03T18:42:00Z' },
      actions: [capability('update_curated_note')],
    })
    const authoritative = readerItem({
      revision: 10,
      curated_note: { body_markdown: 'Concurrent note', revision: 5, updated_at: '2026-09-03T19:00:00Z' },
      actions: [capability('update_curated_note')],
    })
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => {
      throw new ApiError('revision conflict', 409)
    })
    const fetchReaderItem = vi.fn<ApiClient['fetchReaderItem']>(async () => authoritative)
    const onItemChange = renderActions(
      initial,
      clientWithActions({ executeReaderCommand, fetchReaderItem }),
    )

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Curated note' }))
    const editor = screen.getByRole('textbox', { name: /Curated note/ })
    fireEvent.change(editor, { target: { value: 'My unsent revision' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save curated note' }))

    expect(await screen.findByText(/another change won/i)).not.toBeNull()
    await waitFor(() => expect(fetchReaderItem).toHaveBeenCalledWith({ fragmentId: 'fragment-1' }))
    expect(onItemChange).toHaveBeenLastCalledWith(authoritative)
    expect(screen.getByRole('textbox', { name: /Curated note/ })).toHaveProperty(
      'value',
      'My unsent revision',
    )
    expect(executeReaderCommand.mock.calls[0]![0].command).toMatchObject({
      command: 'update_curated_note',
      expected_revision: 9,
      expected_note_revision: 4,
      body_markdown: 'My unsent revision',
    })
  })

  it('shows a known failure and rolls back the optimistic projection', async () => {
    const initial = readerItem({ actions: [capability('mark_read')] })
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => {
      throw new ApiError('Reader command failed', 500)
    })
    const onItemChange = renderActions(initial, clientWithActions({ executeReaderCommand }))

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Mark read' }))

    expect(await screen.findByText('Reader command failed')).not.toBeNull()
    expect(screen.getByRole('status').getAttribute('data-reader-action-state')).toBe('failure')
    expect(onItemChange).toHaveBeenLastCalledWith(initial)
  })

  it('builds note, removal, progress, and unread commands with command-specific revisions', async () => {
    const item = readerItem({
      revision: 14,
      curated_note: { body_markdown: 'Existing', revision: 5, updated_at: '2026-09-03T18:42:00Z' },
      reading_state: {
        ...readerItem().reading_state,
        state: 'in_progress',
        position: { kind: 'article', progress: 0.2 },
        revision: 3,
      },
      actions: [
        capability('remove_tag'),
        capability('append_capture_note'),
        capability('update_curated_note'),
        capability('set_reading_progress'),
        capability('mark_unread'),
      ],
    })
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => item)
    renderActions(item, clientWithActions({ executeReaderCommand }))

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Remove tag' }))
    fireEvent.change(screen.getByRole('combobox', { name: 'Visible tag' }), {
      target: { value: 'systems' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Confirm tag removal' }))
    await screen.findByText('Tag removed.')

    fireEvent.click(screen.getByRole('button', { name: 'Capture note' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Capture note' }), {
      target: { value: 'A retained capture observation' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save capture note' }))
    await screen.findByText('Capture note saved.')

    fireEvent.click(screen.getByRole('button', { name: 'Curated note' }))
    fireEvent.change(screen.getByRole('textbox', { name: /Curated note/ }), {
      target: { value: 'Revised durable note' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save curated note' }))
    await screen.findByText('Curated note updated.')

    fireEvent.click(screen.getByRole('button', { name: 'Reading position' }))
    fireEvent.change(screen.getByRole('spinbutton', { name: /Article progress/ }), {
      target: { value: '0.65' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Save reading position' }))
    await screen.findByText('Reading position saved.')

    fireEvent.click(screen.getByRole('button', { name: 'Mark unread' }))
    await screen.findByText('Marked unread.')

    expect(executeReaderCommand.mock.calls.map(([params]) => params.command)).toEqual([
      expect.objectContaining({ command: 'remove_tag', tag: 'systems', expected_revision: 14 }),
      expect.objectContaining({
        command: 'append_capture_note',
        text: 'A retained capture observation',
        expected_revision: 14,
        annotation_id: expect.stringMatching(/^annotation-reader-command-/),
      }),
      expect.objectContaining({
        command: 'update_curated_note',
        body_markdown: 'Revised durable note',
        expected_revision: 14,
        expected_note_revision: 5,
      }),
      expect.objectContaining({
        command: 'set_reading_progress',
        expected_revision: 3,
        position: { kind: 'article', progress: 0.65 },
      }),
      expect.objectContaining({ command: 'mark_unread', expected_revision: 3 }),
    ])
  })

  it('uses only current media, route, and destination options as effect targets', async () => {
    const item = readerItem({
      revision: 6,
      media: [
        {
          attachment: {
            attachment_id: 'attachment-1',
            fragment_revision_id: 'revision-1',
            media_asset_id: 'media-1',
            role: 'primary',
            position: 0,
          },
          media_asset_id: 'media-1',
          kind: 'video',
          variants: [
            {
              asset_variant_id: 'variant-1',
              kind: 'original',
              custody: 'reference',
              acquisition_state: 'reference_only',
            },
          ],
        },
      ],
      actions: [
        capability('request_asset_acquisition'),
        capability('route'),
        capability('materialize'),
      ],
    })
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => item)
    renderActions(
      item,
      clientWithActions({
        executeReaderCommand,
        fetchRoutes: async () => [
          {
            id: 'route-existing',
            name: 'Existing route',
            match_source: '',
            match_type: '',
            match_entity_kind: '',
            match_entity_value: '',
            destination_id: 'destination-existing',
            auto_route: false,
            confidence_min: 0,
          },
        ],
        fetchDestinations: async () => [
          { id: 'destination-existing', name: 'Existing destination', kind: 'file', config: {} },
        ],
      }),
    )

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Acquire media' }))
    fireEvent.change(screen.getByRole('combobox', { name: 'Captured representation' }), {
      target: { value: 'variant-1' },
    })
    fireEvent.change(screen.getByRole('combobox', { name: 'Requested custody' }), {
      target: { value: 'mirror' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Request acquisition' }))
    await screen.findByText('Media acquisition requested.')
    expect(executeReaderCommand.mock.calls[0]![0].command).toMatchObject({
      expected_revision: 6,
      media_asset_id: 'media-1',
      variant_kind: 'original',
      requested_custody: 'mirror',
    })

    fireEvent.click(screen.getByRole('button', { name: 'Route' }))
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Existing route' })).not.toHaveProperty('value', ''))
    fireEvent.click(screen.getByRole('button', { name: 'Apply route' }))
    await screen.findByText('Route command accepted.')
    expect(executeReaderCommand.mock.calls[1]![0].command).toMatchObject({ route_id: 'route-existing' })

    fireEvent.click(screen.getByRole('button', { name: 'Materialize' }))
    await waitFor(() => expect(screen.getByRole('combobox', { name: 'Existing destination' })).not.toHaveProperty('value', ''))
    fireEvent.click(screen.getByRole('button', { name: 'Run materialization' }))
    await screen.findByText('Materialization command accepted.')
    expect(executeReaderCommand.mock.calls[2]![0].command).toMatchObject({
      destination_id: 'destination-existing',
    })
  })

  it('isolates a lazy route-option failure from unrelated advertised actions', async () => {
    const item = readerItem({ actions: [capability('route'), capability('add_tag')] })
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => item)
    renderActions(
      item,
      clientWithActions({
        executeReaderCommand,
        fetchRoutes: async () => {
          throw new ApiError('Routes are unavailable', 500)
        },
      }),
    )

    await openActions()
    fireEvent.click(screen.getByRole('button', { name: 'Route' }))
    expect(await screen.findByText('Routes are unavailable')).not.toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Add tag' }))
    fireEvent.change(screen.getByPlaceholderText('reader'), { target: { value: 'still-works' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save tag' }))

    expect(await screen.findByText('Tag added.')).not.toBeNull()
    expect(executeReaderCommand).toHaveBeenCalledTimes(1)
    expect(executeReaderCommand.mock.calls[0]![0].command).toMatchObject({
      command: 'add_tag',
      tag: 'still-works',
    })
  })
})
