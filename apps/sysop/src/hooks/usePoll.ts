import { useEffect, useEffectEvent, useRef, useState } from 'react'

export interface UsePollResult<TData> {
  data: TData | null
  error: unknown
  isLoading: boolean
  refetch: () => Promise<void>
}

export type PollFetcher<TData> = (signal: AbortSignal) => Promise<TData>

type RefreshListener = () => void

const DEFAULT_INTERVAL_MS = 15_000
const refreshListeners = new Set<RefreshListener>()

function subscribeToRefresh(listener: RefreshListener) {
  refreshListeners.add(listener)
  return () => {
    refreshListeners.delete(listener)
  }
}

export function refreshPolledData() {
  for (const listener of refreshListeners) {
    listener()
  }
}

export function usePoll<TData>(
  fetcher: PollFetcher<TData>,
  intervalMs = DEFAULT_INTERVAL_MS,
): UsePollResult<TData> {
  const [data, setData] = useState<TData | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [isLoading, setIsLoading] = useState(true)

  const isMountedRef = useRef(true)
  const intervalRef = useRef<number | null>(null)
  const abortControllerRef = useRef<AbortController | null>(null)
  const dataRef = useRef<TData | null>(null)

  const clearScheduledPoll = useEffectEvent(() => {
    if (intervalRef.current !== null) {
      window.clearTimeout(intervalRef.current)
      intervalRef.current = null
    }
  })

  const scheduleNextPoll = useEffectEvent(() => {
    clearScheduledPoll()
    if (document.hidden || intervalMs <= 0) {
      return
    }
    intervalRef.current = window.setTimeout(() => {
      void runFetch(false)
    }, intervalMs)
  })

  const runFetch = useEffectEvent(async (showLoading: boolean) => {
    abortControllerRef.current?.abort()
    const controller = new AbortController()
    abortControllerRef.current = controller

    if (showLoading) {
      setIsLoading(true)
    }

    try {
      const nextData = await fetcher(controller.signal)
      if (!isMountedRef.current || controller.signal.aborted) {
        return
      }

      setData(nextData)
      dataRef.current = nextData
      setError(null)
    } catch (cause) {
      if (!isMountedRef.current || controller.signal.aborted) {
        return
      }

      setError(cause)
    } finally {
      if (!isMountedRef.current || controller.signal.aborted) {
        return
      }

      setIsLoading(false)
      scheduleNextPoll()
    }
  })

  useEffect(() => {
    isMountedRef.current = true
    void runFetch(true)

    return () => {
      isMountedRef.current = false
      clearScheduledPoll()
      abortControllerRef.current?.abort()
    }
  }, [])

  useEffect(() => {
    const unsubscribe = subscribeToRefresh(() => {
      void runFetch(dataRef.current === null)
    })

    return unsubscribe
  }, [])

  useEffect(() => {
    const handleVisibilityChange = () => {
      if (document.hidden) {
        clearScheduledPoll()
        abortControllerRef.current?.abort()
        return
      }

      void runFetch(dataRef.current === null)
    }

    document.addEventListener('visibilitychange', handleVisibilityChange)
    return () => {
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  }, [])

  const refetch = useEffectEvent(async () => {
    await runFetch(dataRef.current === null)
  })

  return {
    data,
    error,
    isLoading,
    refetch,
  }
}

// TODO: Phase 2: migrate to SSE once FE emits events.
