import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Archive } from 'lucide-react'
import { errorMessage, get, post, put } from '../../../lib/api'
import { formatBytes } from '../../../lib/format'
import { Button, Toggle, useUI } from '../../../components/ui'

type Stats = { scanned: number; compressed: number; skipped: number; failed: number; bytes_before: number; bytes_after: number; finished_at: number }
type CompressionView = { enabled: boolean; running: boolean; current?: Stats; last?: Stats; last_error?: string }

/** At-rest storage compression: background toggle and a one-off "Compress now". */
export function StorageCompression() {
  const qc = useQueryClient()
  const { toast } = useUI()
  const q = useQuery({
    queryKey: ['admin-compression'],
    queryFn: () => get<CompressionView>('/admin/storage/compression'),
    refetchInterval: (x) => (x.state.data?.running ? 2000 : 30_000),
  })
  const v = q.data
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin-compression'] })
  const setEnabled = async (on: boolean) => {
    try {
      const cur = await get<{ settings: Record<string, unknown> }>('/admin/settings')
      await put('/admin/settings', { ...cur.settings, storage_compression: on })
      toast('success', on ? 'Storage compression on' : 'Storage compression off')
      refresh()
    } catch (e) {
      toast('error', 'Could not save', errorMessage(e))
    }
  }
  const run = async () => {
    try {
      await post('/admin/storage/compression/run')
      toast('success', 'Compression started')
      refresh()
    } catch (e) {
      toast('error', 'Could not start compression', errorMessage(e))
    }
  }
  const s = v?.running ? v.current : v?.last
  return (
    <div className="mb-4 rounded-2xl border border-slate-200 p-4 text-sm dark:border-slate-800">
      <div className="mb-2 flex flex-wrap items-center gap-3">
        <Archive className="size-4 text-slate-400" />
        <span className="font-semibold">Storage compression</span>
        <div className="ml-auto">
          <Button size="sm" loading={v?.running} disabled={!v || v.running} onClick={() => void run()}>
            {v?.running ? 'Compressing…' : 'Compress now'}
          </Button>
        </div>
      </div>
      <Toggle
        checked={!!v?.enabled}
        disabled={!v}
        onChange={(on) => void setEnabled(on)}
        label="Compress stored files automatically"
        description="Compresses files in the background where it saves space. Opening a compressed file extracts it to a temporary copy that is removed after 10 minutes without use."
      />
      {s && (
        <p className="mt-2 text-xs text-slate-500">
          {v?.running ? 'Running: ' : 'Last run: '}
          {s.compressed} compressed, {s.skipped} skipped{s.failed ? `, ${s.failed} failed` : ''} of {s.scanned} files · saved {formatBytes(Math.max(0, s.bytes_before - s.bytes_after))}
          {!v?.running && s.finished_at ? ` · ${new Date(s.finished_at).toLocaleString()}` : ''}
        </p>
      )}
      {v?.last_error && <p className="mt-1 text-xs text-rose-600">{v.last_error}</p>}
    </div>
  )
}
