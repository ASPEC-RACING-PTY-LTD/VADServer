import { useId, useState } from 'react'
import { Eye, EyeOff, KeyRound } from 'lucide-react'
import { Badge, Button, IconButton } from './ui'
import { formatRelative } from '../lib/format'

/**
 * api-v1 §1 secret semantics as a value:
 * `undefined` keeps the stored secret (omit the field), `null` clears it, a string sets it.
 */
export type SecretValue = string | null | undefined

export type SecretFieldProps = {
  label: string
  /** From the GET body: `has_secret`. */
  hasSecret: boolean
  /** From the GET body: `secret_updated_at` (unix ms, or an ISO string). */
  updatedAt?: number | string | null
  value: SecretValue
  onChange: (next: SecretValue) => void
  placeholder?: string
  hint?: string
  /** Offer "Clear" when a secret is stored (default true). */
  allowClear?: boolean
  disabled?: boolean
}

/** Adds `{[key]: value}` to a request body unless the value means "keep". An empty string also means keep. */
export function withSecret<B extends object>(body: B, value: SecretValue, key = 'secret'): B & Record<string, string | null> {
  if (value === undefined || value === '') return body as B & Record<string, string | null>
  return { ...body, [key]: value }
}

function toMs(v: number | string | null | undefined): number | null {
  if (v === null || v === undefined || v === '') return null
  if (typeof v === 'number') return v
  const n = Date.parse(v)
  return Number.isNaN(n) ? null : n
}

/** Write-only secret editor. Never shows (or receives) the stored value. */
export function SecretField({ label, hasSecret, updatedAt, value, onChange, placeholder, hint, allowClear = true, disabled }: SecretFieldProps) {
  const id = useId()
  const [reveal, setReveal] = useState(false)
  const at = toMs(updatedAt)
  const editing = typeof value === 'string'

  return (
    <div role="group" aria-labelledby={`${id}-label`} aria-describedby={hint ? `${id}-hint` : undefined}>
      <div className="mb-1.5 flex flex-wrap items-center gap-2">
        <span id={`${id}-label`} className="label mb-0">
          {label}
        </span>
        {value === null ? (
          <Badge tone="amber">Will be cleared</Badge>
        ) : editing ? (
          <Badge tone="blue">{hasSecret ? 'Will be replaced' : 'Will be set'}</Badge>
        ) : hasSecret ? (
          <Badge tone="green">
            <KeyRound aria-hidden="true" className="size-3" />
            Set{at ? ` · updated ${formatRelative(at)}` : ''}
          </Badge>
        ) : (
          <Badge>Not set</Badge>
        )}
      </div>

      {editing ? (
        <div className="flex items-center gap-1.5">
          <div className="relative flex-1">
            <input
              id={`${id}-input`}
              className="input pr-10"
              type={reveal ? 'text' : 'password'}
              autoComplete="new-password"
              spellCheck={false}
              autoFocus
              aria-label={`New ${label}`}
              placeholder={placeholder ?? (hasSecret ? 'Enter a new value' : 'Enter a value')}
              value={value}
              disabled={disabled}
              onChange={(e) => onChange(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Escape') {
                  e.preventDefault()
                  onChange(undefined)
                }
              }}
            />
            <IconButton label={reveal ? 'Hide value' : 'Show value'} className="absolute top-1/2 right-1 -translate-y-1/2" onClick={() => setReveal((r) => !r)}>
              {reveal ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
            </IconButton>
          </div>
          <Button size="sm" variant="ghost" disabled={disabled} onClick={() => onChange(undefined)}>
            Cancel
          </Button>
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          {value === null ? (
            <Button size="sm" disabled={disabled} onClick={() => onChange(undefined)}>
              Undo clear
            </Button>
          ) : (
            <>
              <Button size="sm" disabled={disabled} onClick={() => onChange('')}>
                {hasSecret ? 'Replace' : 'Set'}
              </Button>
              {hasSecret && allowClear && (
                <Button size="sm" variant="ghost" className="text-rose-600 dark:text-rose-400" disabled={disabled} onClick={() => onChange(null)}>
                  Clear
                </Button>
              )}
            </>
          )}
        </div>
      )}
      {hint && (
        <p id={`${id}-hint`} className="mt-1 text-xs text-slate-500 dark:text-slate-400">
          {hint}
        </p>
      )}
    </div>
  )
}
