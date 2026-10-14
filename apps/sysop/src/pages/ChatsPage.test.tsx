import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ChatsPage from './ChatsPage'
import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient } from '@/lib/api'
import type { ChatSession } from '@/lib/chats'

const row = (id: string, title: string): ChatSession => ({
  id, tool: 'claude', kind: 'main', title, turns: 1, tokens: 10, tool_calls: 0, errors: 0,
})

function setup() {
  const fetchTranscriptSessions = vi.fn().mockResolvedValue({ items: [row('a'.repeat(64), 'first synthetic')], next_cursor: 'c1' })
  const client = {
    ...apiClient,
    fetchTranscriptSessions,
    fetchTranscriptFacets: vi.fn().mockResolvedValue({ tools: ['claude', 'codex'], models: [], projects: [], total: 7 }),
  } as unknown as typeof apiClient
  render(
    <ApiProvider client={client}>
      <MemoryRouter>
        <ChatsPage />
      </MemoryRouter>
    </ApiProvider>,
  )
  return fetchTranscriptSessions
}

describe('ChatsPage', () => {
  beforeEach(() => localStorage.clear())

  it('loads sessions sorted newest first and pages with the cursor', async () => {
    const fetchSessions = setup()
    await screen.findByText('first synthetic')
    expect(fetchSessions.mock.calls[0][0]).toMatchObject({ sort: 'started', order: 'desc', limit: 50 })
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ cursor: 'c1' }))
  })

  it('applies filters and sort to the query and persists them', async () => {
    const fetchSessions = setup()
    await screen.findByText('first synthetic')
    await screen.findByRole('option', { name: 'codex' })
    fireEvent.change(screen.getByLabelText('Tool'), { target: { value: 'codex' } })
    fireEvent.click(await screen.findByRole('button', { name: /Tokens/ }))
    await waitFor(() =>
      expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ tool: 'codex', sort: 'tokens', order: 'desc' }),
    )
    expect(JSON.parse(localStorage.getItem('sysop:chats:filters:v1') ?? '{}')).toMatchObject({ tool: 'codex', sort: 'tokens' })
  })

  it('sends the search box text as q after a pause', async () => {
    const fetchSessions = setup()
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'zebra' } })
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'zebra' }))
  })
})
