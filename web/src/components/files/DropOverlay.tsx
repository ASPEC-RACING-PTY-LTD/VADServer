import { Upload } from 'lucide-react'

/** Shown over the page while external files are dragged onto it. */
export function DropOverlay({ folderPath }: { folderPath: string }) {
  return (
    <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-2xl border-2 border-dashed border-brand-500 bg-brand-50/80 backdrop-blur-sm dark:bg-brand-500/10">
      <div className="text-center">
        <Upload className="mx-auto mb-2 size-10 text-brand-600" />
        <div className="text-lg font-semibold text-brand-700 dark:text-brand-300">Drop to upload</div>
        <div className="text-sm text-brand-600/80">into {folderPath === '/' ? 'My files' : folderPath}</div>
      </div>
    </div>
  )
}
