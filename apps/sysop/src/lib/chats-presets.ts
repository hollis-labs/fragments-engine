import { parseChatFilters, type ChatFilters } from './chats'

// Presets are non-secret view state kept in this browser only: a name and the
// filters, search text, search mode and sort the owner chose. No chat content.
const KEY = 'sysop:chats:presets:v1'
export const MAX_PRESETS = 30
const MAX_NAME = 40

export interface ChatPreset {
  name: string
  filters: ChatFilters
}

export function cleanPresetName(name: string): string {
  return name.replace(/\s+/g, ' ').trim().slice(0, MAX_NAME)
}

export function parsePresets(raw: unknown): ChatPreset[] {
  if (!Array.isArray(raw)) return []
  const seen = new Set<string>()
  const out: ChatPreset[] = []
  for (const entry of raw) {
    if (typeof entry !== 'object' || entry === null) continue
    const name = cleanPresetName(typeof (entry as { name?: unknown }).name === 'string' ? (entry as { name: string }).name : '')
    if (!name || seen.has(name)) continue
    seen.add(name)
    out.push({ name, filters: parseChatFilters((entry as { filters?: unknown }).filters) })
    if (out.length === MAX_PRESETS) break
  }
  return out
}

/** Add or replace the preset with this name; returns the new list. */
export function upsertPreset(presets: ChatPreset[], name: string, filters: ChatFilters): ChatPreset[] {
  const clean = cleanPresetName(name)
  if (!clean) return presets
  const next = presets.filter((p) => p.name !== clean)
  next.push({ name: clean, filters: { ...filters } })
  return next.slice(-MAX_PRESETS)
}

export function removePreset(presets: ChatPreset[], name: string): ChatPreset[] {
  return presets.filter((p) => p.name !== name)
}

export function loadPresets(): ChatPreset[] {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? parsePresets(JSON.parse(raw)) : []
  } catch {
    return []
  }
}

export function savePresets(presets: ChatPreset[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(presets))
  } catch {
    // Storage may be unavailable or full; presets then last for this visit only.
  }
}
