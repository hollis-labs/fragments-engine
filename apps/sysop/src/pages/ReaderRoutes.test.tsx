import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { BrowserRouter, MemoryRouter, Route, Routes } from 'react-router-dom'
import { AppShell } from '@/App'
import { ApiProvider } from '@/contexts/ApiContext'
import { apiClient, type ApiClient } from '@/lib/api'
import type { ReaderItemList, ReaderScope } from '@/lib/types'
import ReaderDetailPage from './ReaderDetailPage'
import { imageFixture, mixedGalleryFixture, videoFixture } from '@/features/reader/fixtures'
import {
  legacyArticleItem,
  legacyMarkdownBody,
  readerItem,
  readerList,
} from '@/test/reader-fixture'

function clientWithReader(overrides: Partial<ApiClient> = {}): ApiClient {
  return {
    ...apiClient,
    fetchReaderItems: async ({ scope }: { scope: ReaderScope }) => readerList(scope),
    fetchReaderItem: async () => readerItem(),
    ...overrides,
  }
}

function renderMemoryShell(path: string, client: ApiClient) {
  return render(
    <ApiProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <AppShell />
      </MemoryRouter>
    </ApiProvider>,
  )
}

afterEach(() => {
  window.history.replaceState({}, '', '/')
})

describe('Reader routes', () => {
  it('canonicalizes a bare Reader route to the inbox scope', async () => {
    const fetchReaderItems = vi.fn(async ({ scope }: { scope: ReaderScope }) => readerList(scope))
    window.history.replaceState({}, '', '/reader')
    render(
      <ApiProvider client={clientWithReader({ fetchReaderItems })}>
        <BrowserRouter>
          <AppShell />
        </BrowserRouter>
      </ApiProvider>,
    )

    await screen.findByRole('link', { name: 'Open A durable fragment' })
    await waitFor(() => expect(window.location.search).toBe('?scope=inbox'))
    expect(fetchReaderItems.mock.calls[0][0]).toEqual({ scope: 'inbox' })
  })

  it('sends the query-bound scope to the batched endpoint and follows scope history links', async () => {
    const fetchReaderItems = vi.fn(async ({ scope }: { scope: ReaderScope }) => readerList(scope))
    const fetchReaderItem = vi.fn<ApiClient['fetchReaderItem']>(async () => readerItem())
    renderMemoryShell(
      '/reader?scope=library',
      clientWithReader({ fetchReaderItems, fetchReaderItem }),
    )

    await screen.findByRole('link', { name: 'Open A durable fragment' })
    expect(fetchReaderItems.mock.calls[0][0]).toEqual({ scope: 'library' })
    expect(fetchReaderItem).not.toHaveBeenCalled()
    expect(screen.getByRole('link', { name: 'Library' }).getAttribute('aria-current')).toBe('page')

    fireEvent.click(screen.getByRole('link', { name: 'All' }))
    await waitFor(() => {
      expect(fetchReaderItems.mock.calls.some(([params]) => params.scope === 'all')).toBe(true)
    })
    expect(screen.getByRole('link', { name: 'All' }).getAttribute('aria-current')).toBe('page')
  })

  it('renders loading, empty, and error states with a useful next step', async () => {
    let resolveList!: (value: ReaderItemList) => void
    const pending = new Promise<ReaderItemList>((resolve) => {
      resolveList = resolve
    })
    renderMemoryShell(
      '/reader?scope=inbox',
      clientWithReader({ fetchReaderItems: vi.fn(() => pending) }),
    )

    expect(screen.getByRole('status', { name: 'Loading Reader' })).not.toBeNull()
    await act(async () => resolveList(readerList('inbox', [])))
    expect(await screen.findByText('No fragments in inbox')).not.toBeNull()
  })

  it('renders a recoverable list error', async () => {
    renderMemoryShell(
      '/reader?scope=inbox',
      clientWithReader({
        fetchReaderItems: vi.fn(async () => {
          throw new Error('Local Reader is offline')
        }),
      }),
    )

    expect(await screen.findByText('Reader could not load')).not.toBeNull()
    expect(screen.getByText('Local Reader is offline')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Try again' })).not.toBeNull()
  })

  it('loads the next complete batch with the opaque cursor', async () => {
    const second = readerItem({
      fragment_id: 'fragment-2',
      display: {
        ...readerItem().display,
        title: { value: 'A second fragment', source: 'source' },
      },
    })
    const fetchReaderItems = vi.fn(async ({ scope, cursor }: { scope: ReaderScope; cursor?: string }) =>
      cursor
        ? readerList(scope, [second])
        : { ...readerList(scope), next_cursor: 'cursor-next' },
    )
    renderMemoryShell('/reader?scope=inbox', clientWithReader({ fetchReaderItems }))

    fireEvent.click(await screen.findByRole('button', { name: 'Load more' }))
    expect(await screen.findByRole('link', { name: 'Open A second fragment' })).not.toBeNull()
    expect(fetchReaderItems.mock.calls[1][0]).toEqual({
      scope: 'inbox',
      cursor: 'cursor-next',
    })
  })

  it('supports direct revision-pinned detail and in-place refresh', async () => {
    const fetchReaderItem = vi.fn<ApiClient['fetchReaderItem']>(async () => readerItem())
    renderMemoryShell(
      '/reader/fragment-1?revision_id=revision-1',
      clientWithReader({ fetchReaderItem }),
    )

    expect(await screen.findByText(/A compact summary/)).not.toBeNull()
    expect(fetchReaderItem.mock.calls[0]![0]).toEqual({
      fragmentId: 'fragment-1',
      revisionId: 'revision-1',
    })

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(fetchReaderItem).toHaveBeenCalledTimes(2))
  })

  it('rejects an empty historical revision without issuing a detail request', async () => {
    const fetchReaderItem = vi.fn<ApiClient['fetchReaderItem']>(async () => readerItem())
    renderMemoryShell('/reader/fragment-1?revision_id=', clientWithReader({ fetchReaderItem }))

    expect(await screen.findByText('The revision link is invalid.')).not.toBeNull()
    expect(fetchReaderItem).not.toHaveBeenCalled()
  })

  it('canonicalizes an alias response while preserving copy, back, and forward history', async () => {
    const alias = readerItem({
      fragment_id: 'legacy-alias',
      reading_state: { ...readerItem().reading_state, fragment_id: 'legacy-alias' },
    })
    const canonical = readerItem({ fragment_id: 'canonical-fragment' })
    const fetchReaderItem = vi.fn<ApiClient['fetchReaderItem']>(async () => canonical)
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    window.history.replaceState({}, '', '/reader?scope=inbox')
    render(
      <ApiProvider
        client={clientWithReader({
          fetchReaderItems: vi.fn(async () => readerList('inbox', [alias])),
          fetchReaderItem,
        })}
      >
        <BrowserRouter>
          <AppShell />
        </BrowserRouter>
      </ApiProvider>,
    )

    fireEvent.click(await screen.findByRole('link', { name: 'Open A durable fragment' }))
    await waitFor(() => expect(window.location.pathname).toBe('/reader/canonical-fragment'))
    expect(window.history.state.usr).toEqual({ readerReturnPath: '/reader?scope=inbox' })
    expect(fetchReaderItem.mock.calls[0]![0]).toEqual({
      fragmentId: 'legacy-alias',
      revisionId: undefined,
    })

    fireEvent.click(screen.getByRole('button', { name: 'Copy link' }))
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        `${window.location.origin}/reader/canonical-fragment`,
      ),
    )

    act(() => window.history.back())
    await waitFor(() => expect(window.location.pathname).toBe('/reader'))
    expect(window.location.search).toBe('?scope=inbox')

    act(() => window.history.forward())
    await waitFor(() => expect(window.location.pathname).toBe('/reader/canonical-fragment'))
  })

  it('retains a historical revision query while replacing an alias path', async () => {
    const canonical = readerItem({ fragment_id: 'canonical-history' })
    const fetchReaderItem = vi.fn<ApiClient['fetchReaderItem']>(async () => canonical)
    window.history.replaceState({}, '', '/reader/old-name?revision_id=revision-old')
    render(
      <ApiProvider client={clientWithReader({ fetchReaderItem })}>
        <BrowserRouter>
          <AppShell />
        </BrowserRouter>
      </ApiProvider>,
    )

    await waitFor(() => expect(window.location.pathname).toBe('/reader/canonical-history'))
    expect(window.location.search).toBe('?revision_id=revision-old')
    expect(fetchReaderItem.mock.calls[0]![0]).toEqual({
      fragmentId: 'old-name',
      revisionId: 'revision-old',
    })
  })

  it('exposes revision-pinned renderer, action, and sidecar composition seams', async () => {
    const item = readerItem({ fragment_revision_id: 'revision-pinned' })
    render(
      <ApiProvider client={clientWithReader({ fetchReaderItem: vi.fn(async () => item) })}>
        <MemoryRouter initialEntries={['/reader/fragment-1']}>
          <Routes>
            <Route
              path="/reader/:fragmentId"
              element={
                <ReaderDetailPage
                  renderContent={(_, pin) => <div>Renderer for {pin.fragmentRevisionId}</div>}
                  renderActions={(_, pin) => <button type="button">Act on {pin.fragmentRevisionId}</button>}
                  renderSidecar={(pin) => <div>Sidecar for {pin.fragmentRevisionId}</div>}
                />
              }
            />
          </Routes>
        </MemoryRouter>
      </ApiProvider>,
    )

    expect(await screen.findByText('Renderer for revision-pinned')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Act on revision-pinned' })).not.toBeNull()
    const sidecar = screen.getByText('Sidecar for revision-pinned').closest('aside')
    expect(sidecar?.getAttribute('data-fragment-id')).toBe('fragment-1')
    expect(sidecar?.getAttribute('data-fragment-revision-id')).toBe('revision-pinned')
  })

  it('wires the capability-driven action tray into Reader cards', async () => {
    const item = readerItem({
      actions: [
        {
          command: 'mark_read',
          input_schema:
            'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json#/$defs/MarkRead',
          expected_revision_required: true,
        },
      ],
    })
    renderMemoryShell(
      '/reader?scope=inbox',
      clientWithReader({ fetchReaderItems: async () => readerList('inbox', [item]) }),
    )

    expect(await screen.findByRole('button', { name: 'Open Reader actions' })).not.toBeNull()
  })

  it('shows one inert excerpt and the truthful no-visual state for a legacy article card', async () => {
    const item = legacyArticleItem()
    const { container } = renderMemoryShell(
      '/reader?scope=inbox',
      clientWithReader({ fetchReaderItems: async () => readerList('inbox', [item]) }),
    )

    const card = await screen.findByRole('link', { name: 'Open Legacy captured article' })
    expect(within(card).getAllByText(/Some captured prose/)).toHaveLength(1)
    expect(within(card).getByText('No captured visual')).not.toBeNull()
    expect(within(card).getByText('Source reference only')).not.toBeNull()
    expect(card.textContent).not.toMatch(/profile picture|https?:|!\[|\]\(/i)
    expect(container.querySelector('[data-reader-card-visual]')).toBeNull()
  })

  it('uses one bounded static authorized visual for rich image, gallery, and video cards', async () => {
    const items = [
      readerItem({
        fragment_id: 'rich-image',
        renderer: 'image',
        display: { ...readerItem().display, title: { value: 'Rich image', source: 'source' } },
        media: imageFixture.media,
      }),
      readerItem({
        fragment_id: 'rich-gallery',
        renderer: 'gallery',
        display: { ...readerItem().display, title: { value: 'Rich gallery', source: 'source' } },
        media: mixedGalleryFixture.media,
      }),
      readerItem({
        fragment_id: 'rich-video',
        renderer: 'video',
        display: { ...readerItem().display, title: { value: 'Rich video', source: 'source' } },
        media: videoFixture.media,
        playback: {
          kind: 'provider_embed',
          provider: 'youtube',
          provider_item_id: '3RmtNXqnreI',
        },
      }),
    ]
    const { container } = renderMemoryShell(
      '/reader?scope=inbox',
      clientWithReader({ fetchReaderItems: async () => readerList('inbox', items) }),
    )

    await screen.findByRole('link', { name: 'Open Rich video' })
    for (const item of items) {
      const card = container.querySelector(`[data-fragment-id="${item.fragment_id}"]`)
      expect(card).not.toBeNull()
      const mediaSlot = card!.querySelector('[data-reader-media-slot]')
      expect(mediaSlot?.querySelectorAll('img')).toHaveLength(1)
      expect(mediaSlot?.querySelector('[data-reader-card-visual]')?.className).toContain('max-h-40')
      expect(mediaSlot?.querySelector('button')).toBeNull()
      expect(mediaSlot?.querySelector('iframe')).toBeNull()
      expect(mediaSlot?.querySelector('[role="dialog"]')).toBeNull()
      expect(mediaSlot?.querySelector('[data-transcript-state]')).toBeNull()
    }
  })

  it('suppresses a body-backed Markdown deck and puts the reading stage before operations', async () => {
    const revisionId = '6a2041fe4d0ae70b29f41d6ed8338ed86fe593cbf794025a2a95ff8cf29f35b0'
    const item = legacyArticleItem({
      fragment_id: 'legacy-detail',
      fragment_revision_id: revisionId,
      display: {
        ...legacyArticleItem().display,
        summary: { value: legacyMarkdownBody, source: 'deterministic' },
        description: { value: legacyMarkdownBody, source: 'source' },
      },
      article: {
        preview_markdown: legacyMarkdownBody,
        full_content_available: true,
        full_content_href: `/v1/reader/items/legacy-detail/content?revision_id=${revisionId}`,
      },
    })
    const { container } = render(
      <ApiProvider client={clientWithReader({ fetchReaderItem: async () => item })}>
        <MemoryRouter initialEntries={['/reader/legacy-detail']}>
          <Routes>
            <Route
              path="/reader/:fragmentId"
              element={<ReaderDetailPage renderContent={() => <div>Sanitized article body</div>} />}
            />
          </Routes>
        </MemoryRouter>
      </ApiProvider>,
    )

    await screen.findByText('Sanitized article body')
    expect(screen.queryByText(/Some captured prose/)).toBeNull()
    const revision = screen.getByLabelText(`Revision ${revisionId}`)
    expect(revision.textContent).toBe('Revision 6a2041fe4d0a…')
    expect(revision.getAttribute('title')).toBe(`Revision ${revisionId}`)
    const readingStage = container.querySelector('[data-reader-reading-stage]')!
    const operations = screen.getByLabelText('Fragment processing states')
    expect(readingStage.compareDocumentPosition(operations) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
  })

  it('preserves a genuinely distinct enriched summary and provider description on detail', async () => {
    const item = legacyArticleItem({
      display: {
        ...legacyArticleItem().display,
        summary: { value: 'A genuinely enriched synthesis.', source: 'model' },
        description: { value: 'A distinct provider description.', source: 'provider' },
      },
    })
    renderMemoryShell(
      `/reader/${item.fragment_id}`,
      clientWithReader({ fetchReaderItem: async () => item }),
    )

    expect(await screen.findByText('A genuinely enriched synthesis.')).not.toBeNull()
    expect(screen.getByText('A distinct provider description.')).not.toBeNull()
  })

  it('uses the real action tray by default on detail while retaining the injected seam', async () => {
    const item = readerItem({
      actions: [
        {
          command: 'mark_unread',
          input_schema:
            'https://schemas.hollis-labs.dev/fragments-engine/browser-capture-reader/v1/reader-command.schema.json#/$defs/MarkUnread',
          expected_revision_required: true,
        },
      ],
    })
    renderMemoryShell(
      '/reader/fragment-1',
      clientWithReader({ fetchReaderItem: async () => item }),
    )

    expect(await screen.findByRole('button', { name: 'Open Reader actions' })).not.toBeNull()
  })
})
