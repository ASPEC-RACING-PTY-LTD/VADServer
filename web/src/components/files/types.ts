export type Node = {
  id: number
  parent_id: number | null
  name: string
  is_dir: boolean
  size: number
  mime: string
  modified_at: number
  created_at: number
  updated_at: number
  tags: string[]
  path?: string
  child_count?: number
  sha256?: string
}

export type Crumb = { id: number; name: string }

export type Sort = { by: string; desc: boolean }

export type ContentURL = (id: number, download?: boolean) => string

export const contentURL: ContentURL = (id, download = false) => `/api/v1/files/${id}/content${download ? '?download=1' : ''}`

/** MIME type used for dragging nodes onto folders and breadcrumbs. */
export const NODES_MIME = 'application/x-vaultdock-nodes'

export function downloadIDs(ids: number[], nodes: Node[]) {
  if (ids.length === 1) {
    const n = nodes.find((x) => x.id === ids[0])
    if (n && !n.is_dir) {
      window.location.href = contentURL(n.id, true)
      return
    }
  }
  window.location.href = `/api/v1/files/zip?ids=${ids.join(',')}`
}
