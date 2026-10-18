import { useId } from 'react'
import { cx } from '../ui'
import { toneColor, type ChartTone } from './colors'

export type SparklineProps = {
  /** Y values in order (x is evenly spaced). Non-finite values are skipped. */
  values: number[]
  /** Accessible title (SVG <title>). */
  title: string
  format?: (v: number) => string
  /** Fill the area under the line. */
  area?: boolean
  color?: ChartTone | (string & {})
  width?: number | string
  height?: number
  /** Force the y-axis to include 0 (default true). */
  zeroBased?: boolean
  className?: string
}

/** Builds the SVG path for evenly spaced values in a w×h box (y inverted). Exported for tests. */
export function sparkPath(values: number[], w: number, h: number, zeroBased = true): { line: string; area: string; last?: [number, number] } {
  const pts = values.map((v, i) => [i, v] as const).filter(([, v]) => Number.isFinite(v))
  if (pts.length === 0) return { line: '', area: '' }
  let lo = Math.min(...pts.map(([, v]) => v))
  let hi = Math.max(...pts.map(([, v]) => v))
  if (zeroBased) {
    lo = Math.min(lo, 0)
    hi = Math.max(hi, 0)
  }
  if (hi === lo) hi = lo + 1
  const pad = 1
  const step = values.length > 1 ? w / (values.length - 1) : 0
  const xy = pts.map(([i, v]) => [values.length > 1 ? i * step : w / 2, pad + (1 - (v - lo) / (hi - lo)) * (h - 2 * pad)] as [number, number])
  const line = xy.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(2)} ${y.toFixed(2)}`).join(' ')
  const base = pad + (1 - (Math.max(lo, Math.min(0, hi)) - lo) / (hi - lo)) * (h - 2 * pad)
  const area = `${line} L${xy[xy.length - 1][0].toFixed(2)} ${base.toFixed(2)} L${xy[0][0].toFixed(2)} ${base.toFixed(2)} Z`
  return { line, area, last: xy[xy.length - 1] }
}

const VB_W = 100

/** Line or area sparkline (plain SVG, no axes). */
export function Sparkline({ values, title, format = String, area, color, width = '100%', height = 32, zeroBased = true, className }: SparklineProps) {
  const id = useId()
  const c = toneColor(color)
  const p = sparkPath(values, VB_W, height, zeroBased)
  const finite = values.filter((v) => Number.isFinite(v))
  const desc = finite.length ? `${finite.length} points, min ${format(Math.min(...finite))}, max ${format(Math.max(...finite))}, latest ${format(finite[finite.length - 1])}` : 'No data'
  return (
    <svg role="img" aria-labelledby={`${id}-t ${id}-d`} viewBox={`0 0 ${VB_W} ${height}`} preserveAspectRatio="none" width={width} height={height} className={cx('block overflow-visible', className)}>
      <title id={`${id}-t`}>{title}</title>
      <desc id={`${id}-d`}>{desc}</desc>
      {area && p.area && <path d={p.area} fill={c} fillOpacity={0.15} stroke="none" />}
      {p.line && <path d={p.line} fill="none" stroke={c} strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />}
    </svg>
  )
}
