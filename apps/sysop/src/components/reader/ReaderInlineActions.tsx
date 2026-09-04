import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  BookOpenCheck,
  Check,
  Circle,
  Database,
  LoaderCircle,
  MapPin,
  Route as RouteIcon,
} from 'lucide-react'
import {
  Button,
  Input,
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
  cn,
} from '@hollis-labs/sysop-ui'

import { useApi } from '@/hooks/useApi'
import {
  createReaderIntentIdentity,
  hasReaderCommand,
  parseReaderProgressDraft,
  progressDraftForItem,
  readerCommandBase,
  type ReaderProgressDraft,
} from '@/lib/reader-actions'
import { readingStateLabel } from '@/lib/reader'
import type {
  Destination,
  ReaderAssetVariant,
  ReaderCommand,
  ReaderItem,
  Route,
} from '@/lib/types'
import { useReaderCommandExecution } from './useReaderCommandExecution'

type EditorCommand = 'set_reading_progress' | 'request_asset_acquisition' | 'route' | 'materialize'

interface ReaderActionPillProps {
  item: ReaderItem
  onItemChange: (item: ReaderItem) => void
  command: EditorCommand
  compact?: boolean
}

interface AcquisitionChoice {
  key: string
  mediaAssetId: string
  variantKind: ReaderAssetVariant['kind']
  label: string
}

const selectClass =
  'min-h-11 w-full rounded-sm border border-input bg-bg px-3 text-[13px] text-text outline-none focus-visible:ring-2 focus-visible:ring-ring'
const fieldLabelClass = 'text-[12px] font-medium text-text-soft'

const ACTION_META = {
  set_reading_progress: { label: 'Position', title: 'Reading position', icon: MapPin },
  request_asset_acquisition: { label: 'Load media', title: 'Load media', icon: Database },
  route: { label: 'Route', title: 'Route fragment', icon: RouteIcon },
  materialize: { label: 'Materialize', title: 'Materialize fragment', icon: Database },
} satisfies Record<EditorCommand, { label: string; title: string; icon: typeof MapPin }>

export function ReaderActionPill({
  item,
  onItemChange,
  command,
  compact = false,
}: ReaderActionPillProps) {
  const api = useApi()
  const [open, setOpen] = useState(false)
  const [progressDraft, setProgressDraft] = useState<ReaderProgressDraft>(() =>
    progressDraftForItem(item),
  )
  const [acquisitionKey, setAcquisitionKey] = useState('')
  const [custody, setCustody] = useState<'cache' | 'mirror' | 'adopted'>('cache')
  const [routeId, setRouteId] = useState('')
  const [destinationId, setDestinationId] = useState('')
  const [routes, setRoutes] = useState<Route[]>([])
  const [destinations, setDestinations] = useState<Destination[]>([])
  const [optionsError, setOptionsError] = useState<string>()
  const { execute, feedback, pending } = useReaderCommandExecution(item, onItemChange)
  const available = hasReaderCommand(item, command)
  const meta = ACTION_META[command]
  const Icon = meta.icon

  const acquisitionChoices = useMemo<AcquisitionChoice[]>(() => {
    const choices: AcquisitionChoice[] = []
    for (const media of item.media) {
      for (const variant of media.variants) {
        if (variant.acquisition_state === 'available' || variant.acquisition_state === 'pending') continue
        choices.push({
          key: variant.asset_variant_id,
          mediaAssetId: media.media_asset_id,
          variantKind: variant.kind,
          label: `${media.kind} · ${variant.kind} · ${variant.acquisition_state.replace('_', ' ')}`,
        })
      }
    }
    return choices.slice(0, 200)
  }, [item.media])

  function changeOpen(nextOpen: boolean) {
    if (nextOpen) {
      if (command === 'set_reading_progress') setProgressDraft(progressDraftForItem(item))
      if (command === 'request_asset_acquisition') {
        setAcquisitionKey((value) => value || acquisitionChoices[0]?.key || '')
      }
    }
    setOpen(nextOpen)
  }

  useEffect(() => {
    if (!open || command !== 'route' || routes.length > 0 || optionsError) return
    let current = true
    void api.fetchRoutes().then((items) => {
      if (!current) return
      const bounded = items.slice(0, 200)
      setRoutes(bounded)
      setRouteId((value) => value || bounded[0]?.id || '')
    }).catch((reason: unknown) => {
      if (current) setOptionsError(reason instanceof Error ? reason.message : 'Routes could not load.')
    })
    return () => { current = false }
  }, [api, command, open, optionsError, routes.length])

  useEffect(() => {
    if (!open || command !== 'materialize' || destinations.length > 0 || optionsError) return
    let current = true
    void api.fetchDestinations().then((items) => {
      if (!current) return
      const bounded = items.slice(0, 200)
      setDestinations(bounded)
      setDestinationId((value) => value || bounded[0]?.id || '')
    }).catch((reason: unknown) => {
      if (current) setOptionsError(reason instanceof Error ? reason.message : 'Destinations could not load.')
    })
    return () => { current = false }
  }, [api, command, destinations.length, open, optionsError])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!available || pending) return
    const identity = createReaderIntentIdentity()
    const base = readerCommandBase(item, command, identity)
    let built: ReaderCommand | undefined

    if (command === 'set_reading_progress') {
      const result = parseReaderProgressDraft(progressDraft)
      if (result.ok) built = { ...base, command, position: result.position }
    } else if (command === 'request_asset_acquisition') {
      const choice = acquisitionChoices.find((candidate) => candidate.key === acquisitionKey)
      if (choice) {
        built = {
          ...base,
          command,
          media_asset_id: choice.mediaAssetId,
          variant_kind: choice.variantKind,
          requested_custody: custody,
        }
      }
    } else if (command === 'route' && routeId) {
      built = { ...base, command, route_id: routeId }
    } else if (command === 'materialize' && destinationId) {
      built = { ...base, command, destination_id: destinationId }
    }

    if (built && await execute(built)) setOpen(false)
  }

  if (!available || (command === 'request_asset_acquisition' && acquisitionChoices.length === 0)) return null

  return (
    <Popover open={open} onOpenChange={changeOpen}>
      <PopoverTrigger
        className={cn(
          'inline-flex items-center justify-center gap-1.5 rounded-full border border-border bg-transparent font-medium text-text-muted outline-none hover:bg-panel-hover hover:text-text focus-visible:ring-2 focus-visible:ring-ring',
          compact ? 'min-h-8 px-2.5 text-[11px]' : 'min-h-9 px-3 text-[12px]',
        )}
        aria-label={meta.title}
      >
        <Icon className="h-3.5 w-3.5" aria-hidden="true" />
        {meta.label}
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={8}
        className="w-[min(22rem,calc(100vw-1.5rem))] rounded-sm border border-border-strong bg-panel p-0 motion-reduce:animate-none"
        data-reader-inline-action={command}
      >
        <PopoverHeader className="border-b border-border-soft px-4 py-3">
          <PopoverTitle className="text-[14px] font-semibold text-text">{meta.title}</PopoverTitle>
          <PopoverDescription className="mt-0.5 text-[12px] leading-5 text-text-subtle">
            Changes save directly to this fragment.
          </PopoverDescription>
        </PopoverHeader>
        <form className="p-4" onSubmit={(event) => void submit(event)}>
          {command === 'set_reading_progress' && (
            <ProgressEditor draft={progressDraft} setDraft={setProgressDraft} item={item} />
          )}
          {command === 'request_asset_acquisition' && (
            <div className="grid gap-3">
              <label className="grid gap-1.5">
                <span className={fieldLabelClass}>Captured representation</span>
                <select className={selectClass} value={acquisitionKey} onChange={(event) => setAcquisitionKey(event.target.value)}>
                  {acquisitionChoices.length === 0 && <option value="">No unloaded media</option>}
                  {acquisitionChoices.map((choice) => <option key={choice.key} value={choice.key}>{choice.label}</option>)}
                </select>
              </label>
              <label className="grid gap-1.5">
                <span className={fieldLabelClass}>Keep as</span>
                <select className={selectClass} value={custody} onChange={(event) => setCustody(event.target.value as typeof custody)}>
                  <option value="cache">Cache</option>
                  <option value="mirror">Mirror</option>
                  <option value="adopted">Adopted</option>
                </select>
              </label>
            </div>
          )}
          {command === 'route' && (
            <label className="grid gap-1.5">
              <span className={fieldLabelClass}>Route</span>
              <select className={selectClass} value={routeId} onChange={(event) => setRouteId(event.target.value)} disabled={Boolean(optionsError)}>
                <option value="">{routes.length === 0 ? 'Loading routes…' : 'Choose route'}</option>
                {routes.map((route) => <option key={route.id} value={route.id}>{route.name || route.id}</option>)}
              </select>
            </label>
          )}
          {command === 'materialize' && (
            <label className="grid gap-1.5">
              <span className={fieldLabelClass}>Destination</span>
              <select className={selectClass} value={destinationId} onChange={(event) => setDestinationId(event.target.value)} disabled={Boolean(optionsError)}>
                <option value="">{destinations.length === 0 ? 'Loading destinations…' : 'Choose destination'}</option>
                {destinations.map((destination) => <option key={destination.id} value={destination.id}>{destination.name || destination.id} · {destination.kind}</option>)}
              </select>
            </label>
          )}
          {optionsError && <p className="mt-2 text-[12px] text-danger-soft">{optionsError}</p>}
          {feedback.state !== 'idle' && feedback.state !== 'pending' && feedback.state !== 'success' && (
            <p className="mt-2 text-[12px] text-danger-soft" role="status">{feedback.message}</p>
          )}
          <Button type="submit" size="sm" className="mt-3 min-h-10 w-full" disabled={pending || Boolean(optionsError)}>
            {pending ? <LoaderCircle className="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden="true" /> : <Check className="h-4 w-4" aria-hidden="true" />}
            {pending ? 'Saving…' : meta.label}
          </Button>
        </form>
      </PopoverContent>
    </Popover>
  )
}

export function ReaderReadingControls({
  item,
  onItemChange,
  compact = false,
}: Omit<ReaderActionPillProps, 'command'>) {
  const nextCommand = item.reading_state.state === 'read' ? 'mark_unread' : 'mark_read'
  const canToggle = hasReaderCommand(item, nextCommand)
  const { execute, pending } = useReaderCommandExecution(item, onItemChange)

  function toggleRead() {
    if (!canToggle || pending) return
    const identity = createReaderIntentIdentity()
    void execute({
      ...readerCommandBase(item, nextCommand, identity),
      command: nextCommand,
    })
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5" data-reader-reading-controls data-reader-nav-exclude>
      <button
        type="button"
        className={cn(
          'inline-flex items-center gap-1.5 rounded-full bg-panel-2 px-2.5 font-medium text-text-muted outline-none hover:text-text focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-default',
          compact ? 'min-h-7 text-[11px]' : 'min-h-8 text-[12px]',
        )}
        onClick={toggleRead}
        disabled={!canToggle || pending}
        aria-label={nextCommand === 'mark_read' ? 'Mark as read' : 'Mark as unread'}
        aria-pressed={item.reading_state.state === 'read'}
      >
        {item.reading_state.state === 'read' ? (
          <BookOpenCheck className="h-3.5 w-3.5" aria-hidden="true" />
        ) : (
          <Circle className="h-3 w-3" aria-hidden="true" />
        )}
        {readingStateLabel(item.reading_state.state)}
      </button>
      <ReaderActionPill item={item} onItemChange={onItemChange} command="set_reading_progress" compact />
    </div>
  )
}

export function ReaderEffectActions({
  item,
  onItemChange,
  includeMedia = false,
  compact = false,
}: Omit<ReaderActionPillProps, 'command'> & { includeMedia?: boolean }) {
  return (
    <div className="flex flex-wrap items-center gap-1.5" data-reader-effect-actions data-reader-nav-exclude>
      {includeMedia && (
        <ReaderActionPill item={item} onItemChange={onItemChange} command="request_asset_acquisition" compact={compact} />
      )}
      <ReaderActionPill item={item} onItemChange={onItemChange} command="route" compact={compact} />
      <ReaderActionPill item={item} onItemChange={onItemChange} command="materialize" compact={compact} />
    </div>
  )
}

function ProgressEditor({
  draft,
  setDraft,
  item,
}: {
  draft: ReaderProgressDraft
  setDraft: (value: ReaderProgressDraft) => void
  item: ReaderItem
}) {
  if (draft.kind === 'none') return <p className="text-[12px] text-text-subtle">No position is available for this item.</p>
  if (draft.kind === 'article') {
    return (
      <label className="grid gap-1.5">
        <span className={fieldLabelClass}>Article progress · 0 to 1</span>
        <Input type="number" min="0" max="1" step="0.01" value={draft.progress} onChange={(event) => setDraft({ ...draft, progress: event.target.value })} className="min-h-11" />
      </label>
    )
  }
  if (draft.kind === 'video' || draft.kind === 'audio') {
    return (
      <div className="grid grid-cols-2 gap-2">
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Elapsed seconds</span>
          <Input type="number" min="0" step="0.1" value={draft.elapsedSeconds} onChange={(event) => setDraft({ ...draft, elapsedSeconds: event.target.value })} className="min-h-11" />
        </label>
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Duration seconds</span>
          <Input type="number" min="0.1" step="0.1" value={draft.durationSeconds} onChange={(event) => setDraft({ ...draft, durationSeconds: event.target.value })} className="min-h-11" />
        </label>
      </div>
    )
  }
  if (draft.kind === 'gallery') {
    const choices = item.media.filter((media) => media.kind === 'image').toSorted((left, right) => left.attachment.position - right.attachment.position)
    return (
      <label className="grid gap-1.5">
        <span className={fieldLabelClass}>Gallery item</span>
        <select className={selectClass} value={draft.attachmentId} onChange={(event) => {
          const selected = choices.find((choice) => choice.attachment.attachment_id === event.target.value)
          setDraft({ kind: 'gallery', attachmentId: event.target.value, index: selected?.attachment.position ?? 0 })
        }}>
          {choices.map((choice) => <option key={choice.attachment.attachment_id} value={choice.attachment.attachment_id}>{choice.attachment.caption || `Item ${choice.attachment.position + 1}`}</option>)}
        </select>
      </label>
    )
  }
  return (
    <div className="grid grid-cols-2 gap-2">
      <label className="grid gap-1.5">
        <span className={fieldLabelClass}>Page</span>
        <Input type="number" min="1" step="1" value={draft.page} onChange={(event) => setDraft({ ...draft, page: event.target.value })} className="min-h-11" />
      </label>
      <label className="grid gap-1.5">
        <span className={fieldLabelClass}>Page progress · 0 to 1</span>
        <Input type="number" min="0" max="1" step="0.01" value={draft.progress} onChange={(event) => setDraft({ ...draft, progress: event.target.value })} className="min-h-11" />
      </label>
    </div>
  )
}
