import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { Check, ChevronDown, LoaderCircle, RotateCcw } from 'lucide-react'
import {
  Button,
  Input,
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
  Textarea,
} from '@hollis-labs/sysop-ui'
import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import {
  advertisedReaderCommands,
  createReaderIntentIdentity,
  optimisticReaderItem,
  parseReaderProgressDraft,
  progressDraftForItem,
  readerCommandBase,
  type ReaderProgressDraft,
} from '@/lib/reader-actions'
import type {
  Destination,
  ReaderAssetVariant,
  ReaderCommand,
  ReaderCommandName,
  ReaderItem,
  Route,
} from '@/lib/types'

interface ReaderActionsProps {
  item: ReaderItem
  onItemChange: (item: ReaderItem) => void
}

type ActionFeedback =
  | { state: 'idle'; message?: undefined }
  | { state: 'pending' | 'success' | 'conflict' | 'failure' | 'uncertain'; message: string }

interface AcquisitionChoice {
  key: string
  mediaAssetId: string
  variantKind: ReaderAssetVariant['kind']
  label: string
}

type EditorCommand = Exclude<ReaderCommandName, 'mark_read' | 'mark_unread'>

const ACTION_LABELS: Record<ReaderCommandName, string> = {
  add_tag: 'Add tag',
  remove_tag: 'Remove tag',
  append_capture_note: 'Capture note',
  update_curated_note: 'Curated note',
  set_reading_progress: 'Reading position',
  mark_read: 'Mark read',
  mark_unread: 'Mark unread',
  request_asset_acquisition: 'Acquire media',
  route: 'Route',
  materialize: 'Materialize',
}

const selectClass =
  'min-h-11 w-full rounded-sm border border-input bg-transparent px-3 text-[13px] text-text outline-none focus-visible:ring-2 focus-visible:ring-ring'
const fieldLabelClass = 'text-[12px] font-medium text-text-soft'

function feedbackClass(feedback: ActionFeedback): string {
  switch (feedback.state) {
    case 'success':
      return 'border-status-done/40 bg-status-done/10 text-status-done'
    case 'pending':
      return 'border-status-doing/40 bg-status-doing/10 text-status-doing'
    case 'conflict':
    case 'uncertain':
      return 'border-status-paused/40 bg-status-paused/10 text-status-paused'
    case 'failure':
      return 'border-status-blocked/40 bg-status-blocked/10 text-status-blocked'
    default:
      return 'border-border-soft bg-panel-2/30 text-text-subtle'
  }
}

function commandSuccessLabel(command: ReaderCommandName): string {
  switch (command) {
    case 'add_tag':
      return 'Tag added.'
    case 'remove_tag':
      return 'Tag removed.'
    case 'append_capture_note':
      return 'Capture note saved.'
    case 'update_curated_note':
      return 'Curated note updated.'
    case 'set_reading_progress':
      return 'Reading position saved.'
    case 'mark_read':
      return 'Marked read.'
    case 'mark_unread':
      return 'Marked unread.'
    case 'request_asset_acquisition':
      return 'Media acquisition requested.'
    case 'route':
      return 'Route command accepted.'
    case 'materialize':
      return 'Materialization command accepted.'
  }
}

function actionNeedsEditor(command: ReaderCommandName): command is EditorCommand {
  return command !== 'mark_read' && command !== 'mark_unread'
}

function submitLabel(command: EditorCommand): string {
  switch (command) {
    case 'add_tag':
      return 'Save tag'
    case 'remove_tag':
      return 'Confirm tag removal'
    case 'append_capture_note':
      return 'Save capture note'
    case 'update_curated_note':
      return 'Save curated note'
    case 'set_reading_progress':
      return 'Save reading position'
    case 'request_asset_acquisition':
      return 'Request acquisition'
    case 'route':
      return 'Apply route'
    case 'materialize':
      return 'Run materialization'
  }
}

export function ReaderActions({ item, onItemChange }: ReaderActionsProps) {
  const api = useApi()
  const available = advertisedReaderCommands(item)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState<ReaderCommandName>()
  const [feedback, setFeedback] = useState<ActionFeedback>({ state: 'idle' })
  const [retryCommand, setRetryCommand] = useState<ReaderCommand>()
  const [tagDraft, setTagDraft] = useState('')
  const [removeTag, setRemoveTag] = useState(item.tags.combined[0] ?? '')
  const [captureNote, setCaptureNote] = useState('')
  const [curatedNote, setCuratedNote] = useState(item.curated_note?.body_markdown ?? '')
  const [progressDraft, setProgressDraft] = useState<ReaderProgressDraft>(() =>
    progressDraftForItem(item),
  )
  const [acquisitionKey, setAcquisitionKey] = useState('')
  const [custody, setCustody] = useState<'cache' | 'mirror' | 'adopted'>('cache')
  const [routeId, setRouteId] = useState('')
  const [destinationId, setDestinationId] = useState('')
  const [routes, setRoutes] = useState<Route[]>([])
  const [destinations, setDestinations] = useState<Destination[]>([])
  const [routeOptionsError, setRouteOptionsError] = useState<string>()
  const [destinationOptionsError, setDestinationOptionsError] = useState<string>()
  const pending = feedback.state === 'pending'

  const acquisitionChoices = useMemo<AcquisitionChoice[]>(() => {
    const choices: AcquisitionChoice[] = []
    for (const media of item.media) {
      for (const variant of media.variants) {
        choices.push({
          key: variant.asset_variant_id,
          mediaAssetId: media.media_asset_id,
          variantKind: variant.kind,
          label: `${media.kind} · ${variant.kind} · ${variant.acquisition_state}`,
        })
      }
    }
    return choices.slice(0, 200)
  }, [item.media])

  useEffect(() => {
    if (active !== 'route' || routes.length > 0 || routeOptionsError) return
    let current = true
    void api
      .fetchRoutes()
      .then((items) => {
        if (!current) return
        const bounded = items.slice(0, 200)
        setRoutes(bounded)
        setRouteId((value) => value || bounded[0]?.id || '')
      })
      .catch((reason: unknown) => {
        if (current) setRouteOptionsError(reason instanceof Error ? reason.message : 'Routes could not load.')
      })
    return () => {
      current = false
    }
  }, [active, api, routeOptionsError, routes.length])

  useEffect(() => {
    if (active !== 'materialize' || destinations.length > 0 || destinationOptionsError) return
    let current = true
    void api
      .fetchDestinations()
      .then((items) => {
        if (!current) return
        const bounded = items.slice(0, 200)
        setDestinations(bounded)
        setDestinationId((value) => value || bounded[0]?.id || '')
      })
      .catch((reason: unknown) => {
        if (current) {
          setDestinationOptionsError(
            reason instanceof Error ? reason.message : 'Destinations could not load.',
          )
        }
      })
    return () => {
      current = false
    }
  }, [active, api, destinationOptionsError, destinations.length])

  if (available.length === 0 && !open) {
    return (
      <Button
        variant="ghost"
        size="sm"
        className="min-h-11 sm:min-h-8"
        disabled
        aria-label="Actions are not available for this fragment"
      >
        Actions
      </Button>
    )
  }

  async function execute(command: ReaderCommand) {
    const before = item
    setRetryCommand(undefined)
    setFeedback({ state: 'pending', message: `Saving ${ACTION_LABELS[command.command].toLocaleLowerCase()}…` })
    onItemChange(optimisticReaderItem(before, command))
    try {
      const response = await api.executeReaderCommand({ fragmentId: before.fragment_id, command })
      onItemChange(response)
      setFeedback({ state: 'success', message: commandSuccessLabel(command.command) })
      if (command.command === 'add_tag') setTagDraft('')
      if (command.command === 'append_capture_note') setCaptureNote('')
    } catch (reason) {
      onItemChange(before)
      if (reason instanceof ApiError && reason.status === 409) {
        setFeedback({ state: 'conflict', message: 'Another change won. Your draft is preserved.' })
        try {
          const authoritative = await api.fetchReaderItem({ fragmentId: before.fragment_id })
          onItemChange(authoritative)
        } catch {
          setFeedback({
            state: 'conflict',
            message: 'Another change won. Your draft is preserved; refresh to see the current item.',
          })
        }
      } else if (!(reason instanceof ApiError)) {
        setRetryCommand(command)
        setFeedback({
          state: 'uncertain',
          message: 'The network outcome is uncertain. Retry sends the exact same command.',
        })
      } else {
        setFeedback({ state: 'failure', message: reason.message || 'The action could not be saved.' })
      }
    }
  }

  function selectAction(command: ReaderCommandName) {
    setFeedback({ state: 'idle' })
    setRetryCommand(undefined)
    if (actionNeedsEditor(command)) {
      setActive(command)
      return
    }
    const identity = createReaderIntentIdentity()
    const built = {
      ...readerCommandBase(item, command, identity),
      command,
    } as ReaderCommand
    void execute(built)
  }

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!active || !available.includes(active) || !actionNeedsEditor(active)) return
    const identity = createReaderIntentIdentity()
    const base = readerCommandBase(item, active, identity)
    let command: ReaderCommand | undefined
    switch (active) {
      case 'add_tag':
        if (tagDraft.trim()) command = { ...base, command: active, tag: tagDraft.trim() }
        break
      case 'remove_tag':
        if (removeTag) command = { ...base, command: active, tag: removeTag }
        break
      case 'append_capture_note':
        if (captureNote.trim()) {
          command = {
            ...base,
            command: active,
            annotation_id: `annotation-${identity.command_id}`,
            text: captureNote.trim(),
          }
        }
        break
      case 'update_curated_note':
        command = {
          ...base,
          command: active,
          expected_note_revision: item.curated_note?.revision ?? 0,
          body_markdown: curatedNote,
        }
        break
      case 'set_reading_progress': {
        const result = parseReaderProgressDraft(progressDraft)
        if (!result.ok) {
          setFeedback({ state: 'failure', message: result.error })
          return
        }
        command = { ...base, command: active, position: result.position }
        break
      }
      case 'request_asset_acquisition': {
        const choice = acquisitionChoices.find((candidate) => candidate.key === acquisitionKey)
        if (choice) {
          command = {
            ...base,
            command: active,
            media_asset_id: choice.mediaAssetId,
            variant_kind: choice.variantKind,
            requested_custody: custody,
          }
        }
        break
      }
      case 'route':
        if (routeId) command = { ...base, command: active, route_id: routeId }
        break
      case 'materialize':
        if (destinationId) {
          command = { ...base, command: active, destination_id: destinationId }
        }
        break
    }
    if (!command) {
      setFeedback({ state: 'failure', message: 'Choose or enter a valid value first.' })
      return
    }
    void execute(command)
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger
        className="inline-flex min-h-11 items-center justify-center gap-1.5 rounded-sm border border-border bg-transparent px-3 text-[13px] font-medium text-text-muted outline-none hover:bg-panel-hover hover:text-text focus-visible:ring-2 focus-visible:ring-ring sm:min-h-8"
        aria-label="Open Reader actions"
      >
        Actions
        <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={8}
        className="w-[min(23rem,calc(100vw-1.5rem))] gap-0 rounded-sm border border-border-strong bg-panel p-0 motion-reduce:animate-none motion-reduce:duration-0"
        data-reader-action-tray
      >
        <PopoverHeader className="border-b border-border-soft px-4 py-3">
          <PopoverTitle className="text-[14px] font-semibold text-text">Reader actions</PopoverTitle>
          <PopoverDescription className="mt-0.5 text-[12px] leading-5 text-text-subtle">
            {item.reading_state.state.replace('_', ' ')} · revision {item.revision}
          </PopoverDescription>
        </PopoverHeader>

        <div className="max-h-[min(34rem,70vh)] overflow-y-auto px-3 py-3">
          <div className="flex flex-wrap gap-2" aria-label="Available Reader actions">
            {available.map((command) => (
              <button
                key={command}
                type="button"
                className={`min-h-11 rounded-sm border px-3 text-left text-[12px] font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring ${
                  active === command
                    ? 'border-primary bg-primary/10 text-text'
                    : 'border-border bg-bg text-text-muted hover:bg-panel-hover'
                }`}
                onClick={() => selectAction(command)}
                disabled={pending}
              >
                {ACTION_LABELS[command]}
              </button>
            ))}
          </div>

          {active && available.includes(active) && actionNeedsEditor(active) && (
            <form className="mt-4 border-t border-border-soft pt-4" onSubmit={submit}>
              <ActionEditor
                command={active}
                item={item}
                tagDraft={tagDraft}
                setTagDraft={setTagDraft}
                removeTag={removeTag}
                setRemoveTag={setRemoveTag}
                captureNote={captureNote}
                setCaptureNote={setCaptureNote}
                curatedNote={curatedNote}
                setCuratedNote={setCuratedNote}
                progressDraft={progressDraft}
                setProgressDraft={setProgressDraft}
                acquisitionChoices={acquisitionChoices}
                acquisitionKey={acquisitionKey}
                setAcquisitionKey={setAcquisitionKey}
                custody={custody}
                setCustody={setCustody}
                routes={routes}
                routeId={routeId}
                setRouteId={setRouteId}
                routeOptionsError={routeOptionsError}
                destinations={destinations}
                destinationId={destinationId}
                setDestinationId={setDestinationId}
                destinationOptionsError={destinationOptionsError}
              />
              <Button type="submit" size="sm" className="mt-3 min-h-11 w-full" disabled={pending}>
                {pending ? (
                  <LoaderCircle className="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                ) : (
                  <Check className="h-4 w-4" aria-hidden="true" />
                )}
                {pending ? 'Saving…' : submitLabel(active)}
              </Button>
            </form>
          )}

          {feedback.state !== 'idle' && (
            <div
              className={`mt-3 flex min-h-11 items-center gap-2 rounded-sm border px-3 py-2 text-[12px] leading-4 ${feedbackClass(feedback)}`}
              role="status"
              aria-live="polite"
              data-reader-action-state={feedback.state}
            >
              {feedback.state === 'pending' && (
                <LoaderCircle className="h-3.5 w-3.5 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              )}
              <span className="min-w-0 flex-1">{feedback.message}</span>
              {feedback.state === 'uncertain' && retryCommand && (
                <button
                  type="button"
                  className="inline-flex min-h-11 shrink-0 items-center gap-1 rounded-sm px-2 font-medium outline-none hover:bg-bg/60 focus-visible:ring-2 focus-visible:ring-ring"
                  onClick={() => void execute(retryCommand)}
                >
                  <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
                  Retry
                </button>
              )}
            </div>
          )}
        </div>
      </PopoverContent>
    </Popover>
  )
}

interface ActionEditorProps {
  command: EditorCommand
  item: ReaderItem
  tagDraft: string
  setTagDraft: (value: string) => void
  removeTag: string
  setRemoveTag: (value: string) => void
  captureNote: string
  setCaptureNote: (value: string) => void
  curatedNote: string
  setCuratedNote: (value: string) => void
  progressDraft: ReaderProgressDraft
  setProgressDraft: (value: ReaderProgressDraft) => void
  acquisitionChoices: AcquisitionChoice[]
  acquisitionKey: string
  setAcquisitionKey: (value: string) => void
  custody: 'cache' | 'mirror' | 'adopted'
  setCustody: (value: 'cache' | 'mirror' | 'adopted') => void
  routes: Route[]
  routeId: string
  setRouteId: (value: string) => void
  routeOptionsError?: string
  destinations: Destination[]
  destinationId: string
  setDestinationId: (value: string) => void
  destinationOptionsError?: string
}

function ActionEditor(props: ActionEditorProps) {
  switch (props.command) {
    case 'add_tag':
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Tag</span>
          <Input
            value={props.tagDraft}
            onChange={(event) => props.setTagDraft(event.target.value)}
            maxLength={128}
            className="min-h-11"
            placeholder="reader"
            autoFocus
          />
        </label>
      )
    case 'remove_tag':
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Visible tag</span>
          <select
            className={selectClass}
            value={props.removeTag}
            onChange={(event) => props.setRemoveTag(event.target.value)}
            disabled={props.item.tags.combined.length === 0}
          >
            {props.item.tags.combined.length === 0 && <option value="">No removable tags</option>}
            {props.item.tags.combined.map((tag) => (
              <option key={tag} value={tag}>{tag}</option>
            ))}
          </select>
        </label>
      )
    case 'append_capture_note':
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Capture note</span>
          <Textarea
            value={props.captureNote}
            onChange={(event) => props.setCaptureNote(event.target.value)}
            maxLength={65_536}
            className="min-h-28 resize-y"
            placeholder="Add context from this reading pass"
            autoFocus
          />
        </label>
      )
    case 'update_curated_note':
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Curated note · revision {props.item.curated_note?.revision ?? 0}</span>
          <Textarea
            value={props.curatedNote}
            onChange={(event) => props.setCuratedNote(event.target.value)}
            maxLength={524_288}
            className="min-h-32 resize-y"
            placeholder="Keep the durable working note here"
            autoFocus
          />
        </label>
      )
    case 'set_reading_progress':
      return <ProgressEditor draft={props.progressDraft} setDraft={props.setProgressDraft} item={props.item} />
    case 'request_asset_acquisition':
      return (
        <div className="grid gap-3">
          <label className="grid gap-1.5">
            <span className={fieldLabelClass}>Captured representation</span>
            <select
              className={selectClass}
              value={props.acquisitionKey}
              onChange={(event) => props.setAcquisitionKey(event.target.value)}
            >
              <option value="">Choose media</option>
              {props.acquisitionChoices.map((choice) => (
                <option key={choice.key} value={choice.key}>{choice.label}</option>
              ))}
            </select>
          </label>
          <label className="grid gap-1.5">
            <span className={fieldLabelClass}>Requested custody</span>
            <select
              className={selectClass}
              value={props.custody}
              onChange={(event) => props.setCustody(event.target.value as typeof props.custody)}
            >
              <option value="cache">Cache</option>
              <option value="mirror">Mirror</option>
              <option value="adopted">Adopted</option>
            </select>
          </label>
        </div>
      )
    case 'route':
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Existing route</span>
          <select
            className={selectClass}
            value={props.routeId}
            onChange={(event) => props.setRouteId(event.target.value)}
            disabled={Boolean(props.routeOptionsError)}
          >
            <option value="">{props.routes.length === 0 ? 'Loading routes…' : 'Choose route'}</option>
            {props.routes.map((route) => (
              <option key={route.id} value={route.id}>{route.name || route.id}</option>
            ))}
          </select>
          {props.routeOptionsError && <span className="text-[12px] text-danger-soft">{props.routeOptionsError}</span>}
        </label>
      )
    case 'materialize':
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Existing destination</span>
          <select
            className={selectClass}
            value={props.destinationId}
            onChange={(event) => props.setDestinationId(event.target.value)}
            disabled={Boolean(props.destinationOptionsError)}
          >
            <option value="">{props.destinations.length === 0 ? 'Loading destinations…' : 'Choose destination'}</option>
            {props.destinations.map((destination) => (
              <option key={destination.id} value={destination.id}>
                {destination.name || destination.id} · {destination.kind}
              </option>
            ))}
          </select>
          {props.destinationOptionsError && <span className="text-[12px] text-danger-soft">{props.destinationOptionsError}</span>}
        </label>
      )
  }
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
  switch (draft.kind) {
    case 'none':
      return <p className="text-[12px] leading-5 text-text-subtle">No position target is exposed for this renderer.</p>
    case 'article':
      return (
        <div className="grid gap-3">
          <label className="grid gap-1.5">
            <span className={fieldLabelClass}>Article progress · 0 to 1</span>
            <Input type="number" min="0" max="1" step="0.01" value={draft.progress} onChange={(event) => setDraft({ ...draft, progress: event.target.value })} className="min-h-11" />
          </label>
          <div className="grid grid-cols-2 gap-2">
            <label className="grid gap-1.5">
              <span className={fieldLabelClass}>Block anchor</span>
              <Input value={draft.blockAnchor} onChange={(event) => setDraft({ ...draft, blockAnchor: event.target.value })} maxLength={255} className="min-h-11" />
            </label>
            <label className="grid gap-1.5">
              <span className={fieldLabelClass}>Local offset</span>
              <Input type="number" min="0" step="1" value={draft.localOffset} onChange={(event) => setDraft({ ...draft, localOffset: event.target.value })} className="min-h-11" />
            </label>
          </div>
        </div>
      )
    case 'video':
    case 'audio':
      return (
        <div className="grid gap-3">
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
          {draft.kind === 'video' && draft.providerMediaId && (
            <p className="text-[11px] leading-4 text-text-subtle">Provider media {draft.providerMediaId}</p>
          )}
        </div>
      )
    case 'gallery': {
      const options = item.media
        .filter((media) => media.kind === 'image')
        .toSorted((left, right) => left.attachment.position - right.attachment.position)
        .slice(0, 200)
        .map((media) => ({
          id: media.attachment.attachment_id,
          index: media.attachment.position,
          label: media.attachment.caption || `Item ${media.attachment.position + 1}`,
        }))
      return (
        <label className="grid gap-1.5">
          <span className={fieldLabelClass}>Captured gallery item</span>
          <select
            className={selectClass}
            value={draft.attachmentId}
            onChange={(event) => {
              const selected = options.find((option) => option.id === event.target.value)
              setDraft({ kind: 'gallery', attachmentId: event.target.value, index: selected?.index ?? 0 })
            }}
          >
            {options.map((option) => (
              <option key={option.id} value={option.id}>{option.label}</option>
            ))}
          </select>
        </label>
      )
    }
    case 'document':
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
}
