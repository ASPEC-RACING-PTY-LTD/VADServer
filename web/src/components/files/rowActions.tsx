import { Clapperboard, Download, Eye, FolderInput, Link2, Pencil, Tag, Trash2 } from 'lucide-react'
import { previewable } from '../../lib/format'
import type { MenuItem } from '../ui'
import type { Node } from './types'

export type RowActionHandlers = {
  open: (n: Node) => void
  download: (ids: number[]) => void
  showInFolder: (n: Node) => void
  rename: (n: Node) => void
  move: (ids: number[]) => void
  editTags: (n: Node) => void
  trash: (ids: number[]) => void
  /** Opens the video editor (video files only). */
  editVideo?: (n: Node) => void
  /** Creates a public link (video files only). */
  share?: (n: Node) => void
}

/** Context-menu items for `n`; acts on the whole selection when `n` is part of a multi-selection. */
export function rowActions(n: Node, selected: Set<number>, inSearch: boolean, h: RowActionHandlers): MenuItem[] {
  const ids = selected.has(n.id) && selected.size > 1 ? [...selected] : [n.id]
  if (ids.length > 1) {
    return [
      { label: `Download ${ids.length} items (zip)`, icon: <Download className="size-4" />, onClick: () => h.download(ids) },
      { label: 'Move to…', icon: <FolderInput className="size-4" />, onClick: () => h.move(ids) },
      'divider',
      { label: 'Move to trash', icon: <Trash2 className="size-4" />, danger: true, onClick: () => h.trash(ids) },
    ]
  }
  return [
    { label: n.is_dir ? 'Open' : 'Preview', icon: <Eye className="size-4" />, onClick: () => h.open(n), disabled: !n.is_dir && !previewable(n.mime) },
    ...(h.editVideo && !n.is_dir && n.mime.startsWith('video/') ? [{ label: 'Edit video', icon: <Clapperboard className="size-4" />, onClick: () => h.editVideo!(n) }] : []),
    ...(h.share && !n.is_dir && n.mime.startsWith('video/') ? [{ label: 'Share link…', icon: <Link2 className="size-4" />, onClick: () => h.share!(n) }] : []),
    { label: n.is_dir ? 'Download as zip' : 'Download', icon: <Download className="size-4" />, onClick: () => h.download([n.id]) },
    ...(inSearch && n.parent_id ? [{ label: 'Show in folder', icon: <FolderInput className="size-4" />, onClick: () => h.showInFolder(n) }] : []),
    'divider',
    { label: 'Rename', icon: <Pencil className="size-4" />, onClick: () => h.rename(n) },
    { label: 'Move to…', icon: <FolderInput className="size-4" />, onClick: () => h.move([n.id]) },
    ...(!n.is_dir ? [{ label: 'Edit tags', icon: <Tag className="size-4" />, onClick: () => h.editTags(n) }] : []),
    'divider',
    { label: 'Move to trash', icon: <Trash2 className="size-4" />, danger: true, onClick: () => h.trash([n.id]) },
  ]
}
