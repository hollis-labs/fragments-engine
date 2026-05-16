import { useEffect, useRef, useState } from 'react'
import { Search } from 'lucide-react'

interface FilterSearchInputProps {
  value: string
  onChange: (next: string) => void
  placeholder?: string
}

const DEBOUNCE_MS = 250

function isEditableTarget(el: Element | null): boolean {
  if (!el) return false
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) return true
  if (el instanceof HTMLElement && el.isContentEditable) return true
  return false
}

/** Debounced search field with `/`-to-focus — mirrors Torque's FilterSearchInput. */
export function FilterSearchInput({
  value,
  onChange,
  placeholder = 'Search fragments by title, reason, or id…',
}: FilterSearchInputProps) {
  const [local, setLocal] = useState(value)
  const inputRef = useRef<HTMLInputElement | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const lastEmittedRef = useRef(value)

  useEffect(() => {
    setLocal(value)
    lastEmittedRef.current = value
  }, [value])

  useEffect(() => {
    if (local === lastEmittedRef.current) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      lastEmittedRef.current = local
      onChange(local)
    }, DEBOUNCE_MS)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [local, onChange])

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if (e.key !== '/') return
      if (e.ctrlKey || e.metaKey || e.altKey) return
      if (e.defaultPrevented) return
      if (isEditableTarget(document.activeElement)) return
      e.preventDefault()
      inputRef.current?.focus()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  return (
    <div className="flex min-w-[240px] flex-1 items-center gap-2 rounded border border-border bg-panel-2/50 px-2.5 py-1 focus-within:border-border-strong">
      <Search className="h-3.5 w-3.5 text-text-subtle" />
      <input
        ref={inputRef}
        type="search"
        aria-label="Search fragments"
        title="Search fragments by title, reason, or id (press / to focus, Esc to clear)"
        placeholder={placeholder}
        value={local}
        onChange={(e) => setLocal(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            setLocal('')
            inputRef.current?.blur()
          }
        }}
        className="flex-1 bg-transparent text-xs text-text placeholder:text-text-subtle/70 focus:outline-none"
      />
      <kbd className="rounded border border-border bg-bg px-1 text-[9px] uppercase tracking-wider text-text-subtle">
        /
      </kbd>
    </div>
  )
}
