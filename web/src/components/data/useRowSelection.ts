import { useCallback, useState } from 'react'

export type RowKey = string | number

/**
 * Selection state that can be controlled (`selected` + `onChange`) or uncontrolled.
 * DataTable uses it internally; pages can lift it with the same shape.
 */
export function useRowSelection(selected?: ReadonlySet<RowKey>, onChange?: (next: Set<RowKey>) => void) {
  const [inner, setInner] = useState<ReadonlySet<RowKey>>(() => new Set())
  const value = selected ?? inner
  const set = useCallback(
    (next: Set<RowKey>) => {
      if (!selected) setInner(next)
      onChange?.(next)
    },
    [selected, onChange],
  )
  const toggle = useCallback(
    (key: RowKey) => {
      const next = new Set(value)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      set(next)
    },
    [value, set],
  )
  const clear = useCallback(() => set(new Set()), [set])
  return { selected: value, set, toggle, clear }
}
