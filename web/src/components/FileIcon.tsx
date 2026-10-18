import { FileArchive, FileAudio, FileImage, FileText, FileVideo, File as FileGeneric, Folder, FileSpreadsheet } from 'lucide-react'
import { fileKind } from '../lib/format'
import { cx } from './ui'

const styles: Record<string, string> = {
  folder: 'text-amber-500 bg-amber-100 dark:bg-amber-500/15',
  image: 'text-pink-600 bg-pink-100 dark:bg-pink-500/15 dark:text-pink-300',
  video: 'text-violet-600 bg-violet-100 dark:bg-violet-500/15 dark:text-violet-300',
  audio: 'text-teal-600 bg-teal-100 dark:bg-teal-500/15 dark:text-teal-300',
  pdf: 'text-rose-600 bg-rose-100 dark:bg-rose-500/15 dark:text-rose-300',
  text: 'text-slate-600 bg-slate-100 dark:bg-slate-700/40 dark:text-slate-300',
  archive: 'text-orange-600 bg-orange-100 dark:bg-orange-500/15 dark:text-orange-300',
  document: 'text-sky-600 bg-sky-100 dark:bg-sky-500/15 dark:text-sky-300',
  other: 'text-slate-500 bg-slate-100 dark:bg-slate-700/40 dark:text-slate-400',
}

export function FileIcon({ mime, isDir, size = 'md' }: { mime: string; isDir: boolean; size?: 'sm' | 'md' | 'lg' }) {
  const k = fileKind(mime, isDir)
  const Icon = { folder: Folder, image: FileImage, video: FileVideo, audio: FileAudio, pdf: FileText, text: FileText, archive: FileArchive, document: FileSpreadsheet, other: FileGeneric }[k]
  const sizes = { sm: 'size-7 rounded-md [&>svg]:size-4', md: 'size-9 rounded-lg [&>svg]:size-[18px]', lg: 'size-14 rounded-xl [&>svg]:size-7' }
  return (
    <span className={cx('inline-flex shrink-0 items-center justify-center', styles[k], sizes[size])}>
      <Icon fill={k === 'folder' ? 'currentColor' : 'none'} fillOpacity={k === 'folder' ? 0.25 : 0} />
    </span>
  )
}
