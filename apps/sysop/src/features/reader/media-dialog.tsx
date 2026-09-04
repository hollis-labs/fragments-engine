import { useEffect, useRef, type KeyboardEvent, type RefObject } from 'react'
import { X } from 'lucide-react'
import {
  Button,
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from '@hollis-labs/sysop-ui'

import { readerControlClass, stopReaderNavigation } from './interaction'

interface MediaDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  returnFocusRef: RefObject<HTMLElement | null>
  title: string
  description: string
  children: React.ReactNode
  onKeyDown?: (event: KeyboardEvent<HTMLDivElement>) => void
  minimal?: boolean
}

export function MediaDialog({
  open,
  onOpenChange,
  returnFocusRef,
  title,
  description,
  children,
  onKeyDown,
  minimal = false,
}: MediaDialogProps) {
  const wasOpen = useRef(open)

  useEffect(() => {
    if (wasOpen.current && !open) {
      queueMicrotask(() => returnFocusRef.current?.focus())
    }
    wasOpen.current = open
  }, [open, returnFocusRef])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        data-reader-dialog
        widthClassName="max-w-[calc(100%-1rem)] sm:max-w-5xl"
        className="max-h-[calc(100dvh-1rem)] overflow-hidden p-0 motion-reduce:animate-none"
        showCloseButton={false}
        onClick={stopReaderNavigation}
        onKeyDown={onKeyDown}
      >
        <div className={minimal ? 'sr-only' : 'flex items-start gap-4 border-b border-border px-4 py-3 pr-16'}>
          <div className="min-w-0">
            <DialogTitle className="truncate text-base font-semibold leading-6 text-text">{title}</DialogTitle>
            <DialogDescription className="mt-0.5 text-xs leading-5 text-text-subtle">{description}</DialogDescription>
          </div>
        </div>
        <div className={minimal ? 'absolute right-2 top-2 z-10' : ''}>
          <DialogClose
            render={
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className={`${readerControlClass} ${minimal ? 'border border-border-strong bg-panel-overlay-strong/95' : 'absolute right-2 top-2'}`}
                aria-label={`Close ${title}`}
              />
            }
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </DialogClose>
        </div>
        {children}
      </DialogContent>
    </Dialog>
  )
}
