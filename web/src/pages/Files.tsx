import { useEffect, useRef, useState, type DragEvent } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { del } from '../lib/api'
import { previewable } from '../lib/format'
import { filesFromDrop, useUploads } from '../lib/uploads'
import { ContextMenu, ErrorState, PageLoader, useUI } from '../components/ui'
import { DropOverlay } from '../components/files/DropOverlay'
import { FileGrid } from '../components/files/FileGrid'
import { FilesEmpty } from '../components/files/FilesEmpty'
import { FilesHeader } from '../components/files/FilesHeader'
import { FilesToolbar, type FilesView } from '../components/files/FilesToolbar'
import { FileTable } from '../components/files/FileTable'
import { MoveModal } from '../components/files/MoveModal'
import { NewFolderModal } from '../components/files/NewFolderModal'
import { PreviewModal } from '../components/files/PreviewModal'
import { RenameModal } from '../components/files/RenameModal'
import { rowActions } from '../components/files/rowActions'
import { TagsModal } from '../components/files/TagsModal'
import { downloadIDs, NODES_MIME, type Node, type Sort } from '../components/files/types'
import { UploadControls, type PickedFile } from '../components/files/UploadControls'
import { useFileMutations } from '../components/files/useFileMutations'
import { useFilesQuery } from '../components/files/useFilesQuery'
import { useFileShortcuts } from '../components/files/useFileShortcuts'
import { useSelection } from '../components/files/useSelection'

// Kept for compatibility with any importer of the old monolithic page module.
export type { Node }
export { MoveModal }

export function FilesPage() {
  const { folderId } = useParams()
  const [params, setParams] = useSearchParams()
  const searchQ = params.get('search') ?? ''
  const navigate = useNavigate()
  const { toast } = useUI()
  const uploads = useUploads()

  const [view, setView] = useState<FilesView>(() => (localStorage.getItem('vd-files-view') as any) || 'list')
  const [sort, setSort] = useState<Sort>({ by: 'name', desc: false })
  const [kind, setKind] = useState('')
  const [filter, setFilter] = useState('')
  const [ctx, setCtx] = useState<{ x: number; y: number; node: Node } | null>(null)
  const [preview, setPreview] = useState<Node | null>(null)
  const [renaming, setRenaming] = useState<Node | null>(null)
  const [moving, setMoving] = useState<number[] | null>(null)
  const [tagging, setTagging] = useState<Node | null>(null)
  const [newFolder, setNewFolder] = useState(false)
  const [dragOver, setDragOver] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)

  const { listQ, items, crumbs, folder, folderPath } = useFilesQuery({ parent: folderId ?? 'root', sort, kind, searchQ, filter })
  const { selected, setSelected, onRowClick, toggleOne } = useSelection(items)
  const { refresh, deleteIDs, moveTo } = useFileMutations({ items, clearSelection: () => setSelected(new Set()) })

  useEffect(() => localStorage.setItem('vd-files-view', view), [view])
  useEffect(() => {
    setSelected(new Set())
    setFilter('')
  }, [folderId, searchQ])

  // highlight ?select=
  useEffect(() => {
    const s = Number(params.get('select'))
    if (s && items.some((i) => i.id === s)) {
      setSelected(new Set([s]))
      const n = items.find((i) => i.id === s)
      if (n && !n.is_dir && previewable(n.mime)) setPreview(n)
      params.delete('select')
      setParams(params, { replace: true })
    }
  }, [items, params, setParams])

  const open = (n: Node) => {
    if (n.is_dir) navigate(`/files/${n.id}`)
    else setPreview(n)
  }

  useFileShortcuts({ items, selected, setSelected, onDelete: deleteIDs, onRename: setRenaming, onOpen: open })

  const enqueueFiles = (files: PickedFile[]) => {
    if (searchQ) {
      toast('warning', 'Open a folder to upload into it')
      return
    }
    const base = folderPath === '/' ? '' : folderPath
    uploads.enqueue(files.map((f) => ({ file: f.file, path: `${base}/${f.rel}` })))
    toast('info', `Uploading ${files.length} file(s)`, 'Track progress on the Uploads page.')
  }

  const onDrop = async (e: DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    if (e.dataTransfer.types.includes(NODES_MIME)) return
    const files = await filesFromDrop(e.dataTransfer)
    if (files.length) enqueueFiles(files)
  }

  const menuFor = (n: Node) =>
    rowActions(n, selected, !!searchQ, {
      open,
      download: (ids) => downloadIDs(ids, items),
      showInFolder: (x) => navigate(`/files/${x.parent_id}?select=${x.id}`),
      rename: setRenaming,
      move: setMoving,
      editTags: setTagging,
      trash: deleteIDs,
    })

  const previewList = items.filter((i) => !i.is_dir && previewable(i.mime))

  const viewProps = {
    items,
    selected,
    onRowClick,
    onToggle: toggleOne,
    onOpen: open,
    onContextMenu: (n: Node, x: number, y: number) => {
      if (!selected.has(n.id)) setSelected(new Set([n.id]))
      setCtx({ x, y, node: n })
    },
    onActions: (n: Node, x: number, y: number) => setCtx({ x, y, node: n }),
  }

  return (
    <div
      className="relative min-h-[70vh]"
      onDragOver={(e) => {
        if (e.dataTransfer.types.includes('Files')) {
          e.preventDefault()
          setDragOver(true)
        }
      }}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as globalThis.Node)) setDragOver(false)
      }}
      onDrop={onDrop}
    >
      <FilesHeader
        searchQ={searchQ}
        matchCount={items.length}
        crumbs={crumbs}
        onBack={() => navigate('/files')}
        onMoveTo={moveTo}
        actions={<UploadControls showButtons={!searchQ} fileInput={fileInput} folderInput={folderInput} onNewFolder={() => setNewFolder(true)} onFiles={enqueueFiles} />}
      />

      <FilesToolbar
        selectedCount={selected.size}
        searchQ={searchQ}
        filter={filter}
        onFilter={setFilter}
        kind={kind}
        onKind={setKind}
        sort={sort}
        onSort={setSort}
        view={view}
        onView={setView}
        onDownload={() => downloadIDs([...selected], items)}
        onMove={() => setMoving([...selected])}
        onRename={() => setRenaming(items.find((i) => selected.has(i.id)) ?? null)}
        onDelete={() => deleteIDs([...selected])}
        onClear={() => setSelected(new Set())}
      />

      {listQ.isLoading ? (
        <PageLoader />
      ) : listQ.error ? (
        <ErrorState error={listQ.error} onRetry={() => listQ.refetch()} />
      ) : items.length === 0 ? (
        <FilesEmpty filtered={!!(searchQ || filter || kind)} onUpload={() => fileInput.current?.click()} />
      ) : view === 'list' ? (
        <FileTable {...viewProps} onSelectAll={(all) => setSelected(all ? new Set(items.map((i) => i.id)) : new Set())} onMoveTo={moveTo} showPath={!!searchQ} total={listQ.data?.total} filtered={!!filter} />
      ) : (
        <FileGrid {...viewProps} />
      )}

      {dragOver && <DropOverlay folderPath={folderPath} />}

      {ctx && <ContextMenu x={ctx.x} y={ctx.y} items={menuFor(ctx.node)} onClose={() => setCtx(null)} />}
      {preview && <PreviewModal node={preview} list={previewList} onNavigate={setPreview} onClose={() => setPreview(null)} />}
      <NewFolderModal open={newFolder} parentId={folder?.id ?? 0} onClose={() => setNewFolder(false)} onDone={refresh} />
      <RenameModal node={renaming} onClose={() => setRenaming(null)} onDone={refresh} />
      <TagsModal node={tagging} onClose={() => setTagging(null)} onDone={refresh} />
      <MoveModal ids={moving} onClose={() => setMoving(null)} onMove={(target) => moving && moveTo(moving, target)} />
    </div>
  )
}

export { del }
