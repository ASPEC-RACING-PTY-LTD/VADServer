import { useId, useRef, useState, type ReactNode } from 'react'
import { Check } from 'lucide-react'
import { Button, cx } from './ui'
import { errorMessage } from '../lib/api'

/** Result of a step validator: null/undefined/true = valid; a string (or false) = invalid with that message. */
export type StepValidation = string | boolean | null | undefined

export type StepDef = {
  id: string
  title: ReactNode
  description?: ReactNode
  /** Step body. A function receives navigation helpers. */
  content: ReactNode | ((ctx: StepContext) => ReactNode)
  /** Runs before leaving the step forwards (Next/Finish). May be async; a thrown error is shown as the message. */
  validate?: () => StepValidation | Promise<StepValidation>
  /** Skip this step entirely (e.g. not applicable to the current choices). */
  hidden?: boolean
}

export type StepContext = { index: number; isFirst: boolean; isLast: boolean; next: () => Promise<boolean>; back: () => void; goTo: (index: number) => void }

export type StepperProps = {
  steps: StepDef[]
  /** Called after the last step validates. May be async; errors are shown and the wizard stays open. */
  onFinish: () => void | Promise<void>
  onCancel?: () => void
  /** Controlled step index (into the visible steps). Omit for uncontrolled. */
  index?: number
  onIndexChange?: (index: number) => void
  finishLabel?: ReactNode
  nextLabel?: ReactNode
  backLabel?: ReactNode
  cancelLabel?: ReactNode
  /** Allow jumping back to completed steps by clicking them in the header. Default true. */
  allowJumpBack?: boolean
  className?: string
}

/** Multi-step wizard shell with per-step validation. Forward navigation always validates the current step. */
export function Stepper({ steps: allSteps, onFinish, onCancel, index: controlled, onIndexChange, finishLabel = 'Finish', nextLabel = 'Next', backLabel = 'Back', cancelLabel = 'Cancel', allowJumpBack = true, className }: StepperProps) {
  const steps = allSteps.filter((s) => !s.hidden)
  const [inner, setInner] = useState(0)
  const raw = controlled ?? inner
  const index = Math.max(0, Math.min(raw, steps.length - 1))
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const headingId = useId()
  const step = steps[index]
  const isFirst = index === 0
  const isLast = index === steps.length - 1

  const setIndex = (i: number) => {
    setError(null)
    if (controlled === undefined) setInner(i)
    onIndexChange?.(i)
  }

  async function runValidate(): Promise<boolean> {
    if (!step?.validate) return true
    try {
      const r = await step.validate()
      if (r === false) {
        setError('Please complete this step.')
        return false
      }
      if (typeof r === 'string' && r !== '') {
        setError(r)
        return false
      }
      return true
    } catch (e) {
      setError(errorMessage(e))
      return false
    }
  }

  async function next(): Promise<boolean> {
    if (busyRef.current) return false
    busyRef.current = true
    setBusy(true)
    setError(null)
    try {
      if (!(await runValidate())) return false
      if (isLast) {
        try {
          await onFinish()
        } catch (e) {
          setError(errorMessage(e))
          return false
        }
      } else {
        setIndex(index + 1)
      }
      return true
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }
  const back = () => {
    if (!isFirst && !busyRef.current) setIndex(index - 1)
  }
  const goTo = (i: number) => {
    if (i >= 0 && i < index && !busyRef.current) setIndex(i)
  }

  if (!step) return null
  const ctx: StepContext = { index, isFirst, isLast, next, back, goTo }

  return (
    <div className={cx('flex flex-col gap-5', className)}>
      <nav aria-label="Progress">
        <ol className="flex flex-wrap items-center gap-2">
          {steps.map((s, i) => {
            const done = i < index
            const current = i === index
            const canJump = allowJumpBack && done && !busy
            const badge = (
              <span className={cx('flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold', done ? 'bg-brand-600 text-white' : current ? 'border-2 border-brand-600 text-brand-700 dark:text-brand-300' : 'border border-slate-300 text-slate-500 dark:border-slate-700')}>
                {done ? <Check className="size-3.5" aria-hidden /> : i + 1}
              </span>
            )
            const label = <span className={cx('text-sm', current ? 'font-semibold text-slate-900 dark:text-slate-100' : 'text-slate-500 dark:text-slate-400')}>{s.title}</span>
            return (
              <li key={s.id} className="flex items-center gap-2" aria-current={current ? 'step' : undefined}>
                {canJump ? (
                  <button type="button" onClick={() => goTo(i)} className="flex items-center gap-2 rounded-md hover:underline">
                    {badge}
                    {label}
                    <span className="sr-only">(completed, go back to this step)</span>
                  </button>
                ) : (
                  <span className="flex items-center gap-2">
                    {badge}
                    {label}
                    {done && <span className="sr-only">(completed)</span>}
                  </span>
                )}
                {i < steps.length - 1 && <span aria-hidden className="mx-1 h-px w-6 bg-slate-300 dark:bg-slate-700" />}
              </li>
            )
          })}
        </ol>
      </nav>

      <section aria-labelledby={headingId} className="flex flex-col gap-3">
        <div>
          <h2 id={headingId} className="text-base font-semibold text-slate-900 dark:text-slate-100">
            <span className="sr-only">
              Step {index + 1} of {steps.length}:{' '}
            </span>
            {step.title}
          </h2>
          {step.description && <p className="mt-0.5 text-sm text-slate-500 dark:text-slate-400">{step.description}</p>}
        </div>
        <div>{typeof step.content === 'function' ? step.content(ctx) : step.content}</div>
        {error && (
          <p role="alert" className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
            {error}
          </p>
        )}
      </section>

      <div className="flex items-center justify-between gap-2 border-t border-slate-200 pt-4 dark:border-slate-800">
        <div>
          {onCancel && (
            <Button variant="ghost" onClick={onCancel} disabled={busy}>
              {cancelLabel}
            </Button>
          )}
        </div>
        <div className="flex gap-2">
          {!isFirst && (
            <Button onClick={back} disabled={busy}>
              {backLabel}
            </Button>
          )}
          <Button variant="primary" loading={busy} onClick={() => void next()}>
            {isLast ? finishLabel : nextLabel}
          </Button>
        </div>
      </div>
    </div>
  )
}
