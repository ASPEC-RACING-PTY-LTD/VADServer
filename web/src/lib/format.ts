export function formatBytes(n: number | undefined | null, digits = 1): string {
  if (n === undefined || n === null || isNaN(n)) return '-'
  if (n < 1024) return `${n} B`
  // Binary units throughout: 1 TB is 1024 GB, never shown as "1024 GB".
  const units = ['KB', 'MB', 'GB', 'TB', 'PB']
  let v = n
  let i = -1
  do {
    v /= 1024
    i++
  } while (v >= 1024 && i < units.length - 1)
  let text = v.toFixed(v >= 100 ? 0 : digits)
  if (Number(text) >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
    text = v.toFixed(digits)
  }
  return `${text} ${units[i]}`
}

/** Units for size inputs (binary). */
export const sizeUnits = [
  { label: 'MB', mult: 1024 ** 2 },
  { label: 'GB', mult: 1024 ** 3 },
  { label: 'TB', mult: 1024 ** 4 },
] as const

/** The largest unit that shows n as a whole-ish number (at least 1). */
export function sizeUnitFor(n: number): (typeof sizeUnits)[number] {
  return [...sizeUnits].reverse().find((u) => n >= u.mult) ?? sizeUnits[0]
}

export function formatSpeed(bps: number): string {
  if (!bps || bps < 1) return '-'
  return formatBytes(bps) + '/s'
}

export function formatDuration(sec: number): string {
  if (!isFinite(sec) || sec < 0) return '-'
  if (sec < 60) return `${Math.ceil(sec)}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${Math.round(sec % 60)}s`
  const h = Math.floor(sec / 3600)
  return `${h}h ${Math.round((sec % 3600) / 60)}m`
}

const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })

export function timeAgo(ms: number | null | undefined): string {
  if (!ms) return 'never'
  const diff = (ms - Date.now()) / 1000
  const abs = Math.abs(diff)
  if (abs < 45) return diff < 0 ? 'just now' : 'in a few seconds'
  if (abs < 3600) return rtf.format(Math.round(diff / 60), 'minute')
  if (abs < 86400) return rtf.format(Math.round(diff / 3600), 'hour')
  if (abs < 86400 * 30) return rtf.format(Math.round(diff / 86400), 'day')
  return new Date(ms).toLocaleDateString()
}

export function formatDate(ms: number | null | undefined): string {
  if (!ms) return '-'
  return new Date(ms).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

export type FileKind = 'folder' | 'image' | 'video' | 'audio' | 'pdf' | 'text' | 'archive' | 'document' | 'other'

export function fileKind(mime: string, isDir: boolean): FileKind {
  if (isDir) return 'folder'
  if (mime.startsWith('image/')) return 'image'
  if (mime.startsWith('video/')) return 'video'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime === 'application/pdf') return 'pdf'
  if (mime.startsWith('text/') || mime === 'application/json') return 'text'
  if (/zip|7z|rar|tar|gzip|bzip|x-xz/.test(mime)) return 'archive'
  if (/word|excel|sheet|presentation|powerpoint|opendocument|rtf/.test(mime)) return 'document'
  return 'other'
}

export function previewable(mime: string): boolean {
  const k = fileKind(mime, false)
  return k === 'image' || k === 'video' || k === 'audio' || k === 'pdf' || k === 'text'
}

export function pluralize(n: number, word: string, plural = word + 's') {
  return `${n.toLocaleString()} ${n === 1 ? word : plural}`
}

/** Duration from milliseconds (e.g. run times stored as ms). */
export function formatDurationMs(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !isFinite(ms) || ms < 0) return '-'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return formatDuration(ms / 1000)
}

/** Alias kept for new code: relative time from a Unix-ms timestamp. */
export const formatRelative = timeAgo

/** Unix-ms timestamp as an ISO-8601 string (for tooltips, exports). */
export function formatIso(ms: number | null | undefined): string {
  if (!ms) return '-'
  return new Date(ms).toISOString()
}

/** Unix-ms timestamp as a short time (or date + time when not today). */
export function formatTime(ms: number | null | undefined): string {
  if (!ms) return '-'
  const d = new Date(ms)
  const sameDay = d.toDateString() === new Date().toDateString()
  return sameDay ? d.toLocaleTimeString(undefined, { timeStyle: 'short' }) : formatDate(ms)
}

export function formatDateRange(fromMs: number | null | undefined, toMs: number | null | undefined): string {
  if (!fromMs && !toMs) return '-'
  if (!toMs) return `${formatDate(fromMs)} -`
  if (!fromMs) return `- ${formatDate(toMs)}`
  return `${formatDate(fromMs)} - ${formatDate(toMs)}`
}

export function formatPercent(fraction: number | null | undefined, digits = 0): string {
  if (fraction === null || fraction === undefined || !isFinite(fraction)) return '-'
  return `${(fraction * 100).toFixed(digits)}%`
}

export function formatCount(n: number | null | undefined): string {
  if (n === null || n === undefined || !isFinite(n)) return '-'
  return n.toLocaleString()
}
