import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get } from '../../lib/api'
import type { Crumb, Node, Sort } from './types'

/** Lists a folder (or search results) and applies the client-side name filter. */
export function useFilesQuery({ parent, sort, kind, searchQ, filter }: { parent: string; sort: Sort; kind: string; searchQ: string; filter: string }) {
  const listQ = useQuery({
    queryKey: ['files', parent, sort.by, sort.desc, kind, searchQ],
    queryFn: () =>
      searchQ
        ? get(`/files/search?q=${encodeURIComponent(searchQ)}&sort=${sort.by}&order=${sort.desc ? 'desc' : 'asc'}${kind ? `&kind=${kind}` : ''}&limit=500`).then((r) => ({ items: r.items, breadcrumbs: [], folder: null, total: r.items.length }))
        : get(`/files?parent=${parent}&sort=${sort.by}&order=${sort.desc ? 'desc' : 'asc'}${kind ? `&kind=${kind}` : ''}`),
  })
  const items: Node[] = useMemo(() => {
    const all: Node[] = listQ.data?.items ?? []
    if (!filter.trim()) return all
    const f = filter.toLowerCase()
    return all.filter((n) => n.name.toLowerCase().includes(f))
  }, [listQ.data, filter])
  const crumbs: Crumb[] = listQ.data?.breadcrumbs ?? []
  const folder: Node | null = listQ.data?.folder ?? null
  const folderPath = '/' + crumbs.slice(1).map((c) => c.name).join('/')
  return { listQ, items, crumbs, folder, folderPath }
}
