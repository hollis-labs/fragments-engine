import type { ReactNode } from 'react'
import { Button } from '@hollis-labs/sysop-ui'

interface ReaderQuickActionSeamProps {
  children?: ReactNode
}

/**
 * Stable 0047 composition point. The default advertises the location without
 * executing or implying support for any Reader command.
 */
export function ReaderQuickActionSeam({ children }: ReaderQuickActionSeamProps) {
  return (
    <div data-reader-action-slot data-reader-nav-exclude>
      {children ?? (
        <Button
          variant="ghost"
          size="sm"
          className="min-h-11 sm:min-h-8"
          disabled
          aria-label="Actions are not available yet"
          title="Quick actions are not available yet"
        >
          Actions
        </Button>
      )}
    </div>
  )
}
