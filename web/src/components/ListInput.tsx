import { useId, useState } from 'react'
import { Plus, X } from 'lucide-react'
import { Button, IconButton, cx } from './ui'

export type ListInputProps = {
  value: string[]
  onChange: (next: string[]) => void
  /** Accessible name for the list and the add box, for example "Include paths". */
  label: string
  placeholder?: string
  /** Monospace rendering for paths and patterns (default true). */
  mono?: boolean
  /** Returns an error message to reject an entry. */
  validate?: (item: string) => string | null | undefined
  allowDuplicates?: boolean
  /** Pasting several lines adds one entry per line (default true). Commas are never split, since paths may contain them. */
  splitLines?: boolean
  max?: number
  disabled?: boolean
  addLabel?: string
  emptyText?: string
}

/** Edits an ordered list of strings (paths, exclude patterns). Entries are editable in place. */
export function ListInput({ value, onChange, label, placeholder, mono = true, validate, allowDuplicates, splitLines = true, max, disabled, addLabel = 'Add', emptyText = 'None' }: ListInputProps) {
  const id = useId()
  const [text, setText] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const full = max !== undefined && value.length >= max

  const add = (raw: string = text) => {
    const parts = (splitLines ? raw.split(/\r?\n/) : [raw]).map((s) => s.trim()).filter(Boolean)
    if (!parts.length) return
    const next = [...value]
    for (const p of parts) {
      const e = validate?.(p)
      if (e) {
        setText(raw)
        return setErr(`${p}: ${e}`)
      }
      if (!allowDuplicates && next.includes(p)) continue
      if (max !== undefined && next.length >= max) {
        setText(raw)
        return setErr(`At most ${max} entries`)
      }
      next.push(p)
    }
    setErr(null)
    setText('')
    onChange(next)
  }

  const edit = (i: number, v: string) => onChange(value.map((x, j) => (j === i ? v : x)))
  const commitEdit = (i: number) => {
    const v = value[i].trim()
    if (!v) onChange(value.filter((_, j) => j !== i))
    else if (v !== value[i]) edit(i, v)
  }

  return (
    <div className="flex flex-col gap-2">
      {value.length === 0 ? (
        <p className="text-xs text-slate-500 dark:text-slate-400">{emptyText}</p>
      ) : (
        <ul aria-label={label} className="flex flex-col gap-1.5">
          {value.map((item, i) => (
            <li key={i} className="flex items-center gap-1.5">
              <input
                className={cx('input py-1.5', mono && 'font-mono text-xs')}
                value={item}
                disabled={disabled}
                aria-label={`${label} entry ${i + 1}`}
                onChange={(e) => edit(i, e.target.value)}
                onBlur={() => commitEdit(i)}
              />
              <IconButton label={`Remove ${item}`} disabled={disabled} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <X className="size-4" />
              </IconButton>
            </li>
          ))}
        </ul>
      )}
      <div className="flex items-center gap-1.5">
        <input
          id={`${id}-add`}
          className={cx('input py-1.5', mono && 'font-mono text-xs')}
          value={text}
          placeholder={placeholder}
          disabled={disabled || full}
          aria-label={`Add to ${label}`}
          aria-invalid={err ? true : undefined}
          aria-describedby={err ? `${id}-err` : undefined}
          onChange={(e) => {
            setText(e.target.value)
            setErr(null)
          }}
          onPaste={(e) => {
            const t = e.clipboardData.getData('text')
            if (splitLines && /\r?\n/.test(t)) {
              e.preventDefault()
              add((text ? text + '\n' : '') + t)
            }
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              add()
            }
          }}
        />
        <Button size="sm" icon={<Plus className="size-4" />} disabled={disabled || full || !text.trim()} onClick={() => add()}>
          {addLabel}
        </Button>
      </div>
      {err && (
        <p id={`${id}-err`} role="alert" className="text-xs text-rose-600">
          {err}
        </p>
      )}
    </div>
  )
}
