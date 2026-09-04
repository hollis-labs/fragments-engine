import { describe, expect, it } from 'vitest'
import {
  advertisedReaderCommands,
  expectedReaderCommandRevision,
  optimisticReaderItem,
  parseReaderProgressDraft,
  progressDraftForItem,
  readerCommandBase,
} from './reader-actions'
import { readerItem } from '@/test/reader-fixture'
import type { ReaderCommand, ReaderCommandCapability } from './types'

const schema =
  'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json#/$defs/MarkRead'

describe('Reader action model', () => {
  it('exposes only the frozen command union advertised by the item', () => {
    const invalid = {
      command: 'apply_triage_decision',
      input_schema: schema,
      expected_revision_required: true,
    } as unknown as ReaderCommandCapability
    const item = readerItem({
      actions: [
        { command: 'mark_read', input_schema: schema, expected_revision_required: true },
        invalid,
        { command: 'mark_read', input_schema: schema, expected_revision_required: true },
        { command: 'add_tag', input_schema: schema, expected_revision_required: true },
      ],
    })

    expect(advertisedReaderCommands(item)).toEqual(['mark_read', 'add_tag'])
  })

  it('uses reading-state revisions only for reading replacements', () => {
    const item = readerItem({
      revision: 12,
      reading_state: { ...readerItem().reading_state, revision: 7 },
    })

    expect(expectedReaderCommandRevision(item, 'set_reading_progress')).toBe(7)
    expect(expectedReaderCommandRevision(item, 'mark_read')).toBe(7)
    expect(expectedReaderCommandRevision(item, 'mark_unread')).toBe(7)
    expect(expectedReaderCommandRevision(item, 'add_tag')).toBe(12)
    expect(expectedReaderCommandRevision(item, 'update_curated_note')).toBe(12)
    expect(readerCommandBase(item, 'route', { command_id: 'command-1', idempotency_key: 'key-1' }))
      .toMatchObject({ command: 'route', expected_revision: 12 })
  })

  it('parses every frozen reading-position discriminator exactly', () => {
    expect(parseReaderProgressDraft({ kind: 'none' })).toEqual({
      ok: true,
      position: { kind: 'none' },
    })
    expect(
      parseReaderProgressDraft({
        kind: 'article',
        progress: '0.7',
        blockAnchor: 'section-three',
        localOffset: '4',
      }),
    ).toEqual({
      ok: true,
      position: { kind: 'article', progress: 0.7, block_anchor: 'section-three', local_offset: 4 },
    })
    expect(
      parseReaderProgressDraft({
        kind: 'video',
        elapsedSeconds: '14.5',
        durationSeconds: '90',
        providerMediaId: 'youtube-id',
      }),
    ).toEqual({
      ok: true,
      position: {
        kind: 'video',
        elapsed_seconds: 14.5,
        duration_seconds: 90,
        provider_media_id: 'youtube-id',
      },
    })
    expect(
      parseReaderProgressDraft({ kind: 'gallery', attachmentId: 'attachment-2', index: 5 }),
    ).toEqual({
      ok: true,
      position: { kind: 'gallery', attachment_id: 'attachment-2', index: 5 },
    })
    expect(parseReaderProgressDraft({ kind: 'document', page: '8', progress: '0.25' })).toEqual({
      ok: true,
      position: { kind: 'document', page: 8, progress: 0.25 },
    })
    expect(
      parseReaderProgressDraft({ kind: 'audio', elapsedSeconds: '6', durationSeconds: '40' }),
    ).toEqual({
      ok: true,
      position: { kind: 'audio', elapsed_seconds: 6, duration_seconds: 40 },
    })
  })

  it('derives gallery and video ownership targets from the current Reader item', () => {
    const gallery = readerItem({
      renderer: 'gallery',
      media: [
        {
          attachment: {
            attachment_id: 'attachment-current',
            fragment_revision_id: 'revision-1',
            media_asset_id: 'media-current',
            role: 'gallery_item',
            position: 3,
          },
          media_asset_id: 'media-current',
          kind: 'image',
          variants: [],
        },
      ],
    })
    expect(progressDraftForItem(gallery)).toEqual({
      kind: 'gallery',
      attachmentId: 'attachment-current',
      index: 3,
    })

    const video = readerItem({
      renderer: 'video',
      playback: {
        kind: 'provider_embed',
        provider: 'youtube',
        provider_item_id: 'provider-current',
      },
    })
    expect(progressDraftForItem(video)).toMatchObject({
      kind: 'video',
      providerMediaId: 'provider-current',
    })
  })

  it('optimistically changes only the relevant independent projection axis', () => {
    const item = readerItem()
    const route = {
      ...readerCommandBase(item, 'route', { command_id: 'route-command', idempotency_key: 'route-key' }),
      command: 'route',
      route_id: 'route-1',
    } satisfies ReaderCommand
    const routed = optimisticReaderItem(item, route)
    expect(routed.operations.routing).toEqual({ state: 'pending', references: ['route-1'] })
    expect(routed.reading_state).toEqual(item.reading_state)
    expect(routed.operations.triage).toEqual(item.operations.triage)

    const read = {
      ...readerCommandBase(item, 'mark_read', { command_id: 'read-command', idempotency_key: 'read-key' }),
      command: 'mark_read',
    } satisfies ReaderCommand
    const marked = optimisticReaderItem(item, read)
    expect(marked.reading_state.state).toBe('read')
    expect(marked.operations).toEqual(item.operations)
  })
})
