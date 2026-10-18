/**
 * Chart colours come from the Tailwind theme CSS variables (index.css @theme), with literal fallbacks in case a
 * variable is not emitted. Pass any CSS colour to override.
 */
export const chartColors = {
  brand: 'var(--color-brand-500, #6366f1)',
  green: 'var(--color-emerald-500, #10b981)',
  amber: 'var(--color-amber-500, #f59e0b)',
  red: 'var(--color-rose-500, #f43f5e)',
  sky: 'var(--color-sky-500, #0ea5e9)',
  violet: 'var(--color-violet-500, #8b5cf6)',
  slate: 'var(--color-slate-400, #94a3b8)',
} as const

export type ChartTone = keyof typeof chartColors

export function toneColor(c: ChartTone | (string & {}) | undefined, fallback: ChartTone = 'brand'): string {
  if (!c) return chartColors[fallback]
  return c in chartColors ? chartColors[c as ChartTone] : c
}
