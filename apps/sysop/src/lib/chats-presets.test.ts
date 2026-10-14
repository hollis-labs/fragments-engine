import { beforeEach, describe, expect, it } from 'vitest'
import { EMPTY_CHAT_FILTERS } from './chats'
import { loadPresets, parsePresets, removePreset, savePresets, upsertPreset } from './chats-presets'

describe('chat presets', () => {
  beforeEach(() => localStorage.clear())

  it('stores the whole view, replaces by name, and removes', () => {
    const view = { ...EMPTY_CHAT_FILTERS, search: 'ferry', mode: 'summary' as const, tag: 'ops', sort: 'tokens' as const }
    let list = upsertPreset([], '  Ops   ferry ', view)
    expect(list).toEqual([{ name: 'Ops ferry', filters: view }])
    list = upsertPreset(list, 'Ops ferry', { ...view, search: 'other' })
    expect(list).toHaveLength(1)
    expect(list[0].filters.search).toBe('other')
    expect(upsertPreset(list, '   ', view)).toBe(list)
    expect(removePreset(list, 'Ops ferry')).toEqual([])
  })

  it('round-trips through storage and tolerates junk', () => {
    savePresets(upsertPreset([], 'a', { ...EMPTY_CHAT_FILTERS, mode: 'hybrid' }))
    expect(loadPresets()[0].filters.mode).toBe('hybrid')
    localStorage.setItem('sysop:chats:presets:v1', '{not json')
    expect(loadPresets()).toEqual([])
    expect(parsePresets([null, 3, { name: '' }, { name: 'ok', filters: { mode: 'bogus' } }, { name: 'ok' }])).toEqual([
      { name: 'ok', filters: EMPTY_CHAT_FILTERS },
    ])
  })
})
