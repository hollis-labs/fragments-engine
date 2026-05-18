import { StatusBadge, CopyableId, formatRelativeTime, formatShortDate } from '@hollis-labs/sysop-ui'
import type { InboxItem } from '@/lib/types'

interface FragmentRowProps {
  item: InboxItem
  selected?: boolean
  onSelect?: (id: string, selected: boolean) => void
  /** Opens the fragment detail modal. */
  onOpen?: (fragmentId: string) => void
}

// Descendants marked data-row-interactive own their own clicks and must not
// trigger row-level open (checkbox, copy buttons).
const INTERACTIVE_SELECTOR = '[data-row-interactive="true"]'

/** Shorten a long opaque id for inline display while keeping copy-of-full. */
function shortId(id: string): string {
  return id.length > 12 ? `${id.slice(0, 12)}…` : id
}

/** A single inbox fragment row — mirrors Torque's TaskRow. */
export function FragmentRow({ item, selected, onSelect, onOpen }: FragmentRowProps) {
  const dateStr = formatShortDate(item.staged_at)
  const agoStr = formatRelativeTime(item.staged_at)

  function handleOpen(e: React.MouseEvent<HTMLTableRowElement>) {
    if (!onOpen) return
    if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    const target = e.target as HTMLElement | null
    if (target?.closest(INTERACTIVE_SELECTOR)) return
    onOpen(item.fragment_id)
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTableRowElement>) {
    if (!onOpen || e.target !== e.currentTarget) return
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onOpen(item.fragment_id)
    }
  }

  return (
    <tr
      className={`outline-none focus-visible:ring-1 focus-visible:ring-ring ${onOpen ? 'cursor-pointer' : ''} ${selected ? 'bg-panel-hover/70' : 'bg-bg hover:bg-panel-hover/60'}`}
      data-testid="fragment-row"
      onClick={handleOpen}
      onKeyDown={handleKeyDown}
      tabIndex={onOpen ? 0 : undefined}
      role={onOpen ? 'button' : undefined}
      aria-label={onOpen ? `Open fragment ${item.title || item.fragment_id}` : undefined}
    >
      {onSelect && (
        <td className="w-8 align-top py-1.5 pl-4 pr-0">
          <input
            type="checkbox"
            checked={selected ?? false}
            onChange={(e) => onSelect(item.fragment_id, e.target.checked)}
            className="mt-[3px] h-3 w-3 cursor-pointer appearance-none rounded-sm border border-border-strong bg-panel-2 checked:border-text-soft checked:bg-text-soft"
            aria-label={`Select fragment ${item.title || item.fragment_id}`}
            data-row-interactive="true"
          />
        </td>
      )}
      {/* Title + source / id / reason subtitle */}
      <td className="w-full max-w-0 px-3 py-1.5 text-left align-top">
        <div className="min-w-0">
          <span
            className="block truncate tracking-[.02em] text-text"
            title={item.title || item.fragment_id}
          >
            {item.title || '(untitled fragment)'}
          </span>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
            {item.source_type && (
              <span className="text-[10px] text-text-subtle/80">{item.source_type}</span>
            )}
            <span className="font-mono text-[10px] text-text-subtle/80">id:</span>
            <CopyableId id={item.fragment_id} label={shortId(item.fragment_id)} />
            {item.reason && (
              <>
                <span className="font-mono text-[10px] text-text-subtle/80">reason:</span>
                <span className="font-mono text-[10px] text-text-subtle" title={item.reason}>
                  {item.reason}
                </span>
              </>
            )}
          </div>
        </div>
      </td>
      {/* Status */}
      <td className="w-px whitespace-nowrap px-1.5 py-1.5">
        <StatusBadge status={item.status} />
      </td>
      {/* Source */}
      <td className="w-px whitespace-nowrap px-1.5 py-1.5">
        <span className="text-[11px] uppercase tracking-[.12em] text-text-soft">
          {item.source || '—'}
        </span>
      </td>
      {/* Route */}
      <td className="w-px whitespace-nowrap px-1.5 py-1.5">
        {item.route_id ? (
          <CopyableId id={item.route_id} label={shortId(item.route_id)} />
        ) : (
          <span className="text-[11px] uppercase tracking-[.12em] text-text-subtle/70">unrouted</span>
        )}
      </td>
      {/* Staged */}
      <td className="w-px whitespace-nowrap px-1.5 py-1.5 pr-3">
        <div className="text-[11px] uppercase leading-4 tracking-[.12em] text-text-soft">{dateStr}</div>
        <div className="text-[11px] uppercase leading-4 tracking-[.12em] text-text-subtle/80">
          {agoStr}
        </div>
      </td>
    </tr>
  )
}
