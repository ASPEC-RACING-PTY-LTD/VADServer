import { useId, type ReactNode } from 'react'
import { cx } from './ui'
import { formatDate } from '../lib/format'

/** Schedule types (wire `JobSchedule.type`). */
export type ScheduleType = 'interval' | 'daily' | 'weekly' | 'cron' | 'manual'
/** Missed-run policy (wire `JobSchedule.catch_up`); '' means run_once. */
export type CatchUp = 'run_once' | 'skip'

/**
 * BackupJob.schedule exactly as on the wire (`sdk/wire.JobSchedule`, evaluated by `sdk/schedule`). Every field is
 * always present; fields the `type` does not use are ignored by the server.
 * - `at`: local wall-clock 'HH:MM' (24 h), used by daily and weekly.
 * - `weekdays`: ISO numbering, 1 = Monday … 7 = Sunday (weekly).
 * - `every_minutes`: interval length, 5..44640 (interval).
 * - `expr`: 5-field cron expression (cron).
 * - `timezone`: IANA zone, or '' / 'Local' for the device's zone.
 * - `jitter_minutes`: 0..1440 random-but-stable delay per device; for interval it must be below every_minutes.
 * `manual` means no schedule: the job only runs when started by hand.
 */
export type Schedule = {
  type: ScheduleType
  expr: string
  at: string
  weekdays: number[] | null
  every_minutes: number
  timezone: string
  catch_up: CatchUp | ''
  jitter_minutes: number
}

export type ScheduleEditorProps = {
  value: Schedule
  onChange: (value: Schedule) => void
  /** Restrict the types offered. Default: all. */
  types?: ScheduleType[]
  /** Minimum interval in minutes. Default 5 (sdk/schedule MinEveryMinutes). */
  minIntervalMinutes?: number
  /** Next run times (Unix ms) computed by the server, shown as a preview. */
  nextRuns?: number[]
  disabled?: boolean
  /** Accessible group label. */
  label?: string
  className?: string
}

/** Short weekday names in ISO order; the name of ISO day `d` is `WEEKDAYS[d - 1]`. */
export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] as const
const WEEKDAY_NAMES = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'] as const
const TYPE_LABELS: Record<ScheduleType, string> = { interval: 'Every…', daily: 'Daily', weekly: 'Weekly', cron: 'Cron', manual: 'Manual' }
const ALL_TYPES: ScheduleType[] = ['interval', 'daily', 'weekly', 'cron', 'manual']
const INTERVAL_PRESETS = [15, 30, 60, 120, 240, 360, 720]
const TIME_RE = /^([01]\d|2[0-3]):[0-5]\d$/
export const MAX_EVERY_MINUTES = 31 * 24 * 60
export const MAX_JITTER_MINUTES = 24 * 60

/** Converts a JS `Date.getDay()` value (0 = Sunday) to ISO (7 = Sunday). */
export const isoWeekday = (jsDay: number) => (jsDay === 0 ? 7 : jsDay)

function validTimezone(tz: string): boolean {
  if (tz === '' || tz === 'Local') return true
  try {
    new Intl.DateTimeFormat('en', { timeZone: tz })
    return true
  } catch {
    return false
  }
}

/** Returns a human-readable validation error, or null when the schedule is valid. Mirrors sdk/schedule Validate. */
export function validateSchedule(s: Schedule, minIntervalMinutes = 5): string | null {
  switch (s.type) {
    case 'manual':
      break
    case 'interval':
      if (!Number.isInteger(s.every_minutes) || s.every_minutes < minIntervalMinutes) return `Interval must be a whole number of at least ${minIntervalMinutes} minutes.`
      if (s.every_minutes > MAX_EVERY_MINUTES) return 'Interval must be at most 31 days.'
      if (Number.isInteger(s.jitter_minutes) && s.jitter_minutes >= s.every_minutes) return 'Jitter must be less than the interval.'
      break
    case 'daily':
      if (!TIME_RE.test(s.at)) return 'Enter a time as HH:MM.'
      break
    case 'weekly': {
      if (!TIME_RE.test(s.at)) return 'Enter a time as HH:MM.'
      const days = s.weekdays ?? []
      if (days.length === 0) return 'Pick at least one day.'
      if (days.some((d) => !Number.isInteger(d) || d < 1 || d > 7)) return 'Invalid weekday.'
      if (new Set(days).size !== days.length) return 'Duplicate weekday.'
      break
    }
    case 'cron':
      // Light check only; the server parses the expression fully (5-field Vixie cron).
      if (s.expr.trim().split(/\s+/).filter(Boolean).length !== 5) return 'Enter a cron expression with 5 fields: minute hour day month weekday.'
      break
    default:
      return 'Unknown schedule type.'
  }
  if (s.catch_up !== '' && s.catch_up !== 'run_once' && s.catch_up !== 'skip') return 'Unknown catch-up policy.'
  if (!Number.isInteger(s.jitter_minutes) || s.jitter_minutes < 0 || s.jitter_minutes > MAX_JITTER_MINUTES) return `Jitter must be a whole number from 0 to ${MAX_JITTER_MINUTES} minutes.`
  if (!validTimezone(s.timezone)) return 'Unknown time zone.'
  return null
}

/** One-line English description, e.g. "Every 2 hours", "Weekdays at 09:00", "Manual only". */
export function describeSchedule(s: Schedule): string {
  const tz = s.timezone && s.timezone !== 'Local' ? ` (${s.timezone})` : ''
  switch (s.type) {
    case 'manual':
      return 'Manual only'
    case 'interval': {
      const m = s.every_minutes
      if (m % 1440 === 0) return m === 1440 ? 'Every day' : `Every ${m / 1440} days`
      if (m % 60 === 0) return m === 60 ? 'Every hour' : `Every ${m / 60} hours`
      return `Every ${m} minutes`
    }
    case 'daily':
      return `Daily at ${s.at}${tz}`
    case 'weekly': {
      const d = [...new Set(s.weekdays ?? [])].sort((a, b) => a - b)
      const key = d.join(',')
      const days = key === '1,2,3,4,5' ? 'Weekdays' : key === '6,7' ? 'Weekends' : d.length === 7 ? 'Every day' : d.map((x) => WEEKDAYS[x - 1] ?? `?${x}`).join(', ')
      return `${days} at ${s.at}${tz}`
    }
    case 'cron':
      return `Cron "${s.expr.trim()}"${tz}`
  }
}

/** Default for `type`, carrying the other fields over from `from` where they are still sensible. */
export function defaultSchedule(type: ScheduleType, from?: Schedule): Schedule {
  const s: Schedule = { expr: '', at: '02:00', weekdays: [1, 2, 3, 4, 5], every_minutes: 60, timezone: '', catch_up: 'run_once', jitter_minutes: 0, ...from, type }
  if (!TIME_RE.test(s.at)) s.at = '02:00'
  if (!s.weekdays || s.weekdays.length === 0) s.weekdays = [1, 2, 3, 4, 5]
  if (!Number.isInteger(s.every_minutes) || s.every_minutes < 5) s.every_minutes = 60
  if (type === 'cron' && s.expr.trim() === '') {
    const [h, m] = s.at.split(':').map(Number)
    s.expr = `${m} ${h} * * *`
  }
  return s
}

/** Edits a Schedule: interval, daily, weekly, cron or manual, plus time zone, catch-up and jitter, with validation and a next-runs preview. */
export function ScheduleEditor({ value, onChange, types = ALL_TYPES, minIntervalMinutes = 5, nextRuns, disabled, label = 'Schedule', className }: ScheduleEditorProps) {
  const id = useId()
  const error = validateSchedule(value, minIntervalMinutes)
  const errId = `${id}-err`
  const describedBy = error ? errId : undefined
  const presets = INTERVAL_PRESETS.filter((p) => p >= minIntervalMinutes)
  const set = (patch: Partial<Schedule>) => onChange({ ...value, ...patch })
  const intOrNaN = (v: string) => (v === '' ? NaN : Number(v))

  const time = (
    <label className="block">
      <span className="label">At</span>
      <input type="time" className="input w-32" value={value.at} disabled={disabled} required aria-invalid={!!error && !TIME_RE.test(value.at)} aria-describedby={describedBy} onChange={(e) => set({ at: e.target.value })} />
    </label>
  )

  let body: ReactNode
  switch (value.type) {
    case 'manual':
      body = <p className="text-sm text-slate-600 dark:text-slate-300">No schedule: the job runs only when started manually.</p>
      break
    case 'interval': {
      const preset = presets.includes(value.every_minutes) ? String(value.every_minutes) : 'custom'
      body = (
        <div className="flex flex-wrap items-end gap-3">
          <label className="block">
            <span className="label">Repeat every</span>
            <select className="input w-auto" value={preset} disabled={disabled} onChange={(e) => e.target.value !== 'custom' && set({ every_minutes: Number(e.target.value) })}>
              {presets.map((p) => (
                <option key={p} value={p}>
                  {describeSchedule({ ...value, every_minutes: p }).replace(/^Every /, '')}
                </option>
              ))}
              <option value="custom">Custom…</option>
            </select>
          </label>
          <label className="block">
            <span className="label">Minutes</span>
            <input
              type="number"
              className="input w-28"
              min={minIntervalMinutes}
              max={MAX_EVERY_MINUTES}
              step={1}
              value={Number.isFinite(value.every_minutes) ? value.every_minutes : ''}
              disabled={disabled}
              aria-invalid={!!error}
              aria-describedby={describedBy}
              onChange={(e) => set({ every_minutes: intOrNaN(e.target.value) })}
            />
          </label>
        </div>
      )
      break
    }
    case 'daily':
      body = time
      break
    case 'weekly': {
      const days = value.weekdays ?? []
      const toggleDay = (d: number) => set({ weekdays: days.includes(d) ? days.filter((x) => x !== d) : [...days, d].sort((a, b) => a - b) })
      body = (
        <div className="flex flex-col gap-3">
          <fieldset>
            <legend className="label">On</legend>
            <div className="flex flex-wrap gap-1.5">
              {WEEKDAYS.map((d, i) => {
                const iso = i + 1
                const on = days.includes(iso)
                return (
                  <button
                    key={d}
                    type="button"
                    aria-pressed={on}
                    aria-label={WEEKDAY_NAMES[i]}
                    disabled={disabled}
                    onClick={() => toggleDay(iso)}
                    className={cx('h-8 w-11 rounded-lg border text-xs font-medium transition disabled:opacity-50', on ? 'border-brand-600 bg-brand-600 text-white' : 'border-slate-300 bg-white text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200')}
                  >
                    {d}
                  </button>
                )
              })}
            </div>
          </fieldset>
          {time}
        </div>
      )
      break
    }
    case 'cron':
      body = (
        <div>
          <label className="block">
            <span className="label">Cron expression</span>
            <input type="text" className="input font-mono" value={value.expr} disabled={disabled} spellCheck={false} placeholder="0 2 * * 1-5" aria-invalid={!!error} aria-describedby={`${id}-cron-hint${error ? ` ${errId}` : ''}`} onChange={(e) => set({ expr: e.target.value })} />
          </label>
          <p id={`${id}-cron-hint`} className="mt-1 text-xs text-slate-500 dark:text-slate-400">
            5 fields: minute hour day-of-month month day-of-week (0 or 7 = Sunday). Supports *, a-b, */n, lists and names (jan, mon).
          </p>
        </div>
      )
      break
  }

  return (
    <div role="group" aria-label={label} className={cx('flex flex-col gap-3', className)}>
      {types.length > 1 && (
        <div role="radiogroup" aria-label="Schedule type" className="inline-flex w-fit rounded-lg border border-slate-300 p-0.5 dark:border-slate-700">
          {types.map((k) => (
            <button
              key={k}
              type="button"
              role="radio"
              aria-checked={value.type === k}
              disabled={disabled}
              tabIndex={value.type === k ? 0 : -1}
              onClick={() => value.type !== k && onChange(defaultSchedule(k, value))}
              onKeyDown={(e) => {
                const dir = e.key === 'ArrowRight' || e.key === 'ArrowDown' ? 1 : e.key === 'ArrowLeft' || e.key === 'ArrowUp' ? -1 : 0
                if (!dir) return
                e.preventDefault()
                const nk = types[(types.indexOf(value.type) + dir + types.length) % types.length]
                onChange(defaultSchedule(nk, value))
                const sib = (e.currentTarget.parentElement?.children[types.indexOf(nk)] as HTMLElement | undefined) ?? undefined
                sib?.focus()
              }}
              className={cx('rounded-md px-3 py-1 text-sm font-medium transition', value.type === k ? 'bg-brand-600 text-white' : 'text-slate-600 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800')}
            >
              {TYPE_LABELS[k]}
            </button>
          ))}
        </div>
      )}
      {body}
      {value.type !== 'manual' && (
        <div className="flex flex-wrap items-end gap-3">
          {value.type !== 'interval' && (
            <label className="block">
              <span className="label">Time zone</span>
              <input type="text" className="input w-48" value={value.timezone} placeholder="Local (device)" disabled={disabled} spellCheck={false} aria-describedby={describedBy} onChange={(e) => set({ timezone: e.target.value.trim() })} />
            </label>
          )}
          <label className="block">
            <span className="label">If a run is missed</span>
            <select className="input w-auto" value={value.catch_up || 'run_once'} disabled={disabled} onChange={(e) => set({ catch_up: e.target.value as CatchUp })}>
              <option value="run_once">Run once when back</option>
              <option value="skip">Skip it</option>
            </select>
          </label>
          <label className="block">
            <span className="label">Jitter (minutes)</span>
            <input type="number" className="input w-24" min={0} max={MAX_JITTER_MINUTES} step={1} value={Number.isFinite(value.jitter_minutes) ? value.jitter_minutes : ''} disabled={disabled} aria-describedby={describedBy} onChange={(e) => set({ jitter_minutes: intOrNaN(e.target.value) })} />
          </label>
        </div>
      )}
      {error ? (
        <p id={errId} role="alert" className="text-xs text-rose-600">
          {error}
        </p>
      ) : (
        <p className="text-xs text-slate-500 dark:text-slate-400" aria-live="polite">
          {describeSchedule(value)}
        </p>
      )}
      {nextRuns && nextRuns.length > 0 && !error && value.type !== 'manual' && (
        <div>
          <span className="label">Next runs</span>
          <ul className="text-xs text-slate-600 dark:text-slate-300">
            {nextRuns.slice(0, 5).map((t) => (
              <li key={t}>{formatDate(t)}</li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}
