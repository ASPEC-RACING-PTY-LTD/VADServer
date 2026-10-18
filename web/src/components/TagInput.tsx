import { useId, useRef, useState } from 'react'
import { X } from 'lucide-react'
import { cx } from './ui'

export type TagInputProps = {
  value: string[]
  onChange: (next: string[]) => void
  /** Accessible name, for example "Tags". */
  label: string
  placeholder?: string
  /** Offered through a native datalist. */
  suggestions?: string[]
  /** Applied to every new tag (default: trim). Return '' to drop it. */
  normalize?: (tag: string) => string
  validate?: (tag: string) => string | null | undefined
  max?: number
  maxLength?: number
  disabled?: boolean
  mono?: boolean
}

const defaultNormalize = (t: string) => t.trim()

/** Chip-style tag editor. Enter, comma or Tab (with text) adds; Backspace on an empty box removes the last tag. */
export function TagInput({ value, onChange, label, placeholder = 'Add tag…', suggestions, normalize = defaultNormalize, validate, max, maxLength = 64, disabled, mono }: TagInputProps) {
  const id = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [text, setText] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const full = max !== undefined && value.length >= max

  const add = (raw: string) => {
    const next = [...value]
    for (const part of raw.split(',')) {
      const t = normalize(part)
      if (!t) continue
      if (t.length > maxLength) return setErr(`Tags are at most ${maxLength} characters`)
      const e = validate?.(t)
      if (e) return setErr(e)
      if (next.includes(t)) continue
      if (max !== undefined && next.length >= max) return setErr(`At most ${max} tags`)
      next.push(t)
    }
    setErr(null)
    setText('')
    if (next.length !== value.length) onChange(next)
  }

  const remove = (t: string) => {
    onChange(value.filter((x) => x !== t))
    inputRef.current?.focus()
  }

  return (
    <div>
      <div
        className={cx('input flex min-h-9 flex-wrap items-center gap-1.5 py-1.5', disabled && 'opacity-60')}
        onClick={() => inputRef.current?.focus()}
      >
        <ul aria-label={label} className="contents">
          {value.map((t) => (
            <li key={t} className={cx('inline-flex items-center gap-1 rounded-full bg-brand-100 px-2 py-0.5 text-xs font-medium text-brand-800 dark:bg-brand-500/15 dark:text-brand-300', mono && 'font-mono')}>
              {t}
              {!disabled && (
                <button type="button" aria-label={`Remove ${t}`} onClick={(e) => (e.stopPropagation(), remove(t))} className="rounded-full hover:text-brand-950 dark:hover:text-white">
                  <X aria-hidden="true" className="size-3" />
                </button>
              )}
            </li>
          ))}
        </ul>
        <input
          ref={inputRef}
          className="min-w-24 flex-1 bg-transparent text-sm outline-none placeholder:text-slate-400"
          value={text}
          placeholder={full ? undefined : placeholder}
          disabled={disabled || full}
          list={suggestions?.length ? `${id}-list` : undefined}
          aria-label={`Add to ${label}`}
          aria-invalid={err ? true : undefined}
          aria-describedby={err ? `${id}-err` : undefined}
          onChange={(e) => {
            setErr(null)
            if (e.target.value.includes(',')) add(e.target.value)
            else setText(e.target.value)
          }}
          onBlur={() => text && add(text)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || (e.key === 'Tab' && text.trim())) {
              if (text.trim() || e.key === 'Enter') e.preventDefault()
              add(text)
            } else if (e.key === 'Backspace' && !text && value.length) {
              onChange(value.slice(0, -1))
            }
          }}
        />
        {suggestions?.length ? (
          <datalist id={`${id}-list`}>
            {suggestions.filter((s) => !value.includes(s)).map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
        ) : null}
      </div>
      {err && (
        <p id={`${id}-err`} role="alert" className="mt-1 text-xs text-rose-600">
          {err}
        </p>
      )}
    </div>
  )
}
