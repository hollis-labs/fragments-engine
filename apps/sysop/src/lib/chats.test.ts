import { describe, expect, it } from 'vitest'
import {
  EMPTY_CHAT_FILTERS,
  activeChatFilterCount,
  flattenTree,
  formatBytes,
  formatTokens,
  highlightParts,
  noticeText,
  parseChatFilters,
  projectName,
  resolveSearchMode,
  type ChatTreeNode,
} from './chats'

describe('chat filters', () => {
  it('falls back to defaults for junk and keeps valid values', () => {
    expect(parseChatFilters(null)).toEqual(EMPTY_CHAT_FILTERS)
    expect(parseChatFilters({ sort: 'nope', kind: 'x', errors: 'y', desc: 'z' })).toEqual(EMPTY_CHAT_FILTERS)
    const f = parseChatFilters({ tool: 'codex', sort: 'tokens', desc: false, kind: 'subagent', errors: 'errors' })
    expect(f).toMatchObject({ tool: 'codex', sort: 'tokens', desc: false, kind: 'subagent', errors: 'errors' })
  })

  it('counts narrowing filters but not search or sort', () => {
    expect(activeChatFilterCount({ ...EMPTY_CHAT_FILTERS, search: 'x', sort: 'turns' })).toBe(0)
    expect(activeChatFilterCount({ ...EMPTY_CHAT_FILTERS, tool: 'claude', kind: 'main', since: '2026-01-01' })).toBe(3)
    expect(activeChatFilterCount({ ...EMPTY_CHAT_FILTERS, tag: 'alpha', enriched: 'yes' })).toBe(2)
  })
})

describe('chat formatting', () => {
  it('formats tokens, bytes and project names', () => {
    expect(formatTokens(950)).toBe('950')
    expect(formatTokens(1500)).toBe('1.5k')
    expect(formatTokens(2_500_000)).toBe('2.5M')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.0 KB')
    expect(projectName('/synthetic/alpha/')).toBe('alpha')
    expect(projectName(undefined)).toBe('')
  })

  it('flattens the subagent tree depth-first', () => {
    const n = (id: string, children: ChatTreeNode[] = []) =>
      ({ id, children, tool: 'claude', kind: 'subagent', title: id, turns: 0, tokens: 0, tool_calls: 0, errors: 0 }) as ChatTreeNode
    const flat = flattenTree([n('a', [n('a1')]), n('b')])
    expect(flat.map((x) => [x.node.id, x.depth])).toEqual([['a', 0], ['a1', 1], ['b', 0]])
  })
})

describe('search modes', () => {
  it('picks hybrid for auto only when semantic search works', () => {
    expect(resolveSearchMode('auto', true)).toBe('hybrid')
    expect(resolveSearchMode('auto', false)).toBe('text')
    expect(resolveSearchMode('semantic', false)).toBe('semantic')
  })

  it('reads the persisted mode defensively', () => {
    expect(parseChatFilters({ mode: 'summary' }).mode).toBe('summary')
    expect(parseChatFilters({ mode: 'bogus' }).mode).toBe('auto')
    expect(activeChatFilterCount({ ...EMPTY_CHAT_FILTERS, mode: 'summary' })).toBe(0)
  })

  it('splits highlighted excerpts and explains notices', () => {
    expect(highlightParts('a [b] c [d]')).toEqual([
      { text: 'a ', hit: false }, { text: 'b', hit: true }, { text: ' c ', hit: false }, { text: 'd', hit: true },
    ])
    expect(highlightParts('no marks [open')).toEqual([{ text: 'no marks [open', hit: false }])
    expect(noticeText('model_mismatch')).toMatch(/different embedding model/)
    expect(noticeText('unknown_code')).toBeUndefined()
    expect(noticeText(undefined)).toBeUndefined()
  })
})
