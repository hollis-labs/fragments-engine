import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import ChatsTable from './ChatsTable'
import type { ChatSession } from '@/lib/chats'

const session = (over: Partial<ChatSession> = {}): ChatSession => ({
  id: 'a'.repeat(64),
  tool: 'claude',
  kind: 'main',
  title: 'Synthetic chat title',
  model: 'synthetic-model',
  project: '/synthetic/alpha',
  started_at: '2026-10-10T10:00:00Z',
  turns: 4,
  tokens: 1500,
  tool_calls: 3,
  errors: 0,
  ...over,
})

describe('ChatsTable', () => {
  it('opens a row by click and keyboard', () => {
    const onOpen = vi.fn()
    render(<ChatsTable items={[session()]} sort="started" desc onSort={() => {}} onOpen={onOpen} />)
    const row = screen.getByRole('link', { name: 'Open Synthetic chat title' })
    fireEvent.click(row)
    fireEvent.keyDown(row, { key: 'Enter' })
    expect(onOpen).toHaveBeenCalledTimes(2)
    expect(onOpen).toHaveBeenCalledWith('a'.repeat(64))
  })

  it('shows compact metrics and marks subagents and failures', () => {
    render(
      <ChatsTable
        items={[session({ kind: 'subagent', errors: 2, snippet: 'matched [zebra] here' })]}
        sort="started"
        desc
        onSort={() => {}}
        onOpen={() => {}}
      />,
    )
    expect(screen.getByText('1.5k')).toBeTruthy()
    expect(screen.getByText('alpha')).toBeTruthy()
    expect(screen.getByText('sub')).toBeTruthy()
    expect(screen.getByText('matched [zebra] here')).toBeTruthy()
    expect(screen.getByText('2').className).toContain('danger')
  })

  it('asks the server to sort by the clicked column and reports direction', () => {
    const onSort = vi.fn()
    render(<ChatsTable items={[session()]} sort="tokens" desc={false} onSort={onSort} onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: /Errors/ }))
    expect(onSort).toHaveBeenCalledWith('errors')
    const header = screen.getByRole('columnheader', { name: /Tokens/ })
    expect(header.getAttribute('aria-sort')).toBe('ascending')
  })
})
