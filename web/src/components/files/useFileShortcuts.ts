import { useEffect } from 'react'
import type { Node } from './types'

/** Page keyboard shortcuts: Delete, Ctrl/Cmd+A, Escape, F2 and Enter. */
export function useFileShortcuts({
  items,
  selected,
  setSelected,
  onDelete,
  onRename,
  onOpen,
}: {
  items: Node[]
  selected: Set<number>
  setSelected: (s: Set<number>) => void
  onDelete: (ids: number[]) => void
  onRename: (n: Node | null) => void
  onOpen: (n: Node) => void
}) {
  // Re-registered on every render so the handler always sees current state.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement)?.closest('input,textarea,[role=dialog]')) return
      if (e.key === 'Delete' && selected.size) onDelete([...selected])
      if ((e.ctrlKey || e.metaKey) && e.key === 'a') {
        e.preventDefault()
        setSelected(new Set(items.map((i) => i.id)))
      }
      if (e.key === 'Escape') setSelected(new Set())
      if (e.key === 'F2' && selected.size === 1) onRename(items.find((i) => selected.has(i.id)) ?? null)
      if (e.key === 'Enter' && selected.size === 1) {
        const n = items.find((i) => selected.has(i.id))
        if (n) onOpen(n)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })
}
