import { useState } from 'react'
import { sizeUnitFor, sizeUnits } from '../lib/format'

/** A byte size typed in MB, GB or TB (binary units); the unit follows the value. */
export function SizeInput({ value, onChange, min = 0, id, className }: { value: number; onChange: (bytes: number) => void; min?: number; id?: string; className?: string }) {
  const [unit, setUnit] = useState(() => sizeUnitFor(value))
  return (
    <div className={`flex gap-2 ${className ?? ''}`}>
      <input id={id} className="input min-w-0 flex-1" type="number" min={min} step="any" value={+(value / unit.mult).toFixed(3)} onChange={(e) => onChange(Math.round((Number(e.target.value) || 0) * unit.mult))} />
      <select
        className="input w-20 shrink-0"
        aria-label="Unit"
        value={unit.label}
        onChange={(e) => {
          const u = sizeUnits.find((x) => x.label === e.target.value) ?? sizeUnits[1]
          setUnit(u)
          onChange(Math.round((value / unit.mult) * u.mult))
        }}
      >
        {sizeUnits.map((u) => (
          <option key={u.label}>{u.label}</option>
        ))}
      </select>
    </div>
  )
}
