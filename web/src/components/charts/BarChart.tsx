import { useId } from 'react'
import { cx } from '../ui'
import { toneColor, type ChartTone } from './colors'

export type BarDatum = { label: string; value: number; color?: ChartTone | (string & {}) }

export type BarChartProps = {
  data: BarDatum[]
  /** Accessible title (rendered as SVG <title>). */
  title: string
  /** Formats values for tooltips and the accessible description. */
  format?: (v: number) => string
  /** Pixel height of the SVG. Width is fluid (100%). */
  height?: number
  /** Default bar colour. */
  color?: ChartTone | (string & {})
  /** Show category labels under the bars. Default true. */
  showLabels?: boolean
  className?: string
}

const VB_W = 100

/** Vertical bar chart (plain SVG, fluid width). Values below 0 are drawn as 0. */
export function BarChart({ data, title, format = String, height = 120, color, showLabels = true, className }: BarChartProps) {
  const id = useId()
  const max = Math.max(0, ...data.map((d) => d.value))
  const n = Math.max(1, data.length)
  const slot = VB_W / n
  const gap = Math.min(slot * 0.25, 2)
  const desc = data.map((d) => `${d.label}: ${format(d.value)}`).join(', ')
  return (
    <figure className={cx('m-0 flex flex-col gap-1', className)}>
      <svg role="img" aria-labelledby={`${id}-t ${id}-d`} viewBox={`0 0 ${VB_W} ${height}`} preserveAspectRatio="none" width="100%" height={height} className="block overflow-visible">
        <title id={`${id}-t`}>{title}</title>
        <desc id={`${id}-d`}>{desc || 'No data'}</desc>
        <line x1={0} x2={VB_W} y1={height - 0.5} y2={height - 0.5} className="stroke-slate-200 dark:stroke-slate-800" strokeWidth={1} vectorEffect="non-scaling-stroke" />
        {data.map((d, i) => {
          const h = max > 0 ? (Math.max(0, d.value) / max) * (height - 1) : 0
          return (
            <rect key={`${d.label}-${i}`} x={i * slot + gap / 2} y={height - 1 - h} width={Math.max(0, slot - gap)} height={h} rx={0.6} fill={toneColor(d.color ?? color)}>
              <title>{`${d.label}: ${format(d.value)}`}</title>
            </rect>
          )
        })}
      </svg>
      {showLabels && data.length > 0 && (
        <div aria-hidden className="grid text-center text-[10px] text-slate-500 dark:text-slate-400" style={{ gridTemplateColumns: `repeat(${n}, minmax(0, 1fr))` }}>
          {data.map((d, i) => (
            <span key={`${d.label}-${i}`} className="truncate px-0.5">
              {d.label}
            </span>
          ))}
        </div>
      )}
    </figure>
  )
}

export type StackSegment = { label: string; value: number; color?: ChartTone | (string & {}) }

export type BarStackProps = {
  segments: StackSegment[]
  title: string
  format?: (v: number) => string
  /** Total the bar represents; defaults to the sum of segments (remaining space is drawn as track). */
  total?: number
  height?: number
  showLegend?: boolean
  className?: string
}

const STACK_TONES: ChartTone[] = ['brand', 'sky', 'green', 'amber', 'violet', 'red', 'slate']

/** Single horizontal stacked bar with an optional legend (e.g. storage by kind or by location). */
export function BarStack({ segments, title, format = String, total, height = 10, showLegend = true, className }: BarStackProps) {
  const id = useId()
  const sum = segments.reduce((a, s) => a + Math.max(0, s.value), 0)
  const denom = Math.max(total ?? sum, sum, 0)
  let x = 0
  const colorOf = (s: StackSegment, i: number) => toneColor(s.color ?? STACK_TONES[i % STACK_TONES.length])
  return (
    <figure className={cx('m-0 flex flex-col gap-2', className)}>
      <svg role="img" aria-labelledby={`${id}-t ${id}-d`} viewBox={`0 0 100 ${height}`} preserveAspectRatio="none" width="100%" height={height} className="block overflow-hidden rounded-full">
        <title id={`${id}-t`}>{title}</title>
        <desc id={`${id}-d`}>{segments.map((s) => `${s.label}: ${format(s.value)}`).join(', ') || 'No data'}</desc>
        <rect x={0} y={0} width={100} height={height} className="fill-slate-200 dark:fill-slate-800" />
        {denom > 0 &&
          segments.map((s, i) => {
            const w = (Math.max(0, s.value) / denom) * 100
            const r = (
              <rect key={`${s.label}-${i}`} x={x} y={0} width={w} height={height} fill={colorOf(s, i)}>
                <title>{`${s.label}: ${format(s.value)}`}</title>
              </rect>
            )
            x += w
            return r
          })}
      </svg>
      {showLegend && segments.length > 0 && (
        <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-600 dark:text-slate-300">
          {segments.map((s, i) => (
            <li key={`${s.label}-${i}`} className="flex items-center gap-1.5">
              <span aria-hidden className="size-2.5 rounded-full" style={{ background: colorOf(s, i) }} />
              {s.label} <span className="text-slate-500 tabular-nums dark:text-slate-400">{format(s.value)}</span>
            </li>
          ))}
        </ul>
      )}
    </figure>
  )
}
