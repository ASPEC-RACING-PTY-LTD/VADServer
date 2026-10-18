package wire

// Catalog entry types of tree and search results; also the search type=
// filter values (api-v1 §5.4).
const (
	CatalogEntryFile = "file"
	CatalogEntryDir  = "dir"
)

// CatalogEntry is one item of GET /snapshots/{id}/tree?path=&cursor= and
// GET /snapshots/{id}/search?q=&type=&min_size=&max_size=&cursor= (sess;
// api-v1 §5.4), returned as List[CatalogEntry]. Path is the full token path
// (backup-format §6).
type CatalogEntry struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
}

// Catalog diff change kinds of SnapshotDiff.Paths, named after the rev 1.0
// §5.4 counts {added,removed,changed}.
const (
	CatalogDiffAdded   = "added"
	CatalogDiffRemoved = "removed"
	CatalogDiffChanged = "changed"
)

// SnapshotDiffMaxPaths is the number of paths SnapshotDiff lists at most
// (rev 1.0 §5.4).
const SnapshotDiffMaxPaths = 1000

// SnapshotDiff is the reply of GET /snapshots/diff?a=&b= (sess; rev 1.0
// §5.4): the added/removed/changed counts plus the first
// SnapshotDiffMaxPaths paths. The rev 1.0 e2e pack-delta form is gone with
// encryption_mode in rev 1.1.
type SnapshotDiff struct {
	Added   int64              `json:"added"`
	Removed int64              `json:"removed"`
	Changed int64              `json:"changed"`
	Paths   []CatalogDiffEntry `json:"paths"`
}

// CatalogDiffEntry is one path of SnapshotDiff.Paths. OldSize is 0 for
// added entries and NewSize is 0 for removed ones.
type CatalogDiffEntry struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Change  string `json:"change"`
	OldSize int64  `json:"old_size"`
	NewSize int64  `json:"new_size"`
}

// SnapshotPatchRequest is the body of PATCH /snapshots/{id} (sess;
// rev 1.0 §5.4 {tags?, protected?, protected_until?}). Omitted fields are
// kept. Unprotecting returns 202 ApprovalAccepted instead of the updated
// Snapshot.
type SnapshotPatchRequest struct {
	Tags           *[]string `json:"tags,omitempty"`
	Protected      *bool     `json:"protected,omitempty"`
	ProtectedUntil *int64    `json:"protected_until,omitempty"`
}

// SnapshotDownloadRequest is the body of POST /snapshots/{id}/download
// (sess, full escrow only; api-v1 §5.4). The reply is 202 OperationAccepted
// (operation kind snapshot_download).
type SnapshotDownloadRequest struct {
	Paths []string `json:"paths"`
}

// SnapshotDownloadResult is Operation.result of a finished
// snapshot_download: a single-use tokenised zip URL valid for 1 h
// (api-v1 §5.4).
type SnapshotDownloadResult struct {
	DownloadURL string `json:"download_url"`
	// Rev 1.1c additions.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	Files     int64 `json:"files,omitempty"`
	Bytes     int64 `json:"bytes,omitempty"`
}

// CatalogRebuildRequest is the body of POST /catalog/rebuild (sess;
// api-v1 §5.4). The reply is 202 OperationAccepted (operation kind
// catalog_rebuild).
type CatalogRebuildRequest struct {
	RepoID int64 `json:"repo_id"`
}
