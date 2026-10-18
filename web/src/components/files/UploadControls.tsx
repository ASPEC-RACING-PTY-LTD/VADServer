import type { RefObject } from 'react'
import { FolderPlus, FolderUp, Upload } from 'lucide-react'
import { Button } from '../ui'

export type PickedFile = { file: File; rel: string }

/** New-folder and upload buttons plus the hidden file/folder pickers they open. */
export function UploadControls({
  showButtons,
  fileInput,
  folderInput,
  onNewFolder,
  onFiles,
}: {
  showButtons: boolean
  fileInput: RefObject<HTMLInputElement | null>
  folderInput: RefObject<HTMLInputElement | null>
  onNewFolder: () => void
  onFiles: (files: PickedFile[]) => void
}) {
  return (
    <>
      {showButtons && (
        <>
          <Button icon={<FolderPlus className="size-4" />} onClick={onNewFolder}>
            New folder
          </Button>
          <Button icon={<FolderUp className="size-4" />} onClick={() => folderInput.current?.click()}>
            Upload folder
          </Button>
          <Button variant="primary" icon={<Upload className="size-4" />} onClick={() => fileInput.current?.click()}>
            Upload files
          </Button>
        </>
      )}
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          onFiles(Array.from(e.target.files ?? []).map((f) => ({ file: f, rel: f.name })))
          e.target.value = ''
        }}
      />
      <input
        ref={folderInput}
        type="file"
        hidden
        {...({ webkitdirectory: '', directory: '' } as any)}
        onChange={(e) => {
          onFiles(Array.from(e.target.files ?? []).map((f) => ({ file: f, rel: (f as any).webkitRelativePath || f.name })))
          e.target.value = ''
        }}
      />
    </>
  )
}
