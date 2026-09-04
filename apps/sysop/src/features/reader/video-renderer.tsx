import { useRef, useState } from 'react'
import { Expand, FileText, Play } from 'lucide-react'
import { Button, cn } from '@hollis-labs/sysop-ui'

import { ArticleRenderer } from './article-renderer'
import { ReaderCardVisual } from './card-visual'
import { readerControlClass, stopReaderNavigation } from './interaction'
import {
  availableVariantHref,
  imageVariants,
  mediaState,
  orderedMedia,
  transcriptResource,
  trustedYouTubeEmbedURL,
} from './media'
import { MediaDialog } from './media-dialog'
import { ResourceStatePanel } from './resource-state'
import type { ReaderRenderableItem, ReaderRendererProps } from './types'

const PLAYER_SANDBOX = 'allow-scripts allow-same-origin allow-presentation'
const PLAYER_PERMISSIONS = 'encrypted-media; picture-in-picture; fullscreen'

function posterHref(item: ReaderRenderableItem): string | undefined {
  const media = orderedMedia(item.media)
  for (const video of media.filter((entry) => entry.kind === 'video')) {
    const variant = video.variants.find(
      (entry) =>
        ['poster', 'preview', 'thumbnail'].includes(entry.kind) &&
        availableVariantHref(entry) !== undefined,
    )
    const href = variant && availableVariantHref(variant)
    if (href) return href
  }
  for (const candidate of media.filter(
    (entry) => entry.kind === 'image' && entry.attachment.role === 'poster',
  )) {
    const poster = imageVariants(candidate).preview
    const href = poster && availableVariantHref(poster)
    if (href) return href
  }
  return undefined
}

function YouTubePlayer({ src, title, large = false }: { src: string; title: string; large?: boolean }) {
  return (
    <iframe
      className={cn(
        'reader-frame aspect-video w-full border-0 bg-panel-2',
        large && 'max-h-[calc(100dvh-9rem)]',
      )}
      src={src}
      title={`YouTube video: ${title || 'Untitled item'}`}
      sandbox={PLAYER_SANDBOX}
      allow={PLAYER_PERMISSIONS}
      allowFullScreen
      referrerPolicy="strict-origin-when-cross-origin"
      loading="lazy"
    />
  )
}

function VideoMeta({ item }: { item: ReaderRenderableItem }) {
  const values = [
    item.display.byline?.value.trim(),
    item.display.published_at
      ? new Date(item.display.published_at).toLocaleDateString(undefined, {
          year: 'numeric',
          month: 'short',
          day: 'numeric',
        })
      : undefined,
  ].filter((value): value is string => Boolean(value))
  const tags = item.tags?.combined ?? []
  if (values.length === 0 && tags.length === 0) return null

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs leading-5 text-text-subtle">
      {values.map((value) => (
        <span key={value}>{value}</span>
      ))}
      {tags.slice(0, 5).map((tag) => (
        <span key={tag} className="rounded-md border border-border bg-panel-2 px-2 py-0.5 text-text-soft">
          {tag}
        </span>
      ))}
    </div>
  )
}

export function VideoRenderer({ item, presentation, className }: ReaderRendererProps) {
  const embedURL = trustedYouTubeEmbedURL(item.playback)
  const poster = posterHref(item)
  const videoMedia = item.media.find((entry) => entry.kind === 'video')
  const playbackState = videoMedia ? mediaState(videoMedia) : 'unavailable'
  const transcript = transcriptResource(item.media)
  const [playerLoaded, setPlayerLoaded] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const expandRef = useRef<HTMLButtonElement>(null)
  const title = item.display.title.value || 'Untitled video'

  if (presentation === 'card') return <ReaderCardVisual item={item} className={className} />

  return (
    <div
      className={cn('min-w-0', className)}
      data-reader-renderer="video"
      data-reader-presentation={presentation}
    >
      <div className="overflow-hidden rounded-md border border-border bg-panel-2">
        <div className="relative aspect-video w-full overflow-hidden bg-bg">
          {embedURL && playerLoaded ? (
            <YouTubePlayer src={embedURL} title={title} />
          ) : poster ? (
            <img src={poster} alt={`${title} poster`} className="h-full w-full object-contain" loading="lazy" />
          ) : (
            <div className="flex h-full items-center justify-center p-6 text-center text-sm text-text-subtle">
              {embedURL ? 'Trusted YouTube player ready' : 'No trusted player or poster is available'}
            </div>
          )}

          {embedURL && !playerLoaded && (
            <button
              type="button"
              className={`${readerControlClass} absolute inset-x-3 bottom-3 mx-auto flex w-fit items-center gap-2 border border-border-strong bg-panel-overlay-strong/95 px-4 text-sm font-medium text-text`}
              onClick={(event) => {
                stopReaderNavigation(event)
                setPlayerLoaded(true)
              }}
              onKeyDown={stopReaderNavigation}
            >
              <Play className="h-4 w-4" aria-hidden="true" />
              Load trusted YouTube player
            </button>
          )}
        </div>

        {embedURL && (
          <div className="flex justify-end border-t border-border px-3 py-2">
            <Button
              ref={expandRef}
              type="button"
              variant="outline"
              className={readerControlClass}
              onClick={(event) => {
                stopReaderNavigation(event)
                setExpanded(true)
              }}
              onKeyDown={stopReaderNavigation}
            >
              <Expand className="h-4 w-4" aria-hidden="true" />
              Expand video
            </Button>
          </div>
        )}
      </div>

      {!embedURL && (
        <ResourceStatePanel
          className="mt-3"
          state={playbackState === 'available' ? 'unavailable' : playbackState}
          label="Trusted playback unavailable"
          description="Reader only plays a server-validated YouTube provider identity. Captured text and media state remain available below."
        />
      )}

      <div className="mt-4 grid gap-4 lg:grid-cols-[minmax(0,1fr)_16rem]">
        <div className="min-w-0">
          <VideoMeta item={item} />
          {item.display.description?.value && (
            <p className="mt-3 whitespace-pre-wrap text-sm leading-6 text-text-muted">
              {item.display.description.value}
            </p>
          )}
        </div>
        <aside className="rounded-md border border-border bg-panel-2/40 px-3 py-3" aria-label="Transcript status">
          <div className="flex items-center gap-2 text-xs font-medium leading-5 text-text-soft">
            <FileText className="h-4 w-4" aria-hidden="true" />
            Transcript
          </div>
          <p className="mt-1 text-xs leading-5 text-text-subtle" data-transcript-state={transcript.state}>
            {transcript.label}
          </p>
          {transcript.href && (
            <a
              href={transcript.href}
              target="_blank"
              rel="noreferrer"
              className={`${readerControlClass} mt-2 inline-flex items-center px-3 text-xs font-medium text-text underline decoration-border-strong underline-offset-4 hover:decoration-text`}
              onClick={stopReaderNavigation}
              onKeyDown={stopReaderNavigation}
            >
              Open transcript
            </a>
          )}
        </aside>
      </div>

      <div className="mt-5 border-t border-border pt-5">
        <h3 className="mb-3 text-sm font-semibold leading-6 text-text">Captured page</h3>
        <ArticleRenderer item={item} presentation="detail" />
      </div>

      {embedURL && (
        <MediaDialog
          open={expanded}
          onOpenChange={setExpanded}
          returnFocusRef={expandRef}
          title={title}
          description="Trusted YouTube player"
        >
          <div className="flex max-h-[calc(100dvh-6rem)] items-center justify-center bg-bg">
            <YouTubePlayer src={embedURL} title={title} large />
          </div>
        </MediaDialog>
      )}
    </div>
  )
}
