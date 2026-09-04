import { describe, expect, it } from 'vitest'
import {
  acquisitionPresentation,
  boundedReaderText,
  enrichmentPresentation,
  safeReaderSourceHref,
} from './reader'
import { readerItem } from '@/test/reader-fixture'

describe('Reader presentation boundaries', () => {
  it('only permits absolute HTTP(S) source links', () => {
    expect(safeReaderSourceHref(readerItem())).toBe('https://example.com/notes/one')
    expect(
      safeReaderSourceHref(
        readerItem({ source: { ...readerItem().source, canonical_url: 'javascript:alert(1)' } }),
      ),
    ).toBeUndefined()
    expect(
      safeReaderSourceHref(
        readerItem({ source: { ...readerItem().source, canonical_url: 'not a URL' } }),
      ),
    ).toBeUndefined()
  })

  it('bounds summaries by Unicode characters after normalizing whitespace', () => {
    expect(boundedReaderText('  one\n two  ', 20)).toBe('one two')
    expect(boundedReaderText('🙂🙂🙂🙂', 3)).toBe('🙂🙂…')
  })

  it('keeps pending and partial enrichment/media states distinct', () => {
    expect(
      enrichmentPresentation([
        { capability: 'summary', state: 'pending' },
        { capability: 'OCR', state: 'failed' },
      ]),
    ).toEqual({ value: '1 pending · 1 failed', tone: 'attention' })
    expect(
      acquisitionPresentation({ pending: 1, available: 2, reference_only: 0, failed: 0 }),
    ).toEqual({ value: '1 pending · 2 local', tone: 'attention' })
  })
})
