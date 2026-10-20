import { useEffect, useState } from 'react'
import { Toggle } from '../../../components/ui'
import { SizeInput } from '../../../components/SizeInput'

export const GB = 1024 ** 3

export function QuotaInput({ value, onChange }: { value: number; onChange: (v: number) => void }) {
  const [unlimited, setUnlimited] = useState(value === 0)
  const [bytes, setBytes] = useState(value || 100 * GB)
  useEffect(() => onChange(unlimited ? 0 : bytes), [unlimited, bytes])
  return (
    <div className="space-y-2">
      <Toggle checked={unlimited} onChange={setUnlimited} label="Unlimited" description="Limited only by free disk space" />
      {!unlimited && (
        <SizeInput value={bytes} onChange={setBytes} />
      )}
    </div>
  )
}
