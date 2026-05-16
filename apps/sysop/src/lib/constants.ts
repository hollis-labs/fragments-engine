/**
 * Fragment-status presentation. Colors are built on the theme-aware
 * `--color-status-*` tokens (see index.css) so chips and badges follow the
 * active palette.
 */
export const FRAGMENT_STATUSES = ['inbox', 'routed', 'indexed'] as const

export interface StatusTone {
  bg: string
  text: string
  border: string
  dot: string
}

export const STATUS_TONES: Record<string, StatusTone> = {
  inbox: {
    bg: 'bg-status-inbox/10',
    text: 'text-status-inbox',
    border: 'border-status-inbox/40',
    dot: 'bg-status-inbox',
  },
  routed: {
    bg: 'bg-status-routed/10',
    text: 'text-status-routed',
    border: 'border-status-routed/40',
    dot: 'bg-status-routed',
  },
  indexed: {
    bg: 'bg-status-indexed/10',
    text: 'text-status-indexed',
    border: 'border-status-indexed/40',
    dot: 'bg-status-indexed',
  },
}

export const DEFAULT_STATUS_TONE: StatusTone = {
  bg: 'bg-panel-2',
  text: 'text-text-soft',
  border: 'border-border',
  dot: 'bg-text-subtle',
}

/** CSS colors for the summary-card accent dots (theme-aware). */
export const SUMMARY_ACCENTS = {
  total: 'var(--color-text-subtle)',
  unrouted: 'var(--color-status-inbox)',
  routed: 'var(--color-status-routed)',
  sources: 'var(--color-status-indexed)',
} as const
