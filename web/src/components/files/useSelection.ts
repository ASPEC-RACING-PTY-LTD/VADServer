import { useState, type MouseEvent as RMouseEvent } from 'react'
import type { Node } from './types'

/** Click, ctrl/cmd-toggle and shift-range selection over an ordered item list. */
export function useSelection(items: Node[]) {
  const [selected, setSelected] = useState<Set<number>>(new Set())
  const [anchor, setAnchor] = useState<number | null>(null)

  const onRowClick = (e: RMouseEvent, n: Node, idx: number) => {
    if (e.shiftKey && anchor !== null) {
      const a = items.findIndex((i) => i.id === anchor)
      const [s, t] = a < idx ? [a, idx] : [idx, a]
      setSelected(new Set(items.slice(s, t + 1).map((i) => i.id)))
    } else if (e.ctrlKey || e.metaKey) {
      const next = new Set(selected)
      next.has(n.id) ? next.delete(n.id) : next.add(n.id)
      setSelected(next)
      setAnchor(n.id)
    } else {
      setSelected(new Set([n.id]))
      setAnchor(n.id)
    }
  }

  const toggleOne = (id: number) => {
    const next = new Set(selected)
    next.has(id) ? next.delete(id) : next.add(id)
    setSelected(next)
    setAnchor(id)
  }

  return { selected, setSelected, onRowClick, toggleOne }
}

export type Selection = ReturnType<typeof useSelection>
