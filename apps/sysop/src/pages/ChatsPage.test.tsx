import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ChatsPage from './ChatsPage'
import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient } from '@/lib/api'
import type { ChatSession } from '@/lib/chats'

const row = (id: string, title: string): ChatSession => ({
  id, tool: 'claude', kind: 'main', title, turns: 1, tokens: 10, tool_calls: 0, errors: 0,
})

function setup(
  sessionsResult: unknown = { items: [row('a'.repeat(64), 'first synthetic')], next_cursor: 'c1' },
  status: unknown = { enabled: true, available: true, semantic: false, auth: 'none' },
) {
  const fetchTranscriptSessions = vi.fn().mockResolvedValue(sessionsResult)
  const fetchTranscriptProjects = vi.fn().mockResolvedValue([{ project: '/synthetic/deep/project-0699', sessions: 3 }])
  const client = {
    ...apiClient,
    fetchTranscriptSessions,
    fetchTranscriptFacets: vi.fn().mockResolvedValue({ tools: ['claude', 'codex'], models: [], project_count: 835, total: 7, enriched: 4, tag_counts: [{ tag: 'ops', count: 3 }, { tag: 'infra', count: 1 }] }),
    fetchTranscriptProjects,
    fetchTranscriptStatus: vi.fn().mockResolvedValue(status),
  } as unknown as typeof apiClient
  render(
    <ApiProvider client={client}>
      <MemoryRouter>
        <ChatsPage />
      </MemoryRouter>
    </ApiProvider>,
  )
  return { fetchTranscriptSessions, fetchTranscriptProjects }
}

describe('ChatsPage', () => {
  beforeEach(() => localStorage.clear())

  it('loads sessions sorted newest first and pages with the cursor', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    expect(fetchSessions.mock.calls[0][0]).toMatchObject({ sort: 'started', order: 'desc', limit: 50 })
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ cursor: 'c1' }))
  })

  it('applies filters and sort to the query and persists them', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    await screen.findByRole('option', { name: 'codex' })
    fireEvent.change(screen.getByLabelText('Tool'), { target: { value: 'codex' } })
    fireEvent.click(await screen.findByRole('button', { name: /Tokens/ }))
    await waitFor(() =>
      expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ tool: 'codex', sort: 'tokens', order: 'desc' }),
    )
    expect(JSON.parse(localStorage.getItem('sysop:chats:filters:v1') ?? '{}')).toMatchObject({ tool: 'codex', sort: 'tokens' })
  })

  it('filters by enrichment state', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Enriched'), { target: { value: 'yes' } })
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ has_enrichment: 'true' }))
  })

  it('suggests projects as the owner types, beyond any fixed list', async () => {
    const { fetchTranscriptProjects } = setup()
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Project'), { target: { value: 'project-0699' } })
    await waitFor(() => expect(fetchTranscriptProjects.mock.calls.at(-1)?.[0]).toBe('project-0699'))
    await waitFor(() => expect(document.querySelector('#chat-projects option[value="/synthetic/deep/project-0699"]')).not.toBeNull())
  })

  it('tells the owner to refine a search that was too broad', async () => {
    setup({ items: [], hint: 'too_broad' })
    expect(await screen.findByText('That search is too broad')).toBeTruthy()
  })

  it('sends the search box text as q after a pause', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'zebra' } })
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'zebra' }))
  })

  it('defaults to hybrid when semantic search works, and to text when it does not', async () => {
    const on = setup(undefined, { enabled: true, available: true, semantic: true, auth: 'none' })
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'ferry' } })
    await waitFor(() => expect(on.fetchTranscriptSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'ferry', mode: 'hybrid' }))
    cleanup()
    const off = setup()
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'ferry' } })
    await waitFor(() => expect(off.fetchTranscriptSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'ferry', mode: 'text' }))
  })

  it('sends the chosen mode, persists it, and sends no mode without a query', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    expect(fetchSessions.mock.calls[0][0].mode).toBeUndefined()
    fireEvent.change(screen.getByLabelText('Search mode'), { target: { value: 'summary' } })
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'quartz' } })
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'quartz', mode: 'summary' }))
    expect(JSON.parse(localStorage.getItem('sysop:chats:filters:v1') ?? '{}')).toMatchObject({ mode: 'summary' })
  })

  it('shows which field matched with a highlighted excerpt, and explains a semantic fallback', async () => {
    setup({
      items: [{ ...row('b'.repeat(64), 'matched chat'), match: { field: 'decisions', excerpt: 'Adopt a bounded [worker] pool' } }],
      notice: 'semantic_unavailable',
      mode: 'hybrid',
    })
    await screen.findByText('matched chat')
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'worker' } })
    await waitFor(() => expect(screen.getByText('decision')).toBeTruthy())
    expect(screen.getByText('worker').tagName).toBe('MARK')
    expect((await screen.findByRole('status')).textContent).toMatch(/embedding server/)
  })

  it('offers an enriched-only quick filter and tag chips with counts', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    fireEvent.click(await screen.findByRole('button', { name: /Enriched only \(4\)/ }))
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ has_enrichment: 'true' }))
    fireEvent.click(screen.getByRole('button', { name: /ops 3/ }))
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ tag: 'ops' }))
    fireEvent.click(screen.getByRole('button', { name: /ops 3/ }))
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0].tag).toBe(''))
  })

  it('saves a named view and applies it again, search and mode included', async () => {
    const { fetchTranscriptSessions: fetchSessions } = setup()
    await screen.findByText('first synthetic')
    fireEvent.change(screen.getByLabelText('Search mode'), { target: { value: 'summary' } })
    fireEvent.change(screen.getByLabelText('Search chats'), { target: { value: 'quartz' } })
    fireEvent.change(screen.getByLabelText('Tool'), { target: { value: 'codex' } })
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'quartz', tool: 'codex' }))
    fireEvent.click(screen.getByRole('button', { name: 'Save view' }))
    fireEvent.change(screen.getByLabelText('Preset name'), { target: { value: 'Quartz work' } })
    fireEvent.keyDown(screen.getByLabelText('Preset name'), { key: 'Enter' })
    expect(JSON.parse(localStorage.getItem('sysop:chats:presets:v1') ?? '[]')[0]).toMatchObject({
      name: 'Quartz work', filters: { search: 'quartz', mode: 'summary', tool: 'codex' },
    })
    fireEvent.change(screen.getByLabelText('Tool'), { target: { value: '' } })
    fireEvent.change(screen.getByLabelText('Search mode'), { target: { value: 'text' } })
    fireEvent.change(screen.getByLabelText('Presets'), { target: { value: 'Quartz work' } })
    await waitFor(() => expect(fetchSessions.mock.calls.at(-1)?.[0]).toMatchObject({ q: 'quartz', mode: 'summary', tool: 'codex' }))
    expect((screen.getByLabelText('Search chats') as HTMLInputElement).value).toBe('quartz')
  })

  it('navigates with j/k, opens with Enter, and focuses search with /', async () => {
    setup({ items: [row('a'.repeat(64), 'first synthetic'), row('b'.repeat(64), 'second synthetic')] })
    await screen.findByText('first synthetic')
    fireEvent.keyDown(document.body, { key: 'j' })
    expect(document.querySelector('tr[data-selected="true"]')?.textContent).toContain('first synthetic')
    fireEvent.keyDown(document.body, { key: 'j' })
    expect(document.querySelector('tr[data-selected="true"]')?.textContent).toContain('second synthetic')
    fireEvent.keyDown(document.body, { key: 'k' })
    expect(document.querySelector('tr[data-selected="true"]')?.textContent).toContain('first synthetic')
    fireEvent.keyDown(document.body, { key: 'Escape' })
    expect(document.querySelector('tr[data-selected="true"]')).toBeNull()
    fireEvent.keyDown(document.body, { key: '/' })
    expect(document.activeElement).toBe(screen.getByLabelText('Search chats'))
    // Typing j in the search box must not move the selection.
    fireEvent.keyDown(screen.getByLabelText('Search chats'), { key: 'j' })
    expect(document.querySelector('tr[data-selected="true"]')).toBeNull()
  })
})
