import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { errorMessage, post } from '../../lib/api'
import { useUI } from '../ui'
import type { Node } from './types'

/** Trash and move with confirmation, toasts and cache invalidation; both clear the selection on success. */
export function useFileMutations({ items, clearSelection }: { items: Node[]; clearSelection: () => void }) {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['files'] })
    qc.invalidateQueries({ queryKey: ['stats'] })
  }

  const deleteIDs = useCallback(
    async (ids: number[]) => {
      const names = items.filter((i) => ids.includes(i.id)).map((i) => i.name)
      const ok = await confirm({
        title: ids.length === 1 ? `Move "${names[0]}" to trash?` : `Move ${ids.length} items to trash?`,
        message: 'Items in the trash can be restored until they are permanently deleted or the retention period ends.',
        confirmLabel: 'Move to trash',
      })
      if (!ok) return
      try {
        const res = await post('/files/delete', { ids })
        toast('success', `Moved ${res.deleted} item(s) to trash`, res.failed?.length ? `${res.failed.length} could not be deleted` : undefined)
        clearSelection()
        refresh()
      } catch (e) {
        toast('error', 'Delete failed', errorMessage(e))
      }
    },
    [items, confirm, toast],
  )

  const moveTo = async (ids: number[], target: number) => {
    try {
      const res = await post('/files/move', { ids, parent_id: target })
      if (res.failed?.length) toast('warning', `Moved ${res.moved} item(s)`, res.failed.map((f: any) => f.error).join('; '))
      else toast('success', `Moved ${res.moved} item(s)`)
      clearSelection()
      refresh()
    } catch (e) {
      toast('error', 'Move failed', errorMessage(e))
    }
  }

  return { refresh, deleteIDs, moveTo }
}
