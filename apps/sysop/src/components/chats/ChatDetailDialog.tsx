import { useCallback, useEffect, useRef, useState } from 'react'
import { Brain, ChevronDown, ChevronRight } from 'lucide-react'
import { Button, Dialog, DialogContent, DialogTitle, Skeleton, formatShortDate } from '@hollis-labs/sysop-ui'
import { useApi } from '@/hooks/useApi'
import {
  flattenTree,
  formatBytes,
  formatTokens,
  projectName,
  type ChatDecision,
  type ChatDetail,
  type ChatToolCall,
  type ChatTurn,
} from '@/lib/chats'

const TURN_PAGE = 200

function ToolCalls({ calls, noun = 'tool call' }: { calls: ChatToolCall[]; noun?: string }) {
  const [open, setOpen] = useState(false)
  const errors = calls.filter((c) => c.status === 'error').length
  return (
    <div className="mt-1.5 text-[11px]">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="inline-flex items-center gap-1 text-text-subtle hover:text-text-soft"
      >
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
        {calls.length} {noun}{calls.length === 1 ? '' : 's'}
        {errors > 0 && <span className="text-danger-soft">· {errors} failed</span>}
      </button>
      {open && (
        <ul className="mt-1 space-y-0.5 pl-4 font-mono text-text-subtle">
          {calls.map((c, i) => (
            <li key={i}>
              <span className="text-text-soft">{c.name}</span> ·{' '}
              <span className={c.status === 'error' ? 'text-danger-soft' : ''}>{c.status}</span> ·{' '}
              {formatBytes(c.result_bytes)}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function TurnView({ turn }: { turn: ChatTurn }) {
  return (
    <div id={`chat-turn-${turn.seq}`} className="scroll-mt-2 border-b border-border-soft px-4 py-3">
      <div className="mb-1 flex items-center gap-2 text-[10px] uppercase tracking-[.16em] text-text-subtle">
        <span className={turn.role === 'user' ? 'text-text-soft' : ''}>{turn.role}</span>
        <span>#{turn.seq}</span>
        {turn.ts && <span>{formatShortDate(turn.ts)}</span>}
        {turn.is_sidechain && <span>sidechain</span>}
        {turn.has_thinking && (
          <span className="inline-flex items-center gap-1" title="Thinking text is never stored">
            <Brain className="h-3 w-3" /> thinking · {formatBytes(turn.thinking_bytes ?? 0)}
          </span>
        )}
      </div>
      {turn.text && <p className="whitespace-pre-wrap break-words text-[13px] leading-5 text-text">{turn.text}</p>}
      {turn.text_truncated && <p className="mt-1 text-[11px] text-text-subtle">(text shortened)</p>}
      {turn.tool_calls && turn.tool_calls.length > 0 && <ToolCalls calls={turn.tool_calls} />}
    </div>
  )
}

function DecisionList({
  title,
  items,
  onCite,
}: {
  title: string
  items?: ChatDecision[]
  onCite: (seq: number) => void
}) {
  if (!items || items.length === 0) return null
  return (
    <div className="mt-2">
      <p className="text-[10px] uppercase tracking-[.16em] text-text-subtle">{title}</p>
      <ul className="mt-1 list-disc space-y-1 pl-4 text-[13px] text-text-soft">
        {items.map((d, i) => (
          <li key={i}>
            {d.text}{' '}
            {(d.turns ?? []).map((seq) => (
              <button
                key={seq}
                type="button"
                onClick={() => onCite(seq)}
                className="mr-1 rounded border border-border px-1 font-mono text-[10px] text-text-subtle hover:text-text"
              >
                #{seq}
              </button>
            ))}
          </li>
        ))}
      </ul>
    </div>
  )
}

interface ChatDetailDialogProps {
  sessionId: string | null
  onClose: () => void
  onOpenSession?: (id: string) => void
}

export function ChatDetailDialog({ sessionId, onClose, onOpenSession }: ChatDetailDialogProps) {
  const api = useApi()
  const [detail, setDetail] = useState<ChatDetail | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  const pendingCite = useRef<number | null>(null)

  useEffect(() => {
    if (!sessionId) {
      setDetail(null)
      return
    }
    const controller = new AbortController()
    setLoading(true)
    setError(null)
    setDetail(null)
    api
      .fetchTranscriptSession(sessionId, { turn_limit: TURN_PAGE }, { signal: controller.signal })
      .then(setDetail)
      .catch((err) => {
        if (!controller.signal.aborted) setError(err instanceof Error ? err.message : 'Failed to load chat')
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [api, sessionId])

  const loadMore = useCallback(async () => {
    if (!sessionId || !detail || detail.next_turn === undefined) return
    setLoadingMore(true)
    try {
      const next = await api.fetchTranscriptSession(sessionId, {
        turn_offset: detail.next_turn,
        turn_limit: TURN_PAGE,
      })
      setDetail((prev) =>
        prev ? { ...prev, turns: [...prev.turns, ...next.turns], next_turn: next.next_turn } : prev,
      )
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load more turns')
    } finally {
      setLoadingMore(false)
    }
  }, [api, detail, sessionId])

  // A citation to a turn that is not loaded yet pulls pages until it appears.
  const cite = useCallback(
    (seq: number) => {
      const el = document.getElementById(`chat-turn-${seq}`)
      if (el) {
        el.scrollIntoView({ block: 'center' })
        return
      }
      pendingCite.current = seq
      void loadMore()
    },
    [loadMore],
  )

  useEffect(() => {
    const seq = pendingCite.current
    if (seq === null || !detail) return
    const el = document.getElementById(`chat-turn-${seq}`)
    if (el) {
      pendingCite.current = null
      el.scrollIntoView({ block: 'center' })
    } else if (detail.next_turn !== undefined && !loadingMore) {
      void loadMore()
    } else if (detail.next_turn === undefined) {
      pendingCite.current = null
    }
  }, [detail, loadMore, loadingMore])

  const session = detail?.session
  const tree = detail ? flattenTree(detail.subagents) : []
  const enrichment = detail?.enrichment

  return (
    <Dialog open={sessionId !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="overflow-hidden p-0 sm:max-w-3xl">
        <div className="max-h-[85vh] overflow-y-auto">
          <div className="px-4 pt-4">
            <p className="text-[10px] font-semibold uppercase tracking-[.28em] text-text-subtle">Chat</p>
            <DialogTitle className="mt-1 text-[15px] font-medium text-text">
              {session?.title || (loading ? 'Loading…' : '(untitled chat)')}
            </DialogTitle>
          </div>
          {loading && (
            <div className="flex flex-col gap-3 p-4">
              <Skeleton className="h-3 w-1/2" />
              <Skeleton className="h-32 w-full" />
            </div>
          )}
          {error && <p className="p-4 text-sm text-danger-soft">{error}</p>}
          {detail && session && (
            <>
              <div className="flex flex-wrap gap-x-4 gap-y-1 px-4 py-2 text-[11px] text-text-subtle">
                <span>{session.tool}{session.kind === 'subagent' ? ' · subagent' : ''}</span>
                {session.model && <span>{session.model}</span>}
                {session.project && <span title={session.project}>{projectName(session.project)}</span>}
                {session.git_branch && <span>{session.git_branch}</span>}
                {session.started_at && <span>{formatShortDate(session.started_at)}</span>}
                <span>{detail.turn_total} turn{detail.turn_total === 1 ? '' : 's'}</span>
              </div>
              <div className="mx-4 grid grid-cols-2 gap-2 rounded border border-border bg-panel-2/40 p-3 text-[12px] sm:grid-cols-5" aria-label="Usage totals">
                {[
                  ['Requests', String(detail.usage.requests)],
                  ['Input', formatTokens(detail.usage.input_tokens)],
                  ['Output', formatTokens(detail.usage.output_tokens)],
                  ['Cache read', formatTokens(detail.usage.cache_read_tokens)],
                  ['Cache write', formatTokens(detail.usage.cache_creation_tokens)],
                ].map(([label, value]) => (
                  <div key={label}>
                    <div className="text-[10px] uppercase tracking-[.16em] text-text-subtle">{label}</div>
                    <div className="tabular-nums text-text">{value}</div>
                  </div>
                ))}
              </div>
              {enrichment && (
                <div className="mx-4 mt-3 rounded border border-border p-3" aria-label="Enrichment">
                  {enrichment.summary && <p className="text-[13px] text-text">{enrichment.summary}</p>}
                  {(enrichment.outcome || enrichment.session_type) && (
                    <p className="mt-1 text-[11px] text-text-subtle">
                      {[enrichment.session_type, enrichment.outcome].filter(Boolean).join(' · ')}
                    </p>
                  )}
                  <DecisionList title="Decisions" items={enrichment.decisions} onCite={cite} />
                  <DecisionList title="Follow-ups" items={enrichment.follow_ups} onCite={cite} />
                </div>
              )}
              {tree.length > 0 && (
                <div className="mx-4 mt-3" aria-label="Subagents">
                  <p className="text-[10px] uppercase tracking-[.16em] text-text-subtle">Subagents ({tree.length})</p>
                  <ul className="mt-1 space-y-0.5 text-[12px]">
                    {tree.map(({ node, depth }) => (
                      <li key={node.id} style={{ paddingLeft: depth * 16 }}>
                        <button
                          type="button"
                          onClick={() => onOpenSession?.(node.id)}
                          className="text-left text-text-soft hover:text-text"
                        >
                          {node.agent_type || 'subagent'} · {node.title || node.id.slice(0, 8)}
                        </button>
                        <span className="ml-2 text-text-subtle">
                          {node.turns} turn{node.turns === 1 ? '' : 's'} · {formatTokens(node.tokens)} tokens
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              <div className="mt-3 border-t border-border-soft">
                {detail.turns.map((turn) => (
                  <TurnView key={turn.event_id} turn={turn} />
                ))}
                {detail.next_turn === undefined && detail.unattached_calls && detail.unattached_calls.length > 0 && (
                  <div className="border-t border-border-soft px-3 py-2">
                    <ToolCalls calls={detail.unattached_calls} noun="unattached tool result" />
                  </div>
                )}
                {detail.next_turn !== undefined && (
                  <div className="p-3 text-center">
                    <Button variant="outline" size="sm" onClick={() => void loadMore()} disabled={loadingMore}>
                      {loadingMore ? 'Loading…' : `Load more (${detail.turns.length} of ${detail.turn_total})`}
                    </Button>
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
