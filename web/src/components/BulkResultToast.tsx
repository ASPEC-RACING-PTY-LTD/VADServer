import { useCallback, useState } from 'react'
import { AlertTriangle, CheckCircle2, X, XCircle } from 'lucide-react'
import { useUI, cx } from './ui'
import type { BulkResult } from '../lib/api'
import { pluralize } from '../lib/format'

export type BulkSummary = { tone: 'success' | 'warning' | 'error'; title: string; message?: string }

/**
 * Summarises an api-v1 bulk result. `noun` is singular ("device"); `verb` is past tense ("revoked").
 * Example: "3 devices revoked, 1 failed".
 */
export function summarizeBulk(r: BulkResult, verb = 'updated', noun = 'item'): BulkSummary {
  const failed = r.failed.length
  const ok = `${pluralize(r.ok_count, noun)} ${verb}`
  if (failed === 0) return { tone: 'success', title: ok }
  const first = r.failed.slice(0, 3).map((f) => `${f.id}: ${f.message || f.code}`).join('; ')
  const more = failed > 3 ? ` (+${failed - 3} more)` : ''
  if (r.ok_count === 0) return { tone: 'error', title: `${pluralize(failed, noun)} failed`, message: first + more }
  return { tone: 'warning', title: `${ok}, ${failed} failed`, message: first + more }
}

/** Returns `show(result, verb?, noun?)` that raises a toast through UIProvider. */
export function useBulkResultToast() {
  const { toast } = useUI()
  return useCallback(
    (r: BulkResult, verb?: string, noun?: string) => {
      const s = summarizeBulk(r, verb, noun)
      toast(s.tone, s.title, s.message)
      return s
    },
    [toast],
  )
}

export type BulkResultToastProps = {
  result: BulkResult | null | undefined
  verb?: string
  noun?: string
  /** Optional label per failed id (for example a device name instead of its id). */
  describe?: (id: string | number) => string
  onDismiss?: () => void
  className?: string
}

/** Inline, dismissible panel listing every failure of a bulk action. Renders nothing for a null result. */
export function BulkResultToast({ result, verb, noun, describe, onDismiss, className }: BulkResultToastProps) {
  const [dismissed, setDismissed] = useState<BulkResult | null>(null)
  if (!result || dismissed === result) return null
  const s = summarizeBulk(result, verb, noun)
  const Icon = s.tone === 'success' ? CheckCircle2 : s.tone === 'warning' ? AlertTriangle : XCircle
  const tones = {
    success: 'border-emerald-200 bg-emerald-50 dark:border-emerald-500/30 dark:bg-emerald-500/10',
    warning: 'border-amber-200 bg-amber-50 dark:border-amber-500/30 dark:bg-amber-500/10',
    error: 'border-rose-200 bg-rose-50 dark:border-rose-500/30 dark:bg-rose-500/10',
  }
  const iconTone = { success: 'text-emerald-500', warning: 'text-amber-500', error: 'text-rose-500' }
  return (
    <div role={s.tone === 'success' ? 'status' : 'alert'} className={cx('flex items-start gap-3 rounded-xl border p-3.5', tones[s.tone], className)}>
      <Icon aria-hidden="true" className={cx('mt-0.5 size-5 shrink-0', iconTone[s.tone])} />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-semibold text-slate-900 dark:text-white">{s.title}</div>
        {result.failed.length > 0 && (
          <ul className="scroll-thin mt-1.5 max-h-40 space-y-0.5 overflow-auto text-sm text-slate-600 dark:text-slate-300">
            {result.failed.map((f) => (
              <li key={String(f.id)} className="break-words">
                <span className="font-medium">{describe ? describe(f.id) : f.id}</span>: {f.message || f.code} <span className="font-mono text-xs text-slate-400">({f.code})</span>
              </li>
            ))}
          </ul>
        )}
      </div>
      <button
        type="button"
        aria-label="Dismiss"
        className="text-slate-400 hover:text-slate-600"
        onClick={() => {
          setDismissed(result)
          onDismiss?.()
        }}
      >
        <X className="size-4" />
      </button>
    </div>
  )
}
