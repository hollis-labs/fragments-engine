import { DEFAULT_STATUS_TONE, STATUS_TONES } from '@/lib/constants'

interface StatusBadgeProps {
  status: string
  className?: string
}

/** Fragment-status pill — mirrors Torque's StatusBadge, themed via status tokens. */
export function StatusBadge({ status, className = '' }: StatusBadgeProps) {
  const tone = STATUS_TONES[(status || '').toLowerCase()] ?? DEFAULT_STATUS_TONE

  return (
    <span
      className={`inline-flex items-center gap-2 rounded border px-2 py-1 text-[11px] uppercase tracking-[0.14em] ${tone.border} ${tone.bg} ${tone.text} ${className}`}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${tone.dot}`} />
      <span>{status || 'unknown'}</span>
    </span>
  )
}
