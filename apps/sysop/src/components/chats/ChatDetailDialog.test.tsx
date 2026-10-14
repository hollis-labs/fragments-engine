import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatDetailDialog } from './ChatDetailDialog'
import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient } from '@/lib/api'
import type { ChatDetail, ChatSession } from '@/lib/chats'

const base = (over: Partial<ChatSession> = {}): ChatSession => ({
  id: 'a'.repeat(64), tool: 'claude', kind: 'main', title: 'Synthetic chat', turns: 2, tokens: 150, tool_calls: 1, errors: 1, ...over,
})

const detail = (over: Partial<ChatDetail> = {}): ChatDetail => ({
  session: base(),
  usage: { requests: 2, input_tokens: 1000, output_tokens: 500, cache_read_tokens: 0, cache_creation_tokens: 0 },
  turns: [
    { seq: 1, role: 'user', text: 'synthetic question', event_id: 'e1' },
    {
      seq: 2, role: 'assistant', text: 'synthetic answer', event_id: 'e2', has_thinking: true, thinking_bytes: 2048,
      tool_calls: [{ name: 'Read', status: 'ok', result_bytes: 10 }, { name: 'Bash', status: 'error', result_bytes: 3 }],
    },
  ],
  turn_total: 2,
  subagents: [{ ...base({ id: 'b'.repeat(64), kind: 'subagent', title: 'sub work', agent_type: 'explore' }), children: [] }],
  ...over,
})

function renderDialog(fetchSession: ReturnType<typeof vi.fn>, onOpenSession = vi.fn()) {
  const client = { ...apiClient, fetchTranscriptSession: fetchSession } as unknown as typeof apiClient
  render(
    <ApiProvider client={client}>
      <ChatDetailDialog sessionId={'a'.repeat(64)} onClose={() => {}} onOpenSession={onOpenSession} />
    </ApiProvider>,
  )
  return onOpenSession
}

describe('ChatDetailDialog', () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('lists tool results that belong to no turn in a footer', async () => {
    renderDialog(vi.fn().mockResolvedValue(detail({ unattached_calls: [{ name: 'Grep', status: 'ok', result_bytes: 7 }] })))
    await screen.findByText('synthetic answer')
    fireEvent.click(screen.getByRole('button', { name: /1 unattached tool result/ }))
    expect(screen.getByText('Grep')).toBeTruthy()
  })

  it('shows turns, usage, thinking as presence only, and collapsed tool calls', async () => {
    renderDialog(vi.fn().mockResolvedValue(detail()))
    await screen.findByText('synthetic answer')
    expect(screen.getByLabelText('Usage totals').textContent).toContain('1.0k')
    expect(screen.getByText(/thinking · 2.0 KB/)).toBeTruthy()
    expect(screen.queryByText('Read')).toBeNull()
    const toggle = screen.getByRole('button', { name: /2 tool calls/ })
    expect(toggle.textContent).toContain('1 failed')
    fireEvent.click(toggle)
    expect(screen.getByText('Read')).toBeTruthy()
    expect(screen.getByText('Bash')).toBeTruthy()
  })

  it('navigates the subagent tree', async () => {
    const onOpenSession = renderDialog(vi.fn().mockResolvedValue(detail()))
    fireEvent.click(await screen.findByRole('button', { name: /explore · sub work/ }))
    expect(onOpenSession).toHaveBeenCalledWith('b'.repeat(64))
  })

  it('renders enrichment only when present and scrolls to a cited turn', async () => {
    const withEnrichment = detail({
      enrichment: { summary: 'synthetic summary', decisions: [{ text: 'chose X', turns: [2] }] },
    })
    renderDialog(vi.fn().mockResolvedValue(withEnrichment))
    await screen.findByText('synthetic summary')
    fireEvent.click(screen.getByRole('button', { name: '#2' }))
    expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
  })

  it('has no enrichment section without enrichment', async () => {
    renderDialog(vi.fn().mockResolvedValue(detail()))
    await screen.findByText('synthetic answer')
    expect(screen.queryByLabelText('Enrichment')).toBeNull()
  })

  it('loads further turn pages on demand', async () => {
    const first = detail({ turn_total: 3, next_turn: 2 })
    const second = detail({ turns: [{ seq: 3, role: 'user', text: 'third synthetic turn', event_id: 'e3' }], turn_total: 3 })
    const fetchSession = vi.fn().mockResolvedValueOnce(first).mockResolvedValueOnce(second)
    renderDialog(fetchSession)
    fireEvent.click(await screen.findByRole('button', { name: /Load more \(2 of 3\)/ }))
    await waitFor(() => expect(screen.getByText('third synthetic turn')).toBeTruthy())
    expect(fetchSession).toHaveBeenLastCalledWith('a'.repeat(64), { turn_offset: 2, turn_limit: 200 })
  })

  it('shows an error without content', async () => {
    renderDialog(vi.fn().mockRejectedValue(new Error('boom')))
    expect(await screen.findByText('boom')).toBeTruthy()
  })
})
