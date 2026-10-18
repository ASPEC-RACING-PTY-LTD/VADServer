import { Search, Upload } from 'lucide-react'
import { Button, EmptyState } from '../ui'

/** Empty state: "no matches" when filtering/searching, otherwise an empty-folder upload prompt. */
export function FilesEmpty({ filtered, onUpload }: { filtered: boolean; onUpload: () => void }) {
  return (
    <div className="card">
      {filtered ? (
        <EmptyState icon={<Search className="size-6" />} title="No matching files" description="Try a different search term or file type." />
      ) : (
        <EmptyState
          icon={<Upload className="size-6" />}
          title="This folder is empty"
          description="Drag files or folders here, or use the upload buttons. Files from the desktop app appear here automatically."
          action={
            <Button variant="primary" icon={<Upload className="size-4" />} onClick={onUpload}>
              Upload files
            </Button>
          }
        />
      )}
    </div>
  )
}
