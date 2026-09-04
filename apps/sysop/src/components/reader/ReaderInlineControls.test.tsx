import { useState, type ReactNode } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient, type ApiClient } from '@/lib/api'
import type { ReaderCommandName, ReaderItem } from '@/lib/types'
import { readerItem } from '@/test/reader-fixture'
import { ReaderNoteEditor } from './ReaderNotes'
import { ReaderTags } from './ReaderTags'

const schema =
  'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json'

function withCommands(item: ReaderItem, ...commands: ReaderCommandName[]): ReaderItem {
  return {
    ...item,
    actions: commands.map((command) => ({
      command,
      input_schema: `${schema}#/$defs/${command}`,
      expected_revision_required: true,
    })),
  }
}

function renderControl(
  initial: ReaderItem,
  client: ApiClient,
  renderChild: (item: ReaderItem, onChange: (next: ReaderItem) => void) => ReactNode,
) {
  function Harness() {
    const [item, setItem] = useState(initial)
    return renderChild(item, setItem)
  }
  return render(<ApiProvider client={client}><Harness /></ApiProvider>)
}

afterEach(() => {
  vi.useRealTimers()
})

describe('Reader inline controls', () => {
  it('adds and removes tags without opening the legacy action tray', async () => {
    const initial = withCommands(readerItem(), 'add_tag', 'remove_tag')
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async ({ command }) => {
      if (command.command === 'add_tag') {
        return withCommands({
          ...initial,
          revision: 3,
          tags: { ...initial.tags, combined: [...initial.tags.combined, command.tag] },
        }, 'add_tag', 'remove_tag')
      }
      if (command.command === 'remove_tag') {
        return withCommands({
          ...initial,
          revision: 4,
          tags: { ...initial.tags, combined: initial.tags.combined.filter((tag) => tag !== command.tag) },
        }, 'add_tag', 'remove_tag')
      }
      return initial
    })
    renderControl(
      initial,
      { ...apiClient, executeReaderCommand },
      (item, onChange) => <ReaderTags item={item} onItemChange={onChange} />,
    )

    fireEvent.click(screen.getByRole('button', { name: 'Add tag' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'New tag' }), { target: { value: 'review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save tag' }))
    await waitFor(() => expect(executeReaderCommand).toHaveBeenCalledTimes(1))
    expect(executeReaderCommand.mock.calls[0]![0].command).toMatchObject({ command: 'add_tag', tag: 'review' })

    fireEvent.click(screen.getByRole('button', { name: 'Remove tag systems' }))
    await waitFor(() => expect(executeReaderCommand).toHaveBeenCalledTimes(2))
    expect(executeReaderCommand.mock.calls[1]![0].command).toMatchObject({ command: 'remove_tag', tag: 'systems' })
    expect(screen.queryByRole('button', { name: 'Open Reader actions' })).toBeNull()
  })

  it('autosaves a curated note after a short pause', async () => {
    vi.useFakeTimers()
    const initial = withCommands(readerItem({
      curated_note: { body_markdown: 'Starting note', revision: 2, updated_at: '2026-09-04T00:00:00Z' },
    }), 'update_curated_note')
    const saved = withCommands({
      ...initial,
      revision: 3,
      curated_note: { body_markdown: 'Updated inline', revision: 3, updated_at: '2026-09-04T00:01:00Z' },
    }, 'update_curated_note')
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => saved)
    renderControl(
      initial,
      { ...apiClient, executeReaderCommand },
      (item, onChange) => <ReaderNoteEditor item={item} onItemChange={onChange} kind="curated" />,
    )

    fireEvent.change(screen.getByRole('textbox', { name: 'Curated note' }), {
      target: { value: 'Updated inline' },
    })
    expect(executeReaderCommand).not.toHaveBeenCalled()
    await act(async () => vi.advanceTimersByTimeAsync(760))

    expect(executeReaderCommand).toHaveBeenCalledTimes(1)
    expect(executeReaderCommand.mock.calls[0]![0].command).toMatchObject({
      command: 'update_curated_note',
      expected_note_revision: 2,
      body_markdown: 'Updated inline',
    })
  })

  it('appends a capture note when the inline field loses focus', async () => {
    const initial = withCommands(readerItem(), 'append_capture_note')
    const saved = withCommands({
      ...initial,
      annotations: [{
        annotation_id: 'annotation-saved',
        capture_id: 'capture-1',
        kind: 'capture_note',
        text: 'Check this claim',
        captured_at: '2026-09-04T00:00:00Z',
      }],
    }, 'append_capture_note')
    const executeReaderCommand = vi.fn<ApiClient['executeReaderCommand']>(async () => saved)
    renderControl(
      initial,
      { ...apiClient, executeReaderCommand },
      (item, onChange) => <ReaderNoteEditor item={item} onItemChange={onChange} kind="capture" />,
    )

    const editor = screen.getByRole('textbox', { name: 'Capture note' })
    fireEvent.change(editor, { target: { value: 'Check this claim' } })
    fireEvent.blur(editor)

    await waitFor(() => expect(executeReaderCommand).toHaveBeenCalledTimes(1))
    expect(executeReaderCommand.mock.calls[0]![0].command).toMatchObject({
      command: 'append_capture_note',
      text: 'Check this claim',
    })
    expect(await screen.findByText('Check this claim')).toBeTruthy()
  })
})
