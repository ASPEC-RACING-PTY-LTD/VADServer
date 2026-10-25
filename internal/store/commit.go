package store

import (
	"context"
	"database/sql"
	"errors"
)

// CommitRequest describes a fully received and verified file.
type CommitRequest struct {
	User         *User
	DirSegs      []string
	Name         string
	Size         int64
	SHA256       string
	Mime         string
	ModifiedAt   int64
	Tags         []string
	OnConflict   string // replace, rename, skip
	DeviceID     *int64
	LocationID   int64 // location holding the blob
	TransferID   int64
	UploadID     string
	KeepReplaced bool // move replaced files to trash instead of deleting

	// Identity of an updated file (upload.identity). BaseSHA256 is the
	// content the uploading device last stored at this path: an existing
	// file that still has it is this device's own earlier version, so it is
	// updated whatever OnConflict says. PrevSegs is the file's path before a
	// rename or move on the device: when nothing is at the new path yet, the
	// file there (with BaseSHA256 or the new content) is moved instead of
	// stored again. FileCreatedAt is the creation time on the device (unix
	// ms), kept once set.
	BaseSHA256    string
	PrevSegs      []string
	FileCreatedAt int64

	// AfterTx, when set, runs inside the commit's transaction once the file
	// node exists (not for skipped files), so a caller can link it to its own
	// records atomically (smart processing outputs).
	AfterTx func(tx *sql.Tx, nodeID int64) error
}

// CommitResult reports what happened.
type CommitResult struct {
	Node   *Node
	Result string // created, replaced, renamed, moved, unchanged, skipped
	Freed  []BlobRef
}

// CommitFile stores the file node and blob reference in one transaction.
// The blob file must already be in place (see storage.Blobs.Place).
//
// New content for an existing file updates that file in place: its id,
// creation dates, tags and links stay, and the content it had is released
// (or kept in the trash with KeepReplaced). A numbered copy is only made for
// OnConflict "rename" when the existing file is not the device's own
// earlier version.
func (s *Store) CommitFile(ctx context.Context, r CommitRequest) (*CommitResult, error) {
	res := &CommitResult{}
	var nodeID int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var quota, used int64
		var disabled bool
		if err := tx.QueryRowContext(ctx, "SELECT quota_bytes, used_bytes, disabled FROM users WHERE id = ?", r.User.ID).Scan(&quota, &used, &disabled); err != nil {
			return err
		}
		if disabled {
			return errors.New("account is disabled")
		}
		parent, err := s.EnsureDirs(ctx, tx, r.User, r.DirSegs, r.DeviceID)
		if err != nil {
			return err
		}
		name := r.Name
		existing, err := s.childByName(ctx, tx, r.User.ID, parent, name)
		moved := false
		if errors.Is(err, ErrNotFound) && len(r.PrevSegs) > 0 {
			if existing, err = s.moveForCommit(ctx, tx, r, parent); err == nil {
				moved = true
			}
		}
		update := false
		switch {
		case errors.Is(err, ErrNotFound):
			res.Result = "created"
		case err != nil:
			return err
		case existing.IsDir:
			return ErrIsDir
		case existing.SHA256 == r.SHA256 && existing.Size == r.Size:
			res.Result = "unchanged"
			if moved {
				res.Result = "moved"
			}
			if _, err := tx.ExecContext(ctx, "UPDATE nodes SET modified_at = ?, file_created_at = COALESCE(file_created_at, ?), updated_at = ? WHERE id = ?",
				r.ModifiedAt, nullIfZero(r.FileCreatedAt), now(), existing.ID); err != nil {
				return err
			}
			if err := setTagsTx(ctx, tx, existing.ID, r.Tags, false); err != nil {
				return err
			}
			nodeID = existing.ID
			if r.AfterTx != nil {
				if err := r.AfterTx(tx, nodeID); err != nil {
					return err
				}
			}
			return finishUpload(ctx, tx, r, "unchanged")
		case moved || (r.BaseSHA256 != "" && existing.SHA256 == r.BaseSHA256):
			update = true // the device's own earlier version
		default:
			switch r.OnConflict {
			case "skip":
				res.Result = "skipped"
				nodeID = existing.ID
				return finishUpload(ctx, tx, r, "skipped")
			case "rename":
				for i := 1; ; i++ {
					name = numberedName(r.Name, false, i, "")
					if _, err := s.childByName(ctx, tx, r.User.ID, parent, name); errors.Is(err, ErrNotFound) {
						break
					} else if err != nil {
						return err
					}
					if i > 10000 {
						return ErrExists
					}
				}
				res.Result = "renamed"
			default:
				update = true
			}
		}
		t := now()
		if update {
			freed, err := releaseContentTx(ctx, tx, existing, r.KeepReplaced, t)
			if err != nil {
				return err
			}
			res.Freed = freed
			if err := tx.QueryRowContext(ctx, "SELECT used_bytes FROM users WHERE id = ?", r.User.ID).Scan(&used); err != nil {
				return err
			}
			res.Result = "replaced"
		}
		if quota > 0 && used+r.Size > quota {
			return ErrQuota
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO blobs(user_id, sha256, size, location_id, refcount, created_at) VALUES(?,?,?,?,1,?)
			ON CONFLICT(user_id, sha256) DO UPDATE SET refcount = refcount + 1`, r.User.ID, r.SHA256, r.Size, r.LocationID, t); err != nil {
			return err
		}
		if update {
			nodeID = existing.ID
			if _, err := tx.ExecContext(ctx, `UPDATE nodes SET size = ?, sha256 = ?, mime = ?, modified_at = ?, updated_at = ?, revision = revision + 1,
				file_created_at = COALESCE(file_created_at, ?), origin_device_id = COALESCE(?, origin_device_id) WHERE id = ?`,
				r.Size, r.SHA256, r.Mime, r.ModifiedAt, t, nullIfZero(r.FileCreatedAt), r.DeviceID, nodeID); err != nil {
				return err
			}
			if err := setTagsTx(ctx, tx, nodeID, r.Tags, false); err != nil {
				return err
			}
		} else {
			ins, err := tx.ExecContext(ctx, `INSERT INTO nodes(user_id, parent_id, name, is_dir, size, sha256, mime, created_at, modified_at, updated_at, origin_device_id, file_created_at) VALUES(?,?,?,0,?,?,?,?,?,?,?,?)`,
				r.User.ID, parent, name, r.Size, r.SHA256, r.Mime, t, r.ModifiedAt, t, r.DeviceID, nullIfZero(r.FileCreatedAt))
			if isUniqueErr(err) {
				return ErrExists
			}
			if err != nil {
				return err
			}
			nodeID, _ = ins.LastInsertId()
			if err := setTagsTx(ctx, tx, nodeID, r.Tags, true); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET used_bytes = used_bytes + ? WHERE id = ?", r.Size, r.User.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE nodes SET updated_at = ? WHERE id = ?", t, parent); err != nil {
			return err
		}
		if r.AfterTx != nil {
			if err := r.AfterTx(tx, nodeID); err != nil {
				return err
			}
		}
		return finishUpload(ctx, tx, r, "completed")
	})
	if err != nil {
		return nil, err
	}
	n, err := s.NodeWithTags(ctx, r.User.ID, nodeID)
	if err != nil {
		return nil, err
	}
	res.Node = n
	return res, nil
}

// moveForCommit moves the file at r.PrevSegs into parent as r.Name when it
// still holds the device's earlier content (BaseSHA256) or already the new
// content. Anything else there is left alone (ErrNotFound): the upload is
// then stored as a new file.
func (s *Store) moveForCommit(ctx context.Context, tx *sql.Tx, r CommitRequest, parent int64) (*Node, error) {
	prev, err := s.resolveTx(ctx, tx, r.User, r.PrevSegs)
	if err != nil || prev.IsDir {
		return nil, ErrNotFound
	}
	if !(r.BaseSHA256 != "" && prev.SHA256 == r.BaseSHA256) && !(prev.SHA256 == r.SHA256 && prev.Size == r.Size) {
		return nil, ErrNotFound
	}
	t := now()
	if _, err := tx.ExecContext(ctx, "UPDATE nodes SET parent_id = ?, name = ?, updated_at = ? WHERE id = ?", parent, r.Name, t, prev.ID); err != nil {
		if isUniqueErr(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if prev.ParentID != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE nodes SET updated_at = ? WHERE id = ?", t, *prev.ParentID); err != nil {
			return nil, err
		}
	}
	prev.ParentID, prev.Name = &parent, r.Name
	return prev, nil
}

// resolveTx walks segs from the user's root inside tx.
func (s *Store) resolveTx(ctx context.Context, tx *sql.Tx, user *User, segs []string) (*Node, error) {
	cur := user.RootID
	var n *Node
	for _, seg := range segs {
		child, err := s.childByName(ctx, tx, user.ID, cur, seg)
		if err != nil {
			return nil, err
		}
		n, cur = child, child.ID
	}
	if n == nil {
		return nil, ErrNotFound
	}
	return n, nil
}

// releaseContentTx drops a file's current content before it gets new
// content in place. With keep, the old content stays restorable as a
// trashed copy of the file, which holds the blob reference; otherwise the
// reference is released and a blob nothing uses any more is returned.
func releaseContentTx(ctx context.Context, tx *sql.Tx, n *Node, keep bool, t int64) ([]BlobRef, error) {
	if n.SHA256 == "" {
		return nil, nil
	}
	if keep {
		_, err := tx.ExecContext(ctx, `INSERT INTO nodes(user_id, parent_id, name, is_dir, size, sha256, mime, created_at, modified_at, updated_at, origin_device_id, file_created_at,
				deleted_at, trash_root, original_parent_id)
			SELECT user_id, parent_id, name, 0, size, sha256, mime, created_at, modified_at, ?, origin_device_id, file_created_at, ?, 1, parent_id FROM nodes WHERE id = ?`, t, t, n.ID)
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE blobs SET refcount = refcount - 1 WHERE user_id = ? AND sha256 = ?", n.userID, n.SHA256); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET used_bytes = MAX(0, used_bytes - ?) WHERE id = ?", n.Size, n.userID); err != nil {
		return nil, err
	}
	var ref, loc int64
	err := tx.QueryRowContext(ctx, "SELECT refcount, location_id FROM blobs WHERE user_id = ? AND sha256 = ?", n.userID, n.SHA256).Scan(&ref, &loc)
	if err != nil || ref > 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE user_id = ? AND sha256 = ?", n.userID, n.SHA256); err != nil {
		return nil, err
	}
	return []BlobRef{{UserID: n.userID, SHA256: n.SHA256, LocationID: loc}}, nil
}

func nullIfZero(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func finishUpload(ctx context.Context, tx *sql.Tx, r CommitRequest, status string) error {
	if r.UploadID != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE uploads SET status = 'done', updated_at = ? WHERE id = ?", now(), r.UploadID); err != nil {
			return err
		}
	}
	if r.TransferID > 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE transfers SET status = ?, bytes_done = size, finished_at = ? WHERE id = ?", status, now(), r.TransferID); err != nil {
			return err
		}
	}
	return nil
}

// BlobExists reports whether the blob row exists (used under the blob lock
// before deleting a file).
func (s *Store) BlobExists(ctx context.Context, userID int64, sha string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM blobs WHERE user_id = ? AND sha256 = ?", userID, sha).Scan(&n)
	return n > 0, err
}

// AllBlobKeys returns every blob row for orphan scans.
func (s *Store) AllBlobKeys(ctx context.Context, locationID int64) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT user_id, sha256 FROM blobs WHERE location_id = ?", locationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var u int64
		var sha string
		if err := rows.Scan(&u, &sha); err != nil {
			return nil, err
		}
		out[BlobKey(u, sha)] = true
	}
	return out, rows.Err()
}

func BlobKey(userID int64, sha string) string {
	return sha + "@" + itoa(userID)
}

func itoa(i int64) string {
	b := [20]byte{}
	n := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
		if i == 0 {
			break
		}
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}
