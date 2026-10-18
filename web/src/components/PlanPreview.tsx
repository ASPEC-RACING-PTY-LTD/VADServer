import { useEffect, useState, type ReactNode } from 'react'
import { AlertTriangle, Clock } from 'lucide-react'
import { Badge, Button, cx } from './ui'
import { ApiError, errorMessage } from '../lib/api'

/** api-v1 §1 plan/execute: `…/plan` returns this; `…/execute {plan_token}` runs it. `expires_at` is Unix ms. */
export type PlanResponse<P = unknown> = {
  plan: P
  plan_token: string
  plan_digest: string
  expires_at: number
}

export type PlanStat = { label: string; value: ReactNode; tone?: 'slate' | 'green' | 'amber' | 'red' }

export type PlanPreviewProps<P = unknown> = {
  response: PlanResponse<P>
  /** Renders the plan body. Defaults to formatted JSON. */
  renderPlan?: (plan: P) => ReactNode
  /** Summary tiles shown above the plan (counts, bytes reclaimed, …). */
  stats?: PlanStat[]
  warnings?: ReactNode[]
  /** Execute with the plan token. A 409 `plan_stale` (or any error) is shown inline and the preview stays open. */
  onConfirm: (planToken: string) => void | Promise<void>
  onCancel: () => void
  /** Re-run the plan (offered once the token expired or went stale). */
  onReplan?: () => void | Promise<void>
  confirmLabel?: ReactNode
  cancelLabel?: ReactNode
  /** Use the danger button style for destructive plans. */
  danger?: boolean
  /** Require typing this text before confirming (strong confirmation). */
  requireText?: string
  className?: string
}

/** Shows a dry-run plan with its summary, warnings, digest and a live countdown to `expires_at`. */
export function PlanPreview<P = unknown>({ response, renderPlan, stats, warnings, onConfirm, onCancel, onReplan, confirmLabel = 'Execute plan', cancelLabel = 'Cancel', danger, requireText, className }: PlanPreviewProps<P>) {
  const remaining = useCountdown(response.expires_at)
  const expired = remaining <= 0
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [stale, setStale] = useState(false)
  const [typed, setTyped] = useState('')

  // A new plan clears the previous execution state.
  const [token, setToken] = useState(response.plan_token)
  if (token !== response.plan_token) {
    setToken(response.plan_token)
    setError(null)
    setStale(false)
    setTyped('')
  }

  const textOk = !requireText || typed === requireText
  const canConfirm = !expired && !stale && !busy && textOk

  async function confirm() {
    if (!canConfirm) return
    setBusy(true)
    setError(null)
    try {
      await onConfirm(response.plan_token)
    } catch (e) {
      if (e instanceof ApiError && e.code === 'plan_stale') {
        setStale(true)
        setError('The plan is out of date because something changed since it was computed. Re-plan to continue.')
      } else setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  async function replan() {
    if (!onReplan) return
    setBusy(true)
    setError(null)
    try {
      await onReplan()
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className={cx('flex flex-col gap-4', className)}>
      {stats && stats.length > 0 && (
        <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {stats.map((s) => (
            <div key={s.label} className="rounded-xl border border-slate-200 px-3 py-2 dark:border-slate-800">
              <dt className="text-xs text-slate-500 dark:text-slate-400">{s.label}</dt>
              <dd className={cx('mt-0.5 text-lg font-semibold', s.tone === 'red' ? 'text-rose-600' : s.tone === 'amber' ? 'text-amber-600' : s.tone === 'green' ? 'text-emerald-600' : 'text-slate-900 dark:text-slate-100')}>{s.value}</dd>
            </div>
          ))}
        </dl>
      )}

      {warnings && warnings.length > 0 && (
        <ul className="flex flex-col gap-1.5 rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:bg-amber-950/40 dark:text-amber-200" aria-label="Warnings">
          {warnings.map((w, i) => (
            <li key={i} className="flex gap-2">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" aria-hidden />
              <span>{w}</span>
            </li>
          ))}
        </ul>
      )}

      <div className="max-h-96 overflow-auto rounded-lg border border-slate-200 scroll-thin dark:border-slate-800">
        {renderPlan ? renderPlan(response.plan) : <pre className="p-3 font-mono text-xs whitespace-pre-wrap">{JSON.stringify(response.plan, null, 2)}</pre>}
      </div>

      <div className="flex flex-wrap items-center gap-3 text-xs text-slate-500 dark:text-slate-400">
        <span className="flex items-center gap-1.5" role="timer" aria-live="off">
          <Clock className="size-3.5" aria-hidden />
          {expired ? <Badge tone="red">Plan expired</Badge> : <span>Valid for {formatRemaining(remaining)}</span>}
        </span>
        <span className="font-mono" title={response.plan_digest}>
          Digest {response.plan_digest.slice(0, 12)}
        </span>
      </div>

      {requireText && (
        <label className="block">
          <span className="label">
            Type <span className="font-mono normal-case">{requireText}</span> to confirm
          </span>
          <input className="input" value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} disabled={expired || stale} />
        </label>
      )}

      {(error || expired) && (
        <p role="alert" className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
          {error ?? 'This plan has expired. Re-plan to get a fresh one.'}
        </p>
      )}

      <div className="flex justify-end gap-2">
        <Button variant="ghost" onClick={onCancel} disabled={busy}>
          {cancelLabel}
        </Button>
        {(expired || stale) && onReplan ? (
          <Button variant="primary" loading={busy} onClick={() => void replan()}>
            Re-plan
          </Button>
        ) : (
          <Button variant={danger ? 'danger' : 'primary'} loading={busy} disabled={!canConfirm} onClick={() => void confirm()}>
            {confirmLabel}
          </Button>
        )}
      </div>
    </div>
  )
}

/** Milliseconds until `deadline` (Unix ms), updated every second; never negative. */
export function useCountdown(deadline: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (deadline - Date.now() <= 0) return
    const t = setInterval(() => {
      const n = Date.now()
      setNow(n)
      if (n >= deadline) clearInterval(t)
    }, 1000)
    return () => clearInterval(t)
  }, [deadline])
  return Math.max(0, deadline - now)
}

function formatRemaining(ms: number): string {
  const s = Math.ceil(ms / 1000)
  const m = Math.floor(s / 60)
  return m > 0 ? `${m}m ${String(s % 60).padStart(2, '0')}s` : `${s}s`
}
