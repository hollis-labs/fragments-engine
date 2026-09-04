import { AlertTriangle, Clock3, Link2, ShieldX } from 'lucide-react'
import { cn } from '@hollis-labs/sysop-ui'

import { stateDescription, stateLabel, type ResourceState } from './media'

const stateStyles: Record<ResourceState, string> = {
  available: 'border-border bg-panel-2/40 text-text-soft',
  pending: 'border-warning/40 bg-warning/5 text-warning',
  reference_only: 'border-border-strong bg-panel-2/40 text-text-soft',
  failed: 'border-destructive/40 bg-destructive/5 text-destructive',
  unavailable: 'border-border bg-panel-2/30 text-text-subtle',
}

function StateIcon({ state }: { state: ResourceState }) {
  const className = "mt-0.5 h-4 w-4 shrink-0"
  switch (state) {
    case 'pending':
      return <Clock3 className={className} aria-hidden="true" />
    case 'reference_only':
      return <Link2 className={className} aria-hidden="true" />
    case 'failed':
      return <AlertTriangle className={className} aria-hidden="true" />
    default:
      return <ShieldX className={className} aria-hidden="true" />
  }
}

export function ResourceStatePanel({
  state,
  label,
  description,
  compact = false,
  className,
}: {
  state: Exclude<ResourceState, 'available'>
  label?: string
  description?: string
  compact?: boolean
  className?: string
}) {
  return (
    <div
      className={cn(
        'flex items-start gap-2 rounded-md border',
        compact ? 'px-2.5 py-2' : 'px-3 py-3',
        stateStyles[state],
        className,
      )}
      role={state === 'failed' ? 'alert' : 'status'}
      data-resource-state={state}
    >
      <StateIcon state={state} />
      <div className="min-w-0">
        <p className="text-xs font-medium leading-5">{label ?? stateLabel(state)}</p>
        {!compact && (
          <p className="text-xs leading-5 text-text-subtle">
            {description ?? stateDescription(state)}
          </p>
        )}
      </div>
    </div>
  )
}
