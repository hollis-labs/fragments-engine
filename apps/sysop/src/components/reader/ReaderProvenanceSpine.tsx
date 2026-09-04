import {
  acquisitionPresentation,
  enrichmentPresentation,
  type ReaderAxisPresentation,
} from '@/lib/reader'
import type { ReaderItem } from '@/lib/types'

interface ReaderProvenanceSpineProps {
  item: ReaderItem
}

const SEGMENT_TONES: Record<ReaderAxisPresentation['tone'], string> = {
  quiet: 'bg-text-subtle/35',
  attention: 'bg-status-inbox',
  active: 'bg-status-routed',
  success: 'bg-status-indexed',
  danger: 'bg-danger-soft',
}

export function ReaderProvenanceSpine({ item }: ReaderProvenanceSpineProps) {
  const triageTone: ReaderAxisPresentation['tone'] =
    item.operations.triage.unresolved_count > 0 ? 'attention' : 'quiet'
  const enrichmentTone = enrichmentPresentation(item.operations.enrichment).tone
  const mediaTone = acquisitionPresentation(item.operations.acquisition).tone

  return (
    <div
      className="absolute inset-y-0 left-0 flex w-1 flex-col gap-px overflow-hidden rounded-l-sm"
      aria-hidden="true"
    >
      <span className={`flex-1 ${SEGMENT_TONES[triageTone]}`} />
      <span className={`flex-1 ${SEGMENT_TONES[enrichmentTone]}`} />
      <span className={`flex-1 ${SEGMENT_TONES[mediaTone]}`} />
    </div>
  )
}
