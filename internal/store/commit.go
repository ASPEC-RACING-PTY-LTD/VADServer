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
}

// CommitResult reports what happened.
type CommitResult struct {
	Node   *Node
	Result string // created, replaced, renamed, unchanged, skipped
	Freed  []BlobRef
}

// CommitFile inserts the file node and blob reference in one transaction.
// The blob file must already be in place (see storage.Blobs.Place).
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
		switch {
		case errors.Is(err, ErrNotFound):
			res.Result = "created"
		case err != nil:
			return err
		case existing.IsDir:
			return ErrIsDir
		case existing.SHA256 == r.SHA256 && existing.Size == r.Size:
			res.Result = "unchanged"
			if _, err := tx.ExecContext(ctx, "UPDATE nodes SET modified_at = ?, updated_at = ? WHERE id = ?", r.ModifiedAt, now(), existing.ID); err != nil {
				return err
			}
			if err := setTagsTx(ctx, tx, existing.ID, r.Tags, false); err != nil {
				return err
			}
			nodeID = existing.ID
			return finishUpload(ctx, tx, r, "unchanged")
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
				if r.KeepReplaced {
					if err := trashTx(ctx, tx, r.User.ID, existing.ID); err != nil {
						return err
					}
				} else {
					freed, err := purgeTree(ctx, tx, existing.ID)
					if err != nil {
						return err
					}
					res.Freed = freed
					if err := tx.QueryRowContext(ctx, "SELECT used_bytes FROM users WHERE id = ?", r.User.ID).Scan(&used); err != nil {
						return err
					}
				}
				res.Result = "replaced"
			}
		}
		if quota > 0 && used+r.Size > quota {
			return ErrQuota
		}
		t := now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO blobs(user_id, sha256, size, location_id, refcount, created_at) VALUES(?,?,?,?,1,?)
			ON CONFLICT(user_id, sha256) DO UPDATE SET refcount = refcount + 1`, r.User.ID, r.SHA256, r.Size, r.LocationID, t); err != nil {
			return err
		}
		ins, err := tx.ExecContext(ctx, `INSERT INTO nodes(user_id, parent_id, name, is_dir, size, sha256, mime, created_at, modified_at, updated_at, origin_device_id) VALUES(?,?,?,0,?,?,?,?,?,?,?)`,
			r.User.ID, parent, name, r.Size, r.SHA256, r.Mime, t, r.ModifiedAt, t, r.DeviceID)
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
		if _, err := tx.ExecContext(ctx, "UPDATE users SET used_bytes = used_bytes + ? WHERE id = ?", r.Size, r.User.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE nodes SET updated_at = ? WHERE id = ?", t, parent); err != nil {
			return err
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
