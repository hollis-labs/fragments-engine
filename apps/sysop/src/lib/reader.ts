import type {
  ReaderAcquisitionState,
  ReaderCapabilityCoverage,
  ReaderEffectState,
  ReaderItem,
  ReaderScope,
} from './types'

export const READER_SCOPES: readonly ReaderScope[] = ['inbox', 'library', 'all']

export function isReaderScope(value: string | null): value is ReaderScope {
  return value !== null && READER_SCOPES.includes(value as ReaderScope)
}

export function readerScopeLabel(scope: ReaderScope): string {
  return scope[0].toUpperCase() + scope.slice(1)
}

export function boundedReaderText(value: string, limit = 360): string {
  const normalized = value.replace(/\s+/g, ' ').trim()
  const runes = Array.from(normalized)
  if (runes.length <= limit) return normalized
  return `${runes.slice(0, Math.max(0, limit - 1)).join('').trimEnd()}…`
}

export function sourceLabel(item: ReaderItem): string {
  const provider = item.source.provider.replaceAll('_', ' ').trim()
  return provider ? provider[0].toUpperCase() + provider.slice(1) : 'Local source'
}

export function sourceHost(item: ReaderItem): string | undefined {
  const href = safeReaderSourceHref(item)
  if (!href) return undefined
  try {
    return new URL(href).hostname.replace(/^www\./, '')
  } catch {
    return undefined
  }
}

/** Only browser-safe source URLs may cross into an anchor href. */
export function safeReaderSourceHref(item: ReaderItem): string | undefined {
  for (const raw of [item.source.canonical_url, item.source.submitted_url]) {
    if (!raw) continue
    try {
      const parsed = new URL(raw)
      if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') continue
      if (parsed.username || parsed.password) continue
      return parsed.href
    } catch {
      continue
    }
  }
  return undefined
}

export interface ReaderAxisPresentation {
  value: string
  tone: 'quiet' | 'attention' | 'active' | 'success' | 'danger'
}

export function effectPresentation(state: ReaderEffectState): ReaderAxisPresentation {
  switch (state) {
    case 'pending':
      return { value: 'Pending', tone: 'active' }
    case 'partial':
      return { value: 'Partial', tone: 'attention' }
    case 'failed':
      return { value: 'Failed', tone: 'danger' }
    case 'succeeded':
      return { value: 'Complete', tone: 'success' }
    case 'none':
      return { value: 'None', tone: 'quiet' }
  }
}

export function enrichmentPresentation(
  coverage: ReaderCapabilityCoverage[],
): ReaderAxisPresentation {
  if (coverage.length === 0) return { value: 'Not requested', tone: 'quiet' }

  const counts = new Map<string, number>()
  for (const item of coverage) counts.set(item.state, (counts.get(item.state) ?? 0) + 1)
  const pending = counts.get('pending') ?? 0
  const failed = counts.get('failed') ?? 0
  const incomplete = (counts.get('missing') ?? 0) + (counts.get('stale') ?? 0)
  const parts = [
    pending > 0 ? `${pending} pending` : '',
    failed > 0 ? `${failed} failed` : '',
    incomplete > 0 ? `${incomplete} incomplete` : '',
  ].filter(Boolean)

  if (parts.length > 1) return { value: parts.join(' · '), tone: 'attention' }
  if (pending > 0) return { value: parts[0], tone: 'active' }
  if (failed > 0) return { value: parts[0], tone: 'danger' }
  if (incomplete > 0) return { value: parts[0], tone: 'attention' }
  return { value: 'Complete', tone: 'success' }
}

export function acquisitionPresentation(
  acquisition: Record<ReaderAcquisitionState, number>,
): ReaderAxisPresentation {
  const { pending, available, reference_only: referenceOnly, failed } = acquisition
  const total = pending + available + referenceOnly + failed
  if (total === 0) return { value: 'No media', tone: 'quiet' }
  const parts = [
    failed > 0 ? `${failed} failed` : '',
    pending > 0 ? `${pending} pending` : '',
    available > 0 ? `${available} local` : '',
    referenceOnly > 0 ? `${referenceOnly} referenced` : '',
  ].filter(Boolean)
  if (failed > 0 && parts.length > 1) {
    return { value: parts.join(' · '), tone: 'attention' }
  }
  if (failed > 0) return { value: `${failed} failed`, tone: 'danger' }
  if (pending > 0 && parts.length > 1) {
    return { value: parts.join(' · '), tone: 'attention' }
  }
  if (pending > 0) return { value: `${pending} pending`, tone: 'active' }
  if (referenceOnly > 0 && available > 0) {
    return { value: `${available} local · ${referenceOnly} referenced`, tone: 'success' }
  }
  if (referenceOnly > 0) return { value: `${referenceOnly} referenced`, tone: 'quiet' }
  return { value: `${available} available`, tone: 'success' }
}

export function readingStateLabel(state: ReaderItem['reading_state']['state']): string {
  switch (state) {
    case 'in_progress':
      return 'In progress'
    case 'read':
      return 'Read'
    case 'unread':
      return 'Unread'
  }
}
