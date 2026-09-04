import { describe, expect, it } from 'vitest'
import {
  acquisitionPresentation,
  boundedReaderText,
  enrichmentPresentation,
  isReaderBodyBackedText,
  readerPlainTextExcerpt,
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

  it('turns Markdown-shaped capture copy into a bounded inert excerpt without remote URLs', () => {
    const markdown = `
      # Join the conversation
      [![profile picture](https://cdn.example/avatar.jpg)](https://social.example/profile)
      Some **captured prose** with [a useful label](https://example.com/long/path),
      a bare https://evil.example/tracker and <script>alert('no')</script>.
    `

    const excerpt = readerPlainTextExcerpt(markdown, 200)
    expect(excerpt).toBe('Join the conversation Some captured prose with a useful label, a bare and .')
    expect(excerpt).not.toMatch(/https?:|!\[|\]\(|<script|profile picture/i)
  })

  it('only identifies exact nonempty normalized body copies as body-backed decks', () => {
    expect(isReaderBodyBackedText('Same\n body', ' Same body ')).toBe(true)
    expect(isReaderBodyBackedText('An enriched summary', 'A longer captured body')).toBe(false)
    expect(isReaderBodyBackedText('', '')).toBe(false)
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
