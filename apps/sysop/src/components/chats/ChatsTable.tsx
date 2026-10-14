import { useEffect, useRef } from 'react'
import { formatRelativeTime, formatShortDate } from '@hollis-labs/sysop-ui'
import { formatTokens, highlightParts, matchFieldLabel, projectName, type ChatSession, type ChatSortKey } from '@/lib/chats'

interface Column {
  label: string
  sort?: ChatSortKey
  align?: 'right'
}

const COLUMNS: Column[] = [
  { label: 'Title' },
  { label: 'Tool' },
  { label: 'Model' },
  { label: 'Project' },
  { label: 'Started', sort: 'started' },
  { label: 'Turns', sort: 'turns', align: 'right' },
  { label: 'Tokens', sort: 'tokens', align: 'right' },
  { label: 'Calls', sort: 'tool_calls', align: 'right' },
  { label: 'Errors', sort: 'errors', align: 'right' },
  { label: 'Tags' },
]

interface ChatsTableProps {
  items: ChatSession[]
  sort: ChatSortKey
  desc: boolean
  onSort: (key: ChatSortKey) => void
  onOpen: (id: string) => void
  /** Row highlighted by keyboard navigation. */
  selectedId?: string | null
}

/** Which field matched, a highlighted excerpt, and the score for semantic hits. */
function MatchLine({ item }: { item: ChatSession }) {
  const excerpt = item.match?.excerpt || item.snippet
  if (!item.match && !excerpt) return null
  return (
    <div className="mt-0.5 flex items-center gap-1.5 text-[11px] text-text-subtle">
      {item.match && (
        <span
          className="shrink-0 rounded border border-border bg-panel-2/50 px-1 text-[10px] uppercase tracking-wider text-text-soft"
          title={item.match.sources?.length ? `Found by: ${item.match.sources.join(', ')}` : undefined}
        >
          {matchFieldLabel(item.match.field)}
        </span>
      )}
      {item.score !== undefined && item.match?.field === 'semantic' && (
        <span className="shrink-0 tabular-nums" title="Cosine similarity">
          {item.score.toFixed(2)}
        </span>
      )}
      {excerpt && (
        <span className="truncate" data-testid="match-excerpt">
          {highlightParts(excerpt).map((part, i) =>
            part.hit ? (
              <mark key={i} className="rounded-sm bg-status-inbox/20 px-0.5 text-text">
                {part.text}
              </mark>
            ) : (
              <span key={i}>{part.text}</span>
            ),
          )}
        </span>
      )}
    </div>
  )
}

/** Server-sorted chat session table, styled like the inbox fragment table. */
export default function ChatsTable({ items, sort, desc, onSort, onOpen, selectedId }: ChatsTableProps) {
  const selectedRef = useRef<HTMLTableRowElement | null>(null)
  useEffect(() => {
    selectedRef.current?.scrollIntoView?.({ block: 'nearest' })
  }, [selectedId])
  return (
    <div className="w-full overflow-x-auto">
      <table className="w-full min-w-full">
        <thead className="text-[10px] uppercase tracking-[.28em] text-text-subtle">
          <tr className="border-b border-border-strong">
            {COLUMNS.map((col) => (
              <th
                key={col.label}
                className={`whitespace-nowrap px-2 py-1.5 font-medium ${col.align === 'right' ? 'text-right' : 'text-left'}`}
                aria-sort={col.sort && col.sort === sort ? (desc ? 'descending' : 'ascending') : undefined}
              >
                {col.sort ? (
                  <button
                    type="button"
                    className="inline-flex items-center gap-1 transition-colors hover:text-text-muted"
                    onClick={() => onSort(col.sort!)}
                  >
                    {col.label}
                    <span className={col.sort === sort ? 'text-text-muted' : 'text-text-subtle/50'}>
                      {col.sort === sort ? (desc ? '↓' : '↑') : '⇕'}
                    </span>
                  </button>
                ) : (
                  col.label
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-border-soft text-[13px] leading-4">
          {items.map((item) => (
            <tr
              key={item.id}
              ref={item.id === selectedId ? selectedRef : undefined}
              data-selected={item.id === selectedId ? 'true' : undefined}
              tabIndex={0}
              role="link"
              aria-label={`Open ${item.title || 'untitled chat'}`}
              onClick={() => onOpen(item.id)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') onOpen(item.id)
              }}
              className={`cursor-pointer transition-colors hover:bg-panel-hover/50 focus:bg-panel-hover/50 focus:outline-none ${item.id === selectedId ? 'bg-panel-hover/60' : ''}`}
            >
              <td className="max-w-[28rem] px-2 py-2">
                <div className="truncate text-text">{item.title || '(untitled chat)'}</div>
                <MatchLine item={item} />
              </td>
              <td className="whitespace-nowrap px-2 py-2 text-text-soft">
                {item.tool}
                {item.kind === 'subagent' && (
                  <span className="ml-1 rounded border border-border px-1 text-[10px] text-text-subtle">sub</span>
                )}
              </td>
              <td className="whitespace-nowrap px-2 py-2 text-text-subtle">{item.model || '—'}</td>
              <td className="max-w-[10rem] truncate px-2 py-2 text-text-subtle" title={item.project}>
                {projectName(item.project) || '—'}
              </td>
              <td className="whitespace-nowrap px-2 py-2 text-text-soft" title={item.started_at}>
                {item.started_at ? (
                  <>
                    {formatShortDate(item.started_at)}{' '}
                    <span className="text-text-subtle">· {formatRelativeTime(item.started_at)}</span>
                  </>
                ) : (
                  '—'
                )}
              </td>
              <td className="px-2 py-2 text-right tabular-nums text-text-soft">{item.turns}</td>
              <td className="px-2 py-2 text-right tabular-nums text-text-soft">{formatTokens(item.tokens)}</td>
              <td className="px-2 py-2 text-right tabular-nums text-text-soft">{item.tool_calls}</td>
              <td
                className={`px-2 py-2 text-right tabular-nums ${item.errors > 0 ? 'text-danger-soft' : 'text-text-subtle'}`}
              >
                {item.errors}
              </td>
              <td className="px-2 py-2">
                <div className="flex flex-wrap gap-1">
                  {(item.tags ?? []).slice(0, 4).map((tag) => (
                    <span key={tag} className="rounded border border-border bg-panel-2/50 px-1.5 py-0.5 text-[10px] text-text-soft">
                      {tag}
                    </span>
                  ))}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
