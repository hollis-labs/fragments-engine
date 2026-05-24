import type { InboxItem } from '@/lib/types'

interface FragmentGalleryProps {
  items: InboxItem[]
  previewURL: (item: InboxItem) => string | undefined
  onOpenFragment?: (fragmentId: string) => void
}

export default function FragmentGallery({
  items,
  previewURL,
  onOpenFragment,
}: FragmentGalleryProps) {
  return (
    <div className="grid grid-cols-1 gap-4 p-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
      {items.map((item) => {
        const imageURL = previewURL(item)
        return (
          <button
            key={item.fragment_id}
            type="button"
            onClick={() => onOpenFragment?.(item.fragment_id)}
            className="group overflow-hidden rounded-xl border border-border bg-panel-2/40 text-left transition hover:border-border-strong hover:bg-panel-hover/50"
          >
            <div className="aspect-[4/3] overflow-hidden bg-bg">
              {imageURL ? (
                <img
                  src={imageURL}
                  alt={item.title || item.fragment_id}
                  className="h-full w-full object-cover transition duration-300 group-hover:scale-[1.02]"
                  loading="lazy"
                />
              ) : (
                <div className="flex h-full items-center justify-center text-[11px] uppercase tracking-[.18em] text-text-subtle">
                  No preview
                </div>
              )}
            </div>
            <div className="flex flex-col gap-2 p-3">
              <div className="flex items-center gap-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">
                <span>{item.source_type || item.source || 'fragment'}</span>
                <span className="rounded border border-border bg-bg px-1.5 py-0.5 text-text-soft">
                  {item.status}
                </span>
              </div>
              <div>
                <p className="line-clamp-2 text-[14px] leading-5 text-text">
                  {item.title || '(untitled fragment)'}
                </p>
                {item.reason && (
                  <p className="mt-1 line-clamp-3 text-[12px] leading-5 text-text-subtle">
                    {item.reason}
                  </p>
                )}
              </div>
            </div>
          </button>
        )
      })}
    </div>
  )
}
