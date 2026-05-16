import { useEffect, useMemo, useRef, useState, type RefObject } from 'react'
import { FragmentRow } from './fragment-row'
import type { InboxItem } from '@/lib/types'

type SortKey = 'title' | 'status' | 'source' | 'staged_at'
type SortDir = 'asc' | 'desc'

const PAGE_SIZE = 50

interface FragmentTableProps {
  items: InboxItem[]
  /** Scroll container used as the IntersectionObserver root for infinite scroll. */
  scrollRootRef?: RefObject<HTMLElement | null>
  /** Opens the fragment detail modal for a row. */
  onOpenFragment?: (fragmentId: string) => void
}

function sortItems(items: InboxItem[], key: SortKey, dir: SortDir): InboxItem[] {
  return [...items].sort((a, b) => {
    const av = (a[key] ?? '').toLowerCase()
    const bv = (b[key] ?? '').toLowerCase()
    const cmp = av < bv ? -1 : av > bv ? 1 : 0
    return dir === 'asc' ? cmp : -cmp
  })
}

const COLUMNS: { key: SortKey; label: string }[] = [
  { key: 'title', label: 'Fragment' },
  { key: 'status', label: 'Status' },
  { key: 'source', label: 'Source' },
  { key: 'staged_at', label: 'Staged' },
]

/** Inbox fragment table — mirrors Torque's TaskTable (sortable, windowed). */
export default function FragmentTable({
  items,
  scrollRootRef,
  onOpenFragment,
}: FragmentTableProps) {
  const [sortKey, setSortKey] = useState<SortKey>('staged_at')
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [visibleCount, setVisibleCount] = useState(PAGE_SIZE)
  const sentinelRef = useRef<HTMLTableRowElement | null>(null)

  function handleSortClick(key: SortKey) {
    if (sortKey === key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(key)
      setSortDir(key === 'staged_at' ? 'desc' : 'asc')
    }
  }

  function handleSelect(id: string, isSelected: boolean) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (isSelected) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const sorted = useMemo(() => sortItems(items, sortKey, sortDir), [items, sortKey, sortDir])

  // Reset the window to the first page whenever the list or sort changes.
  useEffect(() => {
    setVisibleCount(PAGE_SIZE)
  }, [items, sortKey, sortDir])

  const visible = useMemo(() => sorted.slice(0, visibleCount), [sorted, visibleCount])
  const hasMore = visibleCount < sorted.length

  useEffect(() => {
    if (!hasMore) return
    const el = sentinelRef.current
    if (!el) return
    const root = scrollRootRef?.current ?? null
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((e) => e.isIntersecting)) {
          setVisibleCount((c) => Math.min(c + PAGE_SIZE, sorted.length))
        }
      },
      { root, rootMargin: '200px 0px' },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [hasMore, sorted.length, scrollRootRef])

  function handleSelectAll(e: React.ChangeEvent<HTMLInputElement>) {
    setSelected(e.target.checked ? new Set(visible.map((t) => t.fragment_id)) : new Set())
  }

  const allSelected = visible.length > 0 && visible.every((t) => selected.has(t.fragment_id))

  return (
    <div className="w-full overflow-x-auto">
      <table className="w-full min-w-full">
        <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
          <tr className="border-b border-border-strong">
            <th className="w-8 py-1.5 pl-[14px] pr-0">
              <input
                type="checkbox"
                checked={allSelected}
                onChange={handleSelectAll}
                className="h-3 w-3 cursor-pointer appearance-none rounded-sm border border-border-strong bg-panel-2 checked:border-text-soft checked:bg-text-soft"
                aria-label="Select all fragments"
              />
            </th>
            {COLUMNS.flatMap(({ key, label }) => {
              const isSorted = sortKey === key
              const isTitle = key === 'title'
              const header = (
                <th
                  key={key}
                  className={`py-1.5 font-medium ${isTitle ? 'px-3 text-left' : 'w-px whitespace-nowrap px-1.5'}`}
                >
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 transition-colors hover:text-text-muted"
                    onClick={() => handleSortClick(key)}
                  >
                    {label}
                    <span className={isSorted ? 'text-text-muted' : 'text-text-subtle/50'}>
                      {isSorted ? (sortDir === 'asc' ? '↑' : '↓') : '⇕'}
                    </span>
                  </button>
                </th>
              )
              // Inject the (non-sortable) Route column after Source.
              if (key === 'source') {
                return [
                  header,
                  <th
                    key="route"
                    className="w-px whitespace-nowrap px-1.5 py-1.5 font-medium text-text-subtle"
                  >
                    Route
                  </th>,
                ]
              }
              return [header]
            })}
          </tr>
        </thead>
        <tbody className="divide-y divide-border-soft text-[13px] leading-4">
          {visible.map((item) => (
            <FragmentRow
              key={item.fragment_id}
              item={item}
              selected={selected.has(item.fragment_id)}
              onSelect={handleSelect}
              onOpen={onOpenFragment}
            />
          ))}
          {hasMore && (
            <tr ref={sentinelRef} aria-hidden="true">
              <td colSpan={COLUMNS.length + 2} className="py-3 text-center text-[11px] text-text-subtle/80">
                Loading more… ({visible.length} of {sorted.length})
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  )
}
