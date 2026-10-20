import { FolderPlus, Upload } from 'lucide-react'
import { Button } from '../ui'

export type PickedFile = { file: File; rel: string }

/** New-folder and Uploads buttons of the Files header; Uploads opens the uploads dialog. */
export function UploadControls({ showButtons, activeCount, onNewFolder, onUploads }: { showButtons: boolean; activeCount: number; onNewFolder: () => void; onUploads: () => void }) {
  return (
    <>
      {showButtons && (
        <Button icon={<FolderPlus className="size-4" />} onClick={onNewFolder}>
          New folder
        </Button>
      )}
      <Button variant="primary" icon={<Upload className="size-4" />} onClick={onUploads}>
        Uploads{activeCount > 0 ? ` (${activeCount})` : ''}
      </Button>
    </>
  )
}
