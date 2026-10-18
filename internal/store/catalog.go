package store

// Owner: SRV-C (P1-16). The encrypted snapshot catalog (api-v1 §5.4,
// MASTER_PLAN D9): catalog_keys (per-user DEK wrapped by server custody),
// snapshot_files (path/name ciphertexts, HMAC blind indexes of the path and
// its parent) and snapshot_file_tokens (blind name tokens for search), plus
// the snapshots.ingest_state bookkeeping of catalog ingestion. The store
// never sees plaintext names; internal/catalog encrypts and blinds them.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// CatalogKey is one row of catalog_keys: the user's catalog DEK sealed by
// server key custody. KEKID names the wrapping key version.
type CatalogKey struct {
	UserID    int64
	KEKID     string
	Wrapped   []byte
	CreatedAt int64
	RotatedAt int64 // 0 when never rotated
}

// CatalogKey returns the user's catalog key row or ErrNotFound.
func (s *Store) CatalogKey(ctx context.Context, userID int64) (*CatalogKey, error) {
	var k CatalogKey
	var rot sql.NullInt64
	err := s.DB.QueryRowContext(ctx, "SELECT user_id, kek_id, dek_wrapped_by_kek, created_at, rotated_at FROM catalog_keys WHERE user_id = ?", userID).
		Scan(&k.UserID, &k.KEKID, &k.Wrapped, &k.CreatedAt, &rot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.RotatedAt = rot.Int64
	return &k, nil
}

// InsertCatalogKey stores k unless the user already has a key; inserted
// reports whether k was written (callers then re-read the winner).
func (s *Store) InsertCatalogKey(ctx context.Context, k *CatalogKey) (inserted bool, err error) {
	if k.CreatedAt == 0 {
		k.CreatedAt = now()
	}
	res, err := s.DB.ExecContext(ctx, "INSERT OR IGNORE INTO catalog_keys(user_id, kek_id, dek_wrapped_by_kek, created_at) VALUES(?,?,?,?)",
		k.UserID, k.KEKID, k.Wrapped, k.CreatedAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// PendingIngestSnapshots returns the ids of committed snapshots waiting for
// catalog ingestion, oldest first.
func (s *Store) PendingIngestSnapshots(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM snapshots WHERE ingest_state = 'pending' AND status = 'committed' ORDER BY id LIMIT ?", clampLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetSnapshotIngest records the catalog ingestion state of a snapshot.
// errText is cut to 500 bytes.
func (s *Store) SetSnapshotIngest(ctx context.Context, snapshotID int64, state, errText string) error {
	if len(errText) > 500 {
		errText = errText[:500]
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE snapshots SET ingest_state = ?, ingest_error = ? WHERE id = ?", state, errText, snapshotID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SnapshotIngestError returns the recorded ingestion error text.
func (s *Store) SnapshotIngestError(ctx context.Context, snapshotID int64) (string, error) {
	var e string
	err := s.DB.QueryRowContext(ctx, "SELECT ingest_error FROM snapshots WHERE id = ?", snapshotID).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return e, err
}

// TouchCatalogSync sets repositories.last_catalog_sync_at.
func (s *Store) TouchCatalogSync(ctx context.Context, repoID, at int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE repositories SET last_catalog_sync_at = ? WHERE id = ?", at, repoID)
	return err
}

// CatalogFile is one row of snapshot_files. PathBI/ParentBI are blind
// indexes; PathCT/NameCT ciphertexts; Tokens the blind name tokens (written
// to blind_tokens and mirrored into snapshot_file_tokens). MTime is Unix ms
// and Mode the Windows attribute bits.
type CatalogFile struct {
	ID         int64
	SnapshotID int64
	PathBI     []byte
	ParentBI   []byte
	PathCT     []byte
	NameCT     []byte
	Tokens     [][]byte
	Type       string
	Size       int64
	MTime      int64
	Mode       int64
}

// ClearCatalogFiles removes every catalog row of a snapshot (a restarted or
// repeated ingestion starts from scratch).
func (s *Store) ClearCatalogFiles(ctx context.Context, snapshotID int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM snapshot_file_tokens WHERE snapshot_id = ?", snapshotID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM snapshot_files WHERE snapshot_id = ?", snapshotID)
		return err
	})
}

// InsertCatalogFiles writes a batch of rows in one transaction. A row whose
// path blind index already exists in the snapshot is skipped (duplicate
// names differing only in their raw UTF-16 form keep the first).
func (s *Store) InsertCatalogFiles(ctx context.Context, snapshotID int64, files []CatalogFile) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		fs, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO snapshot_files(snapshot_id, path_bi, parent_bi, path_ct, name_ct, blind_tokens, type, size, mtime, mode)
			VALUES(?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer fs.Close()
		ts, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO snapshot_file_tokens(snapshot_id, token, file_id) VALUES(?,?,?)")
		if err != nil {
			return err
		}
		defer ts.Close()
		for i := range files {
			f := &files[i]
			var blind []byte
			for _, t := range f.Tokens {
				blind = append(blind, t...)
			}
			if blind == nil {
				blind = []byte{}
			}
			res, err := fs.ExecContext(ctx, snapshotID, f.PathBI, f.ParentBI, f.PathCT, f.NameCT, blind, f.Type, f.Size, f.MTime, f.Mode)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
			id, err := res.LastInsertId()
			if err != nil {
				return err
			}
			f.ID = id
			for _, t := range f.Tokens {
				if _, err := ts.ExecContext(ctx, snapshotID, t, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

const catalogFileCols = "id, snapshot_id, path_bi, parent_bi, path_ct, name_ct, type, size, mtime, mode"

func scanCatalogFiles(rows *sql.Rows) ([]CatalogFile, error) {
	defer rows.Close()
	out := []CatalogFile{}
	for rows.Next() {
		var f CatalogFile
		if err := rows.Scan(&f.ID, &f.SnapshotID, &f.PathBI, &f.ParentBI, &f.PathCT, &f.NameCT, &f.Type, &f.Size, &f.MTime, &f.Mode); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CatalogFileByPath returns the row with the path blind index or
// ErrNotFound.
func (s *Store) CatalogFileByPath(ctx context.Context, snapshotID int64, pathBI []byte) (*CatalogFile, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+catalogFileCols+" FROM snapshot_files WHERE snapshot_id = ? AND path_bi = ?", snapshotID, pathBI)
	if err != nil {
		return nil, err
	}
	list, err := scanCatalogFiles(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// CatalogChildren lists the rows whose parent blind index is parentBI, in
// id (ingestion, i.e. tree) order after afterID.
func (s *Store) CatalogChildren(ctx context.Context, snapshotID int64, parentBI []byte, afterID int64, limit int) ([]CatalogFile, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+catalogFileCols+" FROM snapshot_files WHERE snapshot_id = ? AND parent_bi = ? AND id > ? ORDER BY id LIMIT ?",
		snapshotID, parentBI, afterID, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	return scanCatalogFiles(rows)
}

// CatalogSearchFilter selects rows carrying every token. Type is empty for
// any type; MinSize/MaxSize are ignored when negative.
type CatalogSearchFilter struct {
	SnapshotID int64
	Tokens     [][]byte
	Type       string
	MinSize    int64
	MaxSize    int64
	AfterID    int64
	Limit      int
}

// SearchCatalog returns the rows matching f in id order.
func (s *Store) SearchCatalog(ctx context.Context, f CatalogSearchFilter) ([]CatalogFile, error) {
	if len(f.Tokens) == 0 {
		return []CatalogFile{}, nil
	}
	q := "SELECT " + catalogFileCols + " FROM snapshot_files WHERE snapshot_id = ? AND id > ? AND id IN (" +
		"SELECT file_id FROM snapshot_file_tokens WHERE snapshot_id = ? AND token IN (" +
		strings.TrimSuffix(strings.Repeat("?,", len(f.Tokens)), ",") + ") GROUP BY file_id HAVING COUNT(DISTINCT token) = ?)"
	args := []any{f.SnapshotID, f.AfterID, f.SnapshotID}
	for _, t := range f.Tokens {
		args = append(args, t)
	}
	args = append(args, len(f.Tokens))
	if f.Type != "" {
		q += " AND type = ?"
		args = append(args, f.Type)
	}
	if f.MinSize >= 0 {
		q += " AND size >= ?"
		args = append(args, f.MinSize)
	}
	if f.MaxSize >= 0 {
		q += " AND size <= ?"
		args = append(args, f.MaxSize)
	}
	q += " ORDER BY id LIMIT ?"
	args = append(args, clampLimit(f.Limit))
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanCatalogFiles(rows)
}

// CatalogFilesAfter lists a snapshot's rows in id order after afterID.
func (s *Store) CatalogFilesAfter(ctx context.Context, snapshotID, afterID int64, limit int) ([]CatalogFile, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+catalogFileCols+" FROM snapshot_files WHERE snapshot_id = ? AND id > ? ORDER BY id LIMIT ?",
		snapshotID, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanCatalogFiles(rows)
}
