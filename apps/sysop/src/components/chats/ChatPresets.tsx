import { useState } from 'react'
import { Button } from '@hollis-labs/sysop-ui'
import type { ChatFilters } from '@/lib/chats'
import { cleanPresetName, type ChatPreset } from '@/lib/chats-presets'

interface ChatPresetsProps {
  presets: ChatPreset[]
  onApply: (filters: ChatFilters) => void
  onSave: (name: string) => void
  onDelete: (name: string) => void
  selectClass: string
}

/** Named filter presets: pick one to apply it, save the current view, or delete. */
export function ChatPresets({ presets, onApply, onSave, onDelete, selectClass }: ChatPresetsProps) {
  const [selected, setSelected] = useState('')
  const [naming, setNaming] = useState(false)
  const [name, setName] = useState('')

  function commit() {
    const clean = cleanPresetName(name)
    if (!clean) return
    onSave(clean)
    setSelected(clean)
    setName('')
    setNaming(false)
  }

  return (
    <span className="flex items-center gap-1">
      <select
        aria-label="Presets"
        className={selectClass}
        value={selected}
        onChange={(e) => {
          setSelected(e.target.value)
          const preset = presets.find((p) => p.name === e.target.value)
          if (preset) onApply({ ...preset.filters })
        }}
      >
        <option value="">{presets.length ? 'Choose…' : 'None saved'}</option>
        {presets.map((p) => <option key={p.name} value={p.name}>{p.name}</option>)}
      </select>
      {naming ? (
        <>
          <input
            aria-label="Preset name"
            autoFocus
            className={`${selectClass} w-28 normal-case`}
            placeholder="Preset name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commit()
              if (e.key === 'Escape') setNaming(false)
            }}
          />
          <Button variant="outline" size="sm" onClick={commit} disabled={!cleanPresetName(name)}>Save</Button>
        </>
      ) : (
        <Button variant="outline" size="sm" onClick={() => { setName(selected); setNaming(true) }} title="Save the current search, mode, filters and sort">
          Save view
        </Button>
      )}
      {selected && !naming && (
        <Button
          variant="outline"
          size="sm"
          aria-label={`Delete preset ${selected}`}
          onClick={() => { onDelete(selected); setSelected('') }}
        >
          Delete
        </Button>
      )}
    </span>
  )
}
