import { afterEach, describe, expect, it, vi } from 'vitest'
import { executeReaderCommand, fetchReaderItem, fetchReaderItems } from './api'
import { readerItem, readerList } from '@/test/reader-fixture'

function jsonResponse(value: unknown): Response {
  return new Response(JSON.stringify(value), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Reader API client', () => {
  it('passes the selected scope and opaque cursor to the batched list endpoint', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(readerList('library')))
    vi.stubGlobal('fetch', fetchMock)

    await fetchReaderItems({ scope: 'library', cursor: 'opaque/cursor+=' })

    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/v1/reader/items?scope=library&cursor=opaque%2Fcursor%2B%3D')
    expect(init?.credentials).toBe('same-origin')
  })

  it('encodes fragment IDs and preserves a revision pin for detail', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(readerItem()))
    vi.stubGlobal('fetch', fetchMock)

    await fetchReaderItem({ fragmentId: 'fragment alias', revisionId: 'revision-4' })

    expect(fetchMock.mock.calls[0]![0]).toBe(
      '/v1/reader/items/fragment%20alias?revision_id=revision-4',
    )
  })

  it('posts the exact frozen command and returns the reconciled Reader item', async () => {
    const reconciled = readerItem({ revision: 3 })
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(reconciled))
    vi.stubGlobal('fetch', fetchMock)
    const command = {
      schema_version: 'fe.reader.command.v1',
      command: 'add_tag',
      command_id: 'command-reader-api',
      idempotency_key: 'intent-reader-api',
      expected_revision: 2,
      tag: 'durable',
    } as const

    await expect(
      executeReaderCommand({ fragmentId: 'fragment alias', command }),
    ).resolves.toEqual(reconciled)

    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/v1/reader/items/fragment%20alias/commands')
    expect(init?.method).toBe('POST')
    expect(init?.headers).toMatchObject({
      Accept: 'application/json',
      'Content-Type': 'application/json',
    })
    expect(init?.body).toBe(JSON.stringify(command))
  })
})
