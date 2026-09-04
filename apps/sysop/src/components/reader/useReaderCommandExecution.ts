import { useCallback, useState } from 'react'

import { useApi } from '@/hooks/useApi'
import { ApiError } from '@/lib/api'
import { optimisticReaderItem } from '@/lib/reader-actions'
import type { ReaderCommand, ReaderCommandName, ReaderItem } from '@/lib/types'

export type ReaderCommandFeedback =
  | { state: 'idle'; message?: undefined }
  | { state: 'pending' | 'success' | 'conflict' | 'failure' | 'uncertain'; message: string }

const SUCCESS_LABELS: Record<ReaderCommandName, string> = {
  add_tag: 'Tag added',
  remove_tag: 'Tag removed',
  append_capture_note: 'Capture note saved',
  update_curated_note: 'Curated note saved',
  set_reading_progress: 'Reading position saved',
  mark_read: 'Marked read',
  mark_unread: 'Marked unread',
  request_asset_acquisition: 'Media requested',
  route: 'Route requested',
  materialize: 'Materialization requested',
}

export function useReaderCommandExecution(
  item: ReaderItem,
  onItemChange: (item: ReaderItem) => void,
) {
  const api = useApi()
  const [feedback, setFeedback] = useState<ReaderCommandFeedback>({ state: 'idle' })
  const [retryCommand, setRetryCommand] = useState<ReaderCommand>()

  const execute = useCallback(
    async (command: ReaderCommand): Promise<boolean> => {
      const before = item
      setRetryCommand(undefined)
      setFeedback({ state: 'pending', message: 'Saving…' })
      onItemChange(optimisticReaderItem(before, command))

      try {
        const response = await api.executeReaderCommand({
          fragmentId: before.fragment_id,
          command,
        })
        onItemChange(response)
        setFeedback({ state: 'success', message: SUCCESS_LABELS[command.command] })
        return true
      } catch (reason) {
        onItemChange(before)
        if (reason instanceof ApiError && reason.status === 409) {
          setFeedback({
            state: 'conflict',
            message: 'Another change won. Your draft is preserved.',
          })
          try {
            const authoritative = await api.fetchReaderItem({ fragmentId: before.fragment_id })
            onItemChange(authoritative)
          } catch {
            setFeedback({
              state: 'conflict',
              message: 'Another change won. Refresh to see the current item.',
            })
          }
        } else if (!(reason instanceof ApiError)) {
          setRetryCommand(command)
          setFeedback({
            state: 'uncertain',
            message: 'The network result is uncertain. Retry sends the same change.',
          })
        } else {
          setFeedback({ state: 'failure', message: reason.message || 'The change could not be saved.' })
        }
        return false
      }
    },
    [api, item, onItemChange],
  )

  const retry = useCallback(async () => {
    if (!retryCommand) return false
    return execute(retryCommand)
  }, [execute, retryCommand])

  return {
    execute,
    feedback,
    pending: feedback.state === 'pending',
    retry,
    retryAvailable: Boolean(retryCommand),
    resetFeedback: () => setFeedback({ state: 'idle' }),
  }
}
