import { useEffect, useState } from 'react'
import { Toggle } from '../../../components/ui'

export const GB = 1024 ** 3

export function QuotaInput({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  const [unlimited, setUnlimited] = useState(value === 0)
  const [gb, setGb] = useState(value ? +(value / GB).toFixed(2) : 100)
  useEffect(() => onChange(unlimited ? 0 : Math.round(gb * GB)), [unlimited, gb])
  return (
    <div className="space-y-2">
      <Toggle checked={unlimited} onChange={setUnlimited} label="Unlimited" description="Limited only by free disk space" />
      {!unlimited && (
        <div className="flex items-center gap-2">
          <input className="input" type="number" min={0.01} step="any" value={gb} onChange={(e) => setGb(Number(e.target.value))} />
          <span className="text-sm text-slate-500">GB</span>
        </div>
      )}
    </div>
  )
}
