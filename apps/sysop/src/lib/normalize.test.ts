import { describe, expect, it } from 'vitest'

import {
  normalizeFragment,
  normalizeFragmentAttachment,
  normalizeInboxEntityGroups,
  normalizeOptionalDateString,
  normalizeSearchResult,
  parseMetadataJson,
} from './normalize'

describe('parseMetadataJson', () => {
  it('returns an empty object for empty metadata json', () => {
    expect(parseMetadataJson('')).toEqual({})
    expect(parseMetadataJson('   ')).toEqual({})
  })
})

describe('normalizeOptionalDateString', () => {
  it('drops null and Go zero-value timestamps', () => {
    expect(normalizeOptionalDateString(null)).toBeUndefined()
    expect(normalizeOptionalDateString('0001-01-01T00:00:00Z')).toBeUndefined()
  })
})

describe('normalizeFragment', () => {
  it('normalizes PascalCase fragments into stable snake_case objects', () => {
    const fragment = normalizeFragment({
      ID: 'frag-1',
      Source: 'gmail',
      SourceType: 'email',
      SourceID: 'src-1',
      Title: 'Subject',
      Content: 'Body',
      ContentHash: 'hash',
      CreatedAt: '2026-05-15T10:00:00Z',
      IngestedAt: '2026-05-15T10:05:00Z',
      Status: 'indexed',
      Summary: '',
      IndexedAt: '0001-01-01T00:00:00Z',
      MetadataJSON: '{"thread_id":"abc"}',
      IngestName: 'gmail-sync',
      CanonicalPath: '/mail/abc',
    })

    expect(fragment.summary).toBe('')
    expect(fragment.indexed_at).toBeUndefined()
    expect(fragment.metadata_json).toBe('{"thread_id":"abc"}')
    expect(fragment.metadata).toEqual({ thread_id: 'abc' })
  })
})

describe('normalizeFragmentAttachment', () => {
  it('parses attachment metadata json into a typed object', () => {
    const attachment = normalizeFragmentAttachment({
      ID: 'att-1',
      Kind: 'image',
      Role: 'primary',
      Name: 'scan.png',
      MIMEType: 'image/png',
      Source: 'gmail',
      MetadataJSON: '{"page_count":2}',
      CreatedAt: '2026-05-15T10:00:00Z',
    })

    expect(attachment.metadata_json).toBe('{"page_count":2}')
    expect(attachment.metadata).toEqual({ page_count: 2 })
  })
})

describe('normalizeInboxEntityGroups', () => {
  it('flattens nested entity group arrays and normalizes keys', () => {
    const groups = normalizeInboxEntityGroups([
      { Kind: 'person', Value: 'Ada', FragmentCount: 2 },
      [{ kind: 'topic', value: 'systems', fragment_count: 1 }],
    ])

    expect(groups).toEqual([
      { kind: 'person', value: 'Ada', fragment_count: 2 },
      { kind: 'topic', value: 'systems', fragment_count: 1 },
    ])
  })
})

describe('normalizeSearchResult', () => {
  it('maps Trace into recall_trace and normalizes the nested fragment', () => {
    const result = normalizeSearchResult({
      Fragment: {
        ID: 'frag-2',
        Source: 'slack',
        SourceType: 'message',
        SourceID: 'src-2',
        Title: 'Standup',
        Content: 'Done',
        ContentHash: 'hash-2',
        CreatedAt: '2026-05-15T10:00:00Z',
        IngestedAt: '2026-05-15T10:01:00Z',
        Status: 'inbox',
        Summary: 'Done',
        IndexedAt: null,
        MetadataJSON: '',
        IngestName: 'slack-sync',
        CanonicalPath: '/slack/2',
      },
      Score: 0.91,
      Snippet: 'Done',
      Trace: {
        Backend: 'keyword',
        Strategy: 'bm25',
      },
    })

    expect(result.fragment.id).toBe('frag-2')
    expect(result.fragment.metadata).toEqual({})
    expect(result.recall_trace).toEqual({
      backend: 'keyword',
      strategy: 'bm25',
    })
  })
})
