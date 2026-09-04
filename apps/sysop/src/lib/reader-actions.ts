import type {
  ReaderCommand,
  ReaderCommandName,
  ReaderItem,
  ReaderReadingPosition,
} from './types'

const READER_COMMANDS = new Set<ReaderCommandName>([
  'add_tag',
  'remove_tag',
  'append_capture_note',
  'update_curated_note',
  'set_reading_progress',
  'mark_read',
  'mark_unread',
  'request_asset_acquisition',
  'route',
  'materialize',
])

let fallbackIntentSequence = 0

export interface ReaderIntentIdentity {
  command_id: string
  idempotency_key: string
}

export function createReaderIntentIdentity(): ReaderIntentIdentity {
  const nonce = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${++fallbackIntentSequence}`
  return {
    command_id: `reader-command-${nonce}`,
    idempotency_key: `reader-intent-${nonce}`,
  }
}

export function advertisedReaderCommands(item: ReaderItem): ReaderCommandName[] {
  const seen = new Set<ReaderCommandName>()
  const commands: ReaderCommandName[] = []
  for (const capability of item.actions) {
    const command = capability.command
    if (!READER_COMMANDS.has(command) || seen.has(command)) continue
    seen.add(command)
    commands.push(command)
  }
  return commands
}

export function hasReaderCommand(item: ReaderItem, command: ReaderCommandName): boolean {
  return advertisedReaderCommands(item).includes(command)
}

export function expectedReaderCommandRevision(
  item: ReaderItem,
  command: ReaderCommandName,
): number {
  switch (command) {
    case 'set_reading_progress':
    case 'mark_read':
    case 'mark_unread':
      return item.reading_state.revision
    default:
      return item.revision
  }
}

export function readerCommandBase(
  item: ReaderItem,
  command: ReaderCommandName,
  identity: ReaderIntentIdentity = createReaderIntentIdentity(),
): Pick<ReaderCommand, 'schema_version' | 'command' | 'command_id' | 'idempotency_key' | 'expected_revision'> {
  return {
    schema_version: 'fe.reader.command.v1',
    command,
    command_id: identity.command_id,
    idempotency_key: identity.idempotency_key,
    expected_revision: expectedReaderCommandRevision(item, command),
  }
}

function withoutCompletion(item: ReaderItem): ReaderItem['reading_state'] {
  const state = { ...item.reading_state }
  delete state.completed_at
  return state
}

function countAcquisition(item: ReaderItem): ReaderItem['operations']['acquisition'] {
  const counts: ReaderItem['operations']['acquisition'] = {
    pending: 0,
    available: 0,
    reference_only: 0,
    failed: 0,
  }
  for (const media of item.media) {
    for (const variant of media.variants) counts[variant.acquisition_state] += 1
  }
  return counts
}

/**
 * Applies only UI states that remain valid ReaderItem projections. Server-owned
 * capture IDs and first curated-note timestamps are deliberately never invented.
 */
export function optimisticReaderItem(item: ReaderItem, command: ReaderCommand): ReaderItem {
  switch (command.command) {
    case 'add_tag': {
      const tag = command.tag.trim()
      if (!tag || item.tags.combined.some((value) => value.toLocaleLowerCase() === tag.toLocaleLowerCase())) {
        return item
      }
      return {
        ...item,
        tags: {
          combined: [...item.tags.combined, tag],
          attributed: [...item.tags.attributed, { value: tag, source: 'user' }],
        },
      }
    }
    case 'remove_tag':
      return {
        ...item,
        tags: {
          ...item.tags,
          combined: item.tags.combined.filter(
            (value) => value.toLocaleLowerCase() !== command.tag.trim().toLocaleLowerCase(),
          ),
        },
      }
    case 'update_curated_note':
      return item.curated_note
        ? {
            ...item,
            curated_note: { ...item.curated_note, body_markdown: command.body_markdown },
          }
        : item
    case 'set_reading_progress':
      return {
        ...item,
        reading_state: {
          ...withoutCompletion(item),
          state: 'in_progress',
          position: command.position,
        },
      }
    case 'mark_read':
      return {
        ...item,
        reading_state: { ...item.reading_state, state: 'read' },
      }
    case 'mark_unread':
      return {
        ...item,
        reading_state: {
          ...withoutCompletion(item),
          state: 'unread',
          position: { kind: 'none' },
        },
      }
    case 'request_asset_acquisition': {
      const media = item.media.map((entry) =>
        entry.media_asset_id !== command.media_asset_id
          ? entry
          : {
              ...entry,
              variants: entry.variants.map((variant) =>
                variant.kind !== command.variant_kind || variant.acquisition_state === 'available'
                  ? variant
                  : {
                      ...variant,
                      custody: command.requested_custody,
                      acquisition_state: 'pending' as const,
                    },
              ),
            },
      )
      const next = { ...item, media }
      return {
        ...next,
        operations: { ...next.operations, acquisition: countAcquisition(next) },
      }
    }
    case 'route':
      return {
        ...item,
        operations: {
          ...item.operations,
          routing: {
            state: 'pending',
            references: Array.from(new Set([...item.operations.routing.references, command.route_id])),
          },
        },
      }
    case 'materialize':
      return {
        ...item,
        operations: {
          ...item.operations,
          materialization: {
            state: 'pending',
            references: Array.from(
              new Set([...item.operations.materialization.references, command.destination_id]),
            ),
          },
        },
      }
    case 'append_capture_note':
      return item
  }
}

export type ReaderProgressDraft =
  | { kind: 'none' }
  | { kind: 'article'; progress: string; blockAnchor: string; localOffset: string }
  | {
      kind: 'video'
      elapsedSeconds: string
      durationSeconds: string
      providerMediaId: string
    }
  | { kind: 'gallery'; attachmentId: string; index: number }
  | { kind: 'document'; page: string; progress: string }
  | { kind: 'audio'; elapsedSeconds: string; durationSeconds: string }

function durationFor(item: ReaderItem, kind: 'video' | 'audio'): number | undefined {
  for (const media of item.media) {
    if (media.kind !== kind) continue
    const duration = media.variants.find((variant) => variant.duration_seconds)?.duration_seconds
    if (duration !== undefined) return duration
  }
  return undefined
}

function providerMediaFor(item: ReaderItem): string {
  if (item.playback?.kind === 'provider_embed') return item.playback.provider_item_id
  return item.media.find((media) => media.kind === 'video')?.provider_media_id ?? ''
}

export function progressDraftForItem(item: ReaderItem): ReaderProgressDraft {
  const current = item.reading_state.position
  switch (item.renderer) {
    case 'article':
    case 'text':
      return current.kind === 'article'
        ? {
            kind: 'article',
            progress: String(current.progress),
            blockAnchor: current.block_anchor ?? '',
            localOffset: current.local_offset === undefined ? '' : String(current.local_offset),
          }
        : { kind: 'article', progress: '0', blockAnchor: '', localOffset: '' }
    case 'video': {
      const duration = current.kind === 'video' ? current.duration_seconds : durationFor(item, 'video')
      return {
        kind: 'video',
        elapsedSeconds: current.kind === 'video' ? String(current.elapsed_seconds) : '0',
        durationSeconds: duration === undefined ? '' : String(duration),
        providerMediaId:
          current.kind === 'video' ? (current.provider_media_id ?? providerMediaFor(item)) : providerMediaFor(item),
      }
    }
    case 'gallery':
    case 'image': {
      const first = item.media
        .filter((media) => media.kind === 'image')
        .toSorted((left, right) => left.attachment.position - right.attachment.position)[0]
      if (current.kind === 'gallery') {
        return {
          kind: 'gallery',
          attachmentId: current.attachment_id,
          index: current.index,
        }
      }
      return first
        ? {
            kind: 'gallery',
            attachmentId: first.attachment.attachment_id,
            index: first.attachment.position,
          }
        : { kind: 'none' }
    }
    case 'document':
      return current.kind === 'document'
        ? {
            kind: 'document',
            page: String(current.page),
            progress: current.progress === undefined ? '' : String(current.progress),
          }
        : { kind: 'document', page: '1', progress: '' }
    case 'audio': {
      const duration = current.kind === 'audio' ? current.duration_seconds : durationFor(item, 'audio')
      return {
        kind: 'audio',
        elapsedSeconds: current.kind === 'audio' ? String(current.elapsed_seconds) : '0',
        durationSeconds: duration === undefined ? '' : String(duration),
      }
    }
    default:
      return { kind: 'none' }
  }
}

export type ReaderProgressResult =
  | { ok: true; position: ReaderReadingPosition }
  | { ok: false; error: string }

function finiteNumber(value: string): number | undefined {
  if (value.trim() === '') return undefined
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : undefined
}

export function parseReaderProgressDraft(draft: ReaderProgressDraft): ReaderProgressResult {
  switch (draft.kind) {
    case 'none':
      return { ok: true, position: { kind: 'none' } }
    case 'article': {
      const progress = finiteNumber(draft.progress)
      const localOffset = finiteNumber(draft.localOffset)
      if (progress === undefined || progress < 0 || progress > 1) {
        return { ok: false, error: 'Article progress must be between 0 and 1.' }
      }
      if (localOffset !== undefined && (!Number.isInteger(localOffset) || localOffset < 0)) {
        return { ok: false, error: 'Local offset must be a non-negative whole number.' }
      }
      return {
        ok: true,
        position: {
          kind: 'article',
          progress,
          ...(draft.blockAnchor.trim() ? { block_anchor: draft.blockAnchor.trim() } : {}),
          ...(localOffset === undefined ? {} : { local_offset: localOffset }),
        },
      }
    }
    case 'video':
    case 'audio': {
      const elapsed = finiteNumber(draft.elapsedSeconds)
      const duration = finiteNumber(draft.durationSeconds)
      if (elapsed === undefined || elapsed < 0) {
        return { ok: false, error: 'Elapsed time must be zero or greater.' }
      }
      if (duration !== undefined && (duration <= 0 || elapsed > duration)) {
        return { ok: false, error: 'Duration must be greater than elapsed time.' }
      }
      if (draft.kind === 'video') {
        return {
          ok: true,
          position: {
            kind: 'video',
            elapsed_seconds: elapsed,
            ...(duration === undefined ? {} : { duration_seconds: duration }),
            ...(draft.providerMediaId ? { provider_media_id: draft.providerMediaId } : {}),
          },
        }
      }
      return {
        ok: true,
        position: {
          kind: 'audio',
          elapsed_seconds: elapsed,
          ...(duration === undefined ? {} : { duration_seconds: duration }),
        },
      }
    }
    case 'gallery':
      return draft.attachmentId
        ? {
            ok: true,
            position: {
              kind: 'gallery',
              attachment_id: draft.attachmentId,
              index: draft.index,
            },
          }
        : { ok: false, error: 'Choose a captured gallery item.' }
    case 'document': {
      const page = finiteNumber(draft.page)
      const progress = finiteNumber(draft.progress)
      if (page === undefined || !Number.isInteger(page) || page < 1) {
        return { ok: false, error: 'Document page must be a positive whole number.' }
      }
      if (progress !== undefined && (progress < 0 || progress > 1)) {
        return { ok: false, error: 'Page progress must be between 0 and 1.' }
      }
      return {
        ok: true,
        position: {
          kind: 'document',
          page,
          ...(progress === undefined ? {} : { progress }),
        },
      }
    }
  }
}
