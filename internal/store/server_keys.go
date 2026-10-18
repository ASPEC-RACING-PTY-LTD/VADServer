// Owner: SRV-K (P1-12). Server key custody rows (backup-format-v1 §4.6,
// table server_keys): the SIK and the versioned SEK, private keys sealed by
// the server KEK. Sealing is done by internal/keys; this file only stores.

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Server key kinds (server_keys.kind).
const (
	ServerKeySIK = "sik"
	ServerKeySEK = "sek"
)

// ServerKey is one row of server_keys. PrivateSealed is the private key
// sealed by the KEK; Signature is the SIK signature over a SEK (nil for the
// SIK). RetiredAt nil marks the current key of its kind.
type ServerKey struct {
	ID            int64
	Kind          string
	Version       uint32
	Public        []byte
	PrivateSealed []byte
	Fingerprint   string
	Signature     []byte
	CreatedAt     int64
	RetiredAt     *int64
}

const serverKeyCols = `id, kind, version, public, private_sealed_by_kek, fingerprint, signature, created_at, retired_at`

func scanServerKey(row interface{ Scan(...any) error }) (*ServerKey, error) {
	var k ServerKey
	var version int64
	err := row.Scan(&k.ID, &k.Kind, &version, &k.Public, &k.PrivateSealed, &k.Fingerprint, &k.Signature, &k.CreatedAt, &k.RetiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.Version = uint32(version)
	return &k, nil
}

// CountServerKeys returns the number of server_keys rows of any kind. A
// non-zero count means a KEK has been used and must be present.
func (s *Store) CountServerKeys(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM server_keys`).Scan(&n)
	return n, err
}

// CurrentServerKey returns the current (not retired) key of kind, or
// ErrNotFound.
func (s *Store) CurrentServerKey(ctx context.Context, kind string) (*ServerKey, error) {
	return scanServerKey(s.DB.QueryRowContext(ctx, `SELECT `+serverKeyCols+` FROM server_keys WHERE kind = ? AND retired_at IS NULL`, kind))
}

// ServerKeyVersion returns the key of kind with version, current or
// retired, or ErrNotFound.
func (s *Store) ServerKeyVersion(ctx context.Context, kind string, version uint32) (*ServerKey, error) {
	return scanServerKey(s.DB.QueryRowContext(ctx, `SELECT `+serverKeyCols+` FROM server_keys WHERE kind = ? AND version = ?`, kind, int64(version)))
}

// ListServerKeys returns every key of kind, oldest version first.
func (s *Store) ListServerKeys(ctx context.Context, kind string) ([]*ServerKey, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+serverKeyCols+` FROM server_keys WHERE kind = ? ORDER BY version`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ServerKey
	for rows.Next() {
		k, err := scanServerKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func validServerKey(k *ServerKey) error {
	if (k.Kind != ServerKeySIK && k.Kind != ServerKeySEK) || k.Version == 0 || len(k.Public) == 0 || len(k.PrivateSealed) == 0 {
		return errors.New("invalid server key")
	}
	return nil
}

func insertServerKey(ctx context.Context, tx *sql.Tx, k *ServerKey, at int64) error {
	k.CreatedAt, k.RetiredAt = at, nil
	res, err := tx.ExecContext(ctx, `INSERT INTO server_keys(kind, version, public, private_sealed_by_kek, fingerprint, signature, created_at)
		VALUES(?,?,?,?,?,?,?)`, k.Kind, int64(k.Version), k.Public, k.PrivateSealed, k.Fingerprint, k.Signature, at)
	if err != nil {
		return err
	}
	k.ID, err = res.LastInsertId()
	return err
}

// InsertServerKeys stores new current keys in one transaction (first start).
// It fails, storing nothing, when a kind already has a current key.
func (s *Store) InsertServerKeys(ctx context.Context, keys ...*ServerKey) error {
	for _, k := range keys {
		if err := validServerKey(k); err != nil {
			return err
		}
	}
	at := now()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if err := insertServerKey(ctx, tx, k, at); err != nil {
				return err
			}
		}
		return nil
	})
}

// RotateServerKey retires the current key of k.Kind and stores k as the new
// current key, atomically. k.Version must be exactly the current version
// plus one; otherwise (a concurrent rotation) it returns ErrConflict.
func (s *Store) RotateServerKey(ctx context.Context, k *ServerKey) error {
	if err := validServerKey(k); err != nil {
		return err
	}
	at := now()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		var cur int64
		err := tx.QueryRowContext(ctx, `SELECT version FROM server_keys WHERE kind = ? AND retired_at IS NULL`, k.Kind).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("rotate %s: no current key: %w", k.Kind, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if int64(k.Version) != cur+1 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE server_keys SET retired_at = ? WHERE kind = ? AND retired_at IS NULL`, at, k.Kind); err != nil {
			return err
		}
		return insertServerKey(ctx, tx, k, at)
	})
}
