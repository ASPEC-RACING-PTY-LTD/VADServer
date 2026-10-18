import { useEffect, useRef, type ReactNode } from 'react'
import { Badge, ProgressBar, cx } from './ui'
import { formatBytes, formatCount, formatDuration, formatSpeed } from '../lib/format'
import { useOperation } from '../lib/query'
import { errorMessage, type Operation, type OperationId } from '../lib/api'

export type JobProgressState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | (string & {})

export type JobProgressProps = {
  title?: ReactNode
  /** Current phase, e.g. "Scanning", "Uploading", "Verifying". */
  phase?: ReactNode
  state?: JobProgressState
  bytesDone?: number
  bytesTotal?: number
  filesDone?: number
  filesTotal?: number
  /** Bytes per second. */
  speed?: number
  /** Seconds remaining. When omitted it is derived from speed and bytes remaining. */
  etaSec?: number
  /** Error text shown under the bar. */
  error?: ReactNode
  /** Extra content on the right of the header (e.g. a Cancel button). */
  actions?: ReactNode
  className?: string
}

const stateTone = { queued: 'slate', running: 'blue', succeeded: 'green', failed: 'red', cancelled: 'amber' } as const

/** Fraction 0..1 from bytes (preferred) or files; undefined when no total is known. */
export function progressFraction(p: Pick<JobProgressProps, 'bytesDone' | 'bytesTotal' | 'filesDone' | 'filesTotal'>): number | undefined {
  if (p.bytesTotal && p.bytesTotal > 0) return Math.min(1, (p.bytesDone ?? 0) / p.bytesTotal)
  if (p.filesTotal && p.filesTotal > 0) return Math.min(1, (p.filesDone ?? 0) / p.filesTotal)
  return undefined
}

/** Progress card body: phase, bar (indeterminate without a total), bytes/files counters, speed and ETA. */
export function JobProgress({ title, phase, state = 'running', bytesDone, bytesTotal, filesDone, filesTotal, speed, etaSec, error, actions, className }: JobProgressProps) {
  const frac = progressFraction({ bytesDone, bytesTotal, filesDone, filesTotal })
  const finished = state === 'succeeded' || state === 'failed' || state === 'cancelled'
  const eta = etaSec ?? (speed && speed > 0 && bytesTotal && bytesDone !== undefined ? Math.max(0, (bytesTotal - bytesDone) / speed) : undefined)
  const tone = state === 'failed' ? 'red' : state === 'cancelled' ? 'amber' : state === 'succeeded' ? 'green' : 'brand'
  const value = finished && state === 'succeeded' ? 100 : (frac ?? 0) * 100
  const label = typeof title === 'string' ? title : 'Job progress'
  return (
    <div className={cx('flex flex-col gap-2', className)}>
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          {title && <span className="truncate text-sm font-medium text-slate-900 dark:text-slate-100">{title}</span>}
          <Badge tone={stateTone[state as keyof typeof stateTone] ?? 'slate'}>{state}</Badge>
          {phase && !finished && <span className="truncate text-xs text-slate-500 dark:text-slate-400">{phase}</span>}
        </div>
        {actions}
      </div>
      <div aria-label={label}>
        <ProgressBar value={value} tone={tone} indeterminate={!finished && state !== 'queued' && frac === undefined} />
      </div>
      <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500 tabular-nums dark:text-slate-400">
        {frac !== undefined && <span>{Math.floor(frac * 100)}%</span>}
        {(bytesDone !== undefined || bytesTotal !== undefined) && (
          <span>
            {formatBytes(bytesDone ?? 0)}
            {bytesTotal ? ` of ${formatBytes(bytesTotal)}` : ''}
          </span>
        )}
        {(filesDone !== undefined || filesTotal !== undefined) && (
          <span>
            {formatCount(filesDone ?? 0)}
            {filesTotal ? ` of ${formatCount(filesTotal)}` : ''} files
          </span>
        )}
        {!finished && speed !== undefined && speed > 0 && <span>{formatSpeed(speed)}</span>}
        {!finished && eta !== undefined && <span>ETA {formatDuration(eta)}</span>}
      </div>
      {error && (
        <p role="alert" className="text-xs text-rose-600">
          {error}
        </p>
      )}
    </div>
  )
}

export type OperationProgressProps = Omit<JobProgressProps, 'state' | 'bytesDone' | 'filesDone' | 'filesTotal' | 'error'> & {
  operationId: OperationId | null | undefined
  /** Poll interval in ms (default 1500, see useOperation). */
  intervalMs?: number
  /** Called once when the operation reaches a terminal state. */
  onFinished?: (op: Operation) => void
}

/**
 * JobProgress fed by `GET /operations/{id}` via useOperation (polls until terminal).
 * Operation progress {done,total,bytes} maps to files done/total and bytes done.
 */
export function OperationProgress({ operationId, intervalMs, onFinished, phase, ...rest }: OperationProgressProps) {
  const q = useOperation(operationId, intervalMs)
  const op = q.data
  const finished = op && (op.state === 'succeeded' || op.state === 'failed' || op.state === 'cancelled')
  const reported = useRef<OperationId | null>(null)
  const cb = useRef(onFinished)
  useEffect(() => {
    cb.current = onFinished
  })
  useEffect(() => {
    if (op && finished && reported.current !== op.id) {
      reported.current = op.id
      cb.current?.(op)
    }
  }, [op, finished])

  const opError = op?.error ? (typeof op.error === 'string' ? op.error : (op.error.message ?? op.error.code ?? 'Failed')) : undefined
  return (
    <JobProgress
      {...rest}
      phase={phase ?? op?.kind}
      state={op?.state ?? 'queued'}
      bytesDone={op?.progress?.bytes}
      filesDone={op?.progress?.done}
      filesTotal={op?.progress?.total}
      error={q.error ? errorMessage(q.error) : opError}
    />
  )
}
