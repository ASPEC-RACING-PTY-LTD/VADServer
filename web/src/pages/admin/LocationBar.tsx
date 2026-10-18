import { HardDrive } from 'lucide-react'
import { formatBytes } from '../../lib/format'
import { Badge, ProgressBar } from '../../components/ui'

/** Usage bar for one storage location (Overview, Storage and Health tabs). */
export function LocationBar({ l }: { l: any }) {
  const used = l.total_bytes - l.available_bytes
  return (
    <div>
      <div className="mb-1.5 flex items-center justify-between gap-2 text-sm">
        <span className="flex items-center gap-2 font-medium">
          <HardDrive className="size-4 text-slate-400" />
          {l.name} {l.is_default && <Badge tone="violet">default</Badge>} {!l.enabled && <Badge>disabled</Badge>}
          {!l.healthy ? <Badge tone="red">unavailable</Badge> : l.low_space ? <Badge tone="amber">low space</Badge> : null}
        </span>
        <span className="text-xs text-slate-500">{l.healthy ? `${formatBytes(l.available_bytes)} free of ${formatBytes(l.total_bytes)}` : ''}</span>
      </div>
      {l.healthy ? <ProgressBar value={l.total_bytes ? (used / l.total_bytes) * 100 : 0} tone={l.low_space ? 'red' : used / l.total_bytes > 0.8 ? 'amber' : 'brand'} /> : <div className="text-xs text-rose-600">{l.problem}</div>}
      <div className="mt-1 font-mono text-[11px] text-slate-400">
        {l.path} · VaultDock data {formatBytes(l.stored_bytes)} · reserve {formatBytes(l.reserve_bytes)}
        {l.pending_bytes > 0 && ` · ${formatBytes(l.pending_bytes)} incoming`}
      </div>
    </div>
  )
}
