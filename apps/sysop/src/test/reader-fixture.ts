import type { ReaderItem, ReaderItemList, ReaderScope } from '@/lib/types'

export function readerItem(overrides: Partial<ReaderItem> = {}): ReaderItem {
  const fragmentId = overrides.fragment_id ?? 'fragment-1'
  const revisionId = overrides.fragment_revision_id ?? 'revision-1'
  return {
    schema_version: 'fe.reader.item.v1',
    fragment_id: fragmentId,
    fragment_revision_id: revisionId,
    revision: 2,
    source: {
      provider: 'generic_web',
      provider_item_id: 'page-1',
      source_item_key: 'generic:web:page-1',
      segment_key: 'root',
      canonical_url: 'https://example.com/notes/one',
    },
    renderer: 'article',
    display: {
      title: { value: 'A durable fragment', source: 'source', observation_id: 'observation-title' },
      description: { value: 'A source description.', source: 'source' },
      summary: {
        value: 'A compact summary that remains useful while background work continues.',
        source: 'deterministic',
      },
      published_at: '2026-09-03T18:42:00Z',
    },
    article: {
      preview_markdown: 'Preview text.',
      full_content_available: true,
      full_content_href: `/v1/reader/items/${fragmentId}/content?revision_id=${revisionId}`,
    },
    media: [],
    tags: {
      combined: ['systems', 'capture'],
      attributed: [
        { value: 'systems', source: 'user' },
        { value: 'capture', source: 'deterministic' },
      ],
    },
    annotations: [],
    capture_count: 2,
    reading_state: {
      principal_id: 'local-user',
      fragment_id: fragmentId,
      state: 'unread',
      position: { kind: 'none' },
      revision: 0,
    },
    operations: {
      triage: { case_ids: [], unresolved_count: 0 },
      routing: { state: 'none', references: [] },
      materialization: { state: 'none', references: [] },
      enrichment: [
        { capability: 'title', state: 'provided', observation_id: 'observation-title' },
        { capability: 'summary', state: 'pending' },
      ],
      acquisition: { pending: 0, available: 0, reference_only: 0, failed: 0 },
    },
    actions: [],
    ...overrides,
  }
}

export function readerList(scope: ReaderScope, items: ReaderItem[] = [readerItem()]): ReaderItemList {
  return {
    schema_version: 'fe.reader.list.v1',
    scope,
    items,
  }
}
