/* @vitest-environment jsdom */

import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { refreshPolledData, usePoll } from './usePoll'

function setDocumentHidden(hidden: boolean) {
  Object.defineProperty(document, 'hidden', {
    configurable: true,
    get: () => hidden,
  })
}

describe('usePoll', () => {
  beforeEach(() => {
    setDocumentHidden(false)
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
    setDocumentHidden(false)
  })

  it('loads data and exposes refetch', async () => {
    const fetcher = vi
      .fn<(signal: AbortSignal) => Promise<number>>()
      .mockResolvedValueOnce(1)
      .mockResolvedValueOnce(2)

    const { result } = renderHook(() => usePoll(fetcher, 5_000))

    await waitFor(() => expect(result.current.data).toBe(1))
    expect(result.current.isLoading).toBe(false)
    expect(result.current.error).toBeNull()

    await act(async () => {
      await result.current.refetch()
    })

    expect(result.current.data).toBe(2)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('pauses while the tab is hidden and resumes when visible again', async () => {
    vi.useFakeTimers()

    const fetcher = vi.fn<(signal: AbortSignal) => Promise<number>>().mockResolvedValue(1)

    renderHook(() => usePoll(fetcher, 1_000))

    await act(async () => {
      await Promise.resolve()
    })
    expect(fetcher).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000)
    })
    expect(fetcher).toHaveBeenCalledTimes(2)

    setDocumentHidden(true)
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })
    expect(fetcher).toHaveBeenCalledTimes(2)

    setDocumentHidden(false)
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })

    await act(async () => {
      await Promise.resolve()
    })
    expect(fetcher).toHaveBeenCalledTimes(3)
  })

  it('aborts the in-flight request on unmount', async () => {
    const state: { activeSignal?: AbortSignal } = {}
    const fetcher = vi.fn((signal: AbortSignal) => {
      state.activeSignal = signal
      return new Promise<number>(() => {})
    })

    const { unmount } = renderHook(() => usePoll(fetcher, 1_000))

    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
    if (!state.activeSignal) {
      throw new Error('Expected the fetcher to receive an AbortSignal.')
    }
    expect(state.activeSignal.aborted).toBe(false)

    unmount()

    expect(state.activeSignal.aborted).toBe(true)
  })

  it('responds to global refresh requests', async () => {
    const fetcher = vi
      .fn<(signal: AbortSignal) => Promise<number>>()
      .mockResolvedValueOnce(1)
      .mockResolvedValueOnce(2)

    renderHook(() => usePoll(fetcher, 60_000))

    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))

    act(() => {
      refreshPolledData()
    })

    await waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2))
  })
})
