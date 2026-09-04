import type { ReaderAxisPresentation } from '@/lib/reader'
import {
  acquisitionPresentation,
  effectPresentation,
  enrichmentPresentation,
} from '@/lib/reader'
import type { ReaderItem } from '@/lib/types'

interface ReaderStateSummaryProps {
  item: ReaderItem
  compact?: boolean
}

const TONE_CLASSES: Record<ReaderAxisPresentation['tone'], string> = {
  quiet: 'text-text-subtle',
  attention: 'text-status-inbox',
  active: 'text-status-routed',
  success: 'text-status-indexed',
  danger: 'text-danger-soft',
}

function StateDatum({ label, state }: { label: string; state: ReaderAxisPresentation }) {
  return (
    <div className="min-w-0">
      <dt className="text-[11px] leading-4 text-text-subtle">{label}</dt>
      <dd className={`truncate text-[12px] font-medium leading-5 ${TONE_CLASSES[state.tone]}`}>
        {state.value}
      </dd>
    </div>
  )
}

export function ReaderStateSummary({ item, compact = false }: ReaderStateSummaryProps) {
  const triage: ReaderAxisPresentation =
    item.operations.triage.unresolved_count > 0
      ? {
          value: `${item.operations.triage.unresolved_count} open`,
          tone: 'attention',
        }
      : { value: 'Clear', tone: 'quiet' }

  return (
    <dl
      className={`grid gap-x-5 gap-y-3 ${
        compact ? 'grid-cols-2 sm:grid-cols-5' : 'grid-cols-2 border-y border-border-soft py-4 sm:grid-cols-5'
      }`}
      aria-label="Fragment processing states"
    >
      <StateDatum label="Triage" state={triage} />
      <StateDatum label="Routing" state={effectPresentation(item.operations.routing.state)} />
      <StateDatum
        label="Materialization"
        state={effectPresentation(item.operations.materialization.state)}
      />
      <StateDatum label="Enrichment" state={enrichmentPresentation(item.operations.enrichment)} />
      <StateDatum label="Media" state={acquisitionPresentation(item.operations.acquisition)} />
    </dl>
  )
}
