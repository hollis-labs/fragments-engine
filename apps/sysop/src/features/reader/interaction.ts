import type React from 'react'

export const readerControlClass =
  'reader-control min-h-11 rounded-md focus-visible:outline-none motion-reduce:transition-none'

export function stopReaderNavigation(event: React.SyntheticEvent): void {
  event.stopPropagation()
}
