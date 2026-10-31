package store

// Remote storage nodes: pairing secrets an administrator hands to a node's
// owner, the per-node key pair whose private key never leaves this server,
// the files whose content lives only on a node (remote_blobs) and the
// users whose files a node holds (upload origin).

import (
	"context"
	"database/sql"
	"errors"
)

// NodePairing is a single-use secret that pairs (auto-approves) a node.
type NodePairing struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	PublicURL string `json:"public_url"`
	CreatedBy *int64 `json:"created_by,omitempty"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
	UsedAt    *int64 `json:"used_at,omitempty"`
	NodeID    *int64 `json:"node_id,omitempty"`
}

func (s *Store) CreateNodePairing(ctx context.Context, p NodePairing, codeHash string) (*NodePairing, error) {
	p.CreatedAt = now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO node_pairings(name, code_hash, public_url, created_by, created_at, expires_at) VALUES (?,?,?,?,?,?)",
		p.Name, codeHash, p.PublicURL, p.CreatedBy, p.CreatedAt, p.ExpiresAt)
	if err != nil {
		return nil, err
	}
	p.ID, _ = res.LastInsertId()
	return &p, nil
}

func (s *Store) NodePairings(ctx context.Context) ([]NodePairing, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, public_url, created_by, created_at, expires_at, used_at, node_id FROM node_pairings ORDER BY created_at DESC LIMIT 200")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodePairing{}
	for rows.Next() {
		var p NodePairing
		if err := rows.Scan(&p.ID, &p.Name, &p.PublicURL, &p.CreatedBy, &p.CreatedAt, &p.ExpiresAt, &p.UsedAt, &p.NodeID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteNodePairing(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM node_pairings WHERE id = ? AND used_at IS NULL", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UseNodePairing consumes an unused, unexpired pairing secret for a node
// (once only) and returns it.
func (s *Store) UseNodePairing(ctx context.Context, codeHash string, nodeID int64) (*NodePairing, error) {
	var p NodePairing
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		err := tx.QueryRowContext(ctx, "SELECT id, name, public_url, created_by, created_at, expires_at FROM node_pairings WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?", codeHash, t).
			Scan(&p.ID, &p.Name, &p.PublicURL, &p.CreatedBy, &p.CreatedAt, &p.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE node_pairings SET used_at = ?, node_id = ? WHERE id = ?", t, nodeID, p.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE storage_nodes SET paired = 1 WHERE id = ?", nodeID)
		p.UsedAt, p.NodeID = &t, &nodeID
		return err
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// NodeOriginKey is a node's key pair for remote files. The private key is
// sealed (node_keys.go) and never leaves this server.
type NodeOriginKey struct {
	KeyID         string
	PublicKey     string // base64 PKIX (P-256)
	PrivateSealed string
}

func (s *Store) NodeOriginKey(ctx context.Context, nodeID int64) (*NodeOriginKey, error) {
	var k NodeOriginKey
	err := s.DB.QueryRowContext(ctx, "SELECT origin_key_id, origin_public_key, origin_private_key FROM storage_nodes WHERE id = ?", nodeID).Scan(&k.KeyID, &k.PublicKey, &k.PrivateSealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &k, err
}

// SetNodeOriginKey stores a node's key pair unless it already has one.
func (s *Store) SetNodeOriginKey(ctx context.Context, nodeID int64, k NodeOriginKey) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE storage_nodes SET origin_key_id = ?, origin_public_key = ?, origin_private_key = ? WHERE id = ? AND origin_key_id = ''",
		k.KeyID, k.PublicKey, k.PrivateSealed, nodeID)
	return err
}

// --- upload origin ---

// UploadOrigin returns the node holding a user's files (nil: this server).
func (s *Store) UploadOrigin(ctx context.Context, userID int64) (*int64, error) {
	var id sql.NullInt64
	err := s.DB.QueryRowContext(ctx, "SELECT upload_origin_node_id FROM users WHERE id = ?", userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return nullInt(id), err
}

func (s *Store) SetUploadOrigin(ctx context.Context, userID int64, nodeID *int64) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE users SET upload_origin_node_id = ? WHERE id = ?", nodeID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UsersOfNode lists the users whose upload origin is a node.
func (s *Store) UsersOfNode(ctx context.Context, nodeID int64) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM users WHERE upload_origin_node_id = ? ORDER BY id", nodeID)
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

// --- remote blobs ---

// RemoteBlob is a file content held by a node (encrypted with the node's
// public key; this server alone can decrypt it). While State is "moving"
// the local file still exists; once "ok" it has been removed here.
type RemoteBlob struct {
	UserID       int64  `json:"user_id"`
	SHA256       string `json:"sha256"`
	NodeID       int64  `json:"node_id"`
	LocationID   int64  `json:"location_id"` // the node location (node_locations.id)
	KeyID        string `json:"key_id"`
	PlainSize    int64  `json:"plain_size"`
	CipherSHA256 string `json:"cipher_sha256"`
	CipherSize   int64  `json:"cipher_size"`
	State        string `json:"state"` // moving, ok, missing, deleting
	Attempts     int    `json:"attempts"`
	LastError    string `json:"last_error,omitempty"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	VerifiedAt   *int64 `json:"verified_at,omitempty"`
}

const remoteBlobCols = "user_id, sha256, node_id, location_id, key_id, plain_size, cipher_sha256, cipher_size, state, attempts, last_error, created_at, updated_at, verified_at"

func scanRemoteBlob(sc interface{ Scan(...any) error }) (*RemoteBlob, error) {
	var b RemoteBlob
	err := sc.Scan(&b.UserID, &b.SHA256, &b.NodeID, &b.LocationID, &b.KeyID, &b.PlainSize, &b.CipherSHA256, &b.CipherSize, &b.State, &b.Attempts, &b.LastError, &b.CreatedAt, &b.UpdatedAt, &b.VerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

func (s *Store) RemoteBlob(ctx context.Context, userID int64, sha string) (*RemoteBlob, error) {
	return scanRemoteBlob(s.DB.QueryRowContext(ctx, "SELECT "+remoteBlobCols+" FROM remote_blobs WHERE user_id = ? AND sha256 = ?", userID, sha))
}

// StartRemoteBlob records a move of a blob to a node (state moving).
func (s *Store) StartRemoteBlob(ctx context.Context, b RemoteBlob) error {
	t := now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO remote_blobs(user_id, sha256, node_id, location_id, key_id, plain_size, state, created_at, updated_at)
		VALUES (?,?,?,?,?,?,'moving',?,?) ON CONFLICT(user_id, sha256) DO UPDATE SET node_id = excluded.node_id, location_id = excluded.location_id,
		key_id = excluded.key_id, plain_size = excluded.plain_size, state = 'moving', last_error = '', updated_at = excluded.updated_at
		WHERE remote_blobs.state != 'ok'`, b.UserID, b.SHA256, b.NodeID, b.LocationID, b.KeyID, b.PlainSize, t, t)
	return err
}

// FinishRemoteBlob marks a moved blob as held by the node (its local file
// is then removed by the caller).
func (s *Store) FinishRemoteBlob(ctx context.Context, userID int64, sha, cipherSHA string, cipherSize int64) error {
	t := now()
	_, err := s.DB.ExecContext(ctx, "UPDATE remote_blobs SET state = 'ok', cipher_sha256 = ?, cipher_size = ?, last_error = '', updated_at = ?, verified_at = ? WHERE user_id = ? AND sha256 = ?",
		cipherSHA, cipherSize, t, t, userID, sha)
	return err
}

// FailRemoteBlob records a failed move attempt (the local file is kept).
func (s *Store) FailRemoteBlob(ctx context.Context, userID int64, sha, msg string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE remote_blobs SET attempts = attempts + 1, last_error = ?, updated_at = ? WHERE user_id = ? AND sha256 = ? AND state = 'moving'", msg, now(), userID, sha)
	return err
}

// SetRemoteBlobState changes a remote blob's state.
func (s *Store) SetRemoteBlobState(ctx context.Context, userID int64, sha, state, msg string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE remote_blobs SET state = ?, last_error = ?, updated_at = ? WHERE user_id = ? AND sha256 = ?", state, msg, now(), userID, sha)
	return err
}

func (s *Store) DeleteRemoteBlob(ctx context.Context, userID int64, sha string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM remote_blobs WHERE user_id = ? AND sha256 = ?", userID, sha)
	return err
}

// BlobsToMove lists blobs of a user still stored here (not on a node, or a
// move that failed fewer than maxAttempts times), largest first.
func (s *Store) BlobsToMove(ctx context.Context, userID int64, maxAttempts, limit int) ([]BlobRef, []int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.sha256, b.location_id, b.size FROM blobs b LEFT JOIN remote_blobs r ON r.user_id = b.user_id AND r.sha256 = b.sha256
		WHERE b.user_id = ? AND b.refcount > 0 AND (r.sha256 IS NULL OR (r.state = 'moving' AND r.attempts < ?)) ORDER BY b.size DESC LIMIT ?`, userID, maxAttempts, limit)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var refs []BlobRef
	var sizes []int64
	for rows.Next() {
		r := BlobRef{UserID: userID}
		var size int64
		if err := rows.Scan(&r.SHA256, &r.LocationID, &size); err != nil {
			return nil, nil, err
		}
		refs, sizes = append(refs, r), append(sizes, size)
	}
	return refs, sizes, rows.Err()
}

// RemoteBlobsOfNode lists a node's remote blobs in a state.
func (s *Store) RemoteBlobsOfNode(ctx context.Context, nodeID int64, state string, limit int) ([]RemoteBlob, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+remoteBlobCols+" FROM remote_blobs WHERE node_id = ? AND state = ? ORDER BY updated_at LIMIT ?", nodeID, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RemoteBlob{}
	for rows.Next() {
		b, err := scanRemoteBlob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// RemoteStats counts a node's remote blobs and bytes by state, and how much
// of its assigned users' content is still stored here.
type RemoteStats struct {
	Files      map[string]int64 `json:"files"`
	Bytes      map[string]int64 `json:"bytes"`
	LocalFiles int64            `json:"local_files"` // assigned users' files not moved yet
	LocalBytes int64            `json:"local_bytes"`
}

func (s *Store) RemoteStatsOfNode(ctx context.Context, nodeID int64) (*RemoteStats, error) {
	st := &RemoteStats{Files: map[string]int64{}, Bytes: map[string]int64{}}
	rows, err := s.DB.QueryContext(ctx, "SELECT state, COUNT(*), COALESCE(SUM(plain_size), 0) FROM remote_blobs WHERE node_id = ? GROUP BY state", nodeID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var state string
		var n, b int64
		if err := rows.Scan(&state, &n, &b); err != nil {
			rows.Close()
			return nil, err
		}
		st.Files[state], st.Bytes[state] = n, b
	}
	rows.Close()
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(b.size), 0) FROM blobs b JOIN users u ON u.id = b.user_id AND u.upload_origin_node_id = ?
		LEFT JOIN remote_blobs r ON r.user_id = b.user_id AND r.sha256 = b.sha256 WHERE b.refcount > 0 AND (r.sha256 IS NULL OR r.state != 'ok')`, nodeID).Scan(&st.LocalFiles, &st.LocalBytes)
	return st, err
}

// NodePairingUsable reports whether a pairing secret exists, is unused and
// has not expired (without using it).
func (s *Store) NodePairingUsable(ctx context.Context, codeHash string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM node_pairings WHERE code_hash = ? AND used_at IS NULL AND expires_at > ?", codeHash, now()).Scan(&n)
	return n > 0, err
}

// StorageNodeReadKey is the key that signs this server's reads from a node:
// the stored SHA-256 (hex) of the node secret, which the node also derives.
func (s *Store) StorageNodeReadKey(ctx context.Context, nodeID int64) ([]byte, error) {
	var h string
	err := s.DB.QueryRowContext(ctx, "SELECT secret_hash FROM storage_nodes WHERE id = ? AND status = 'approved'", nodeID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) || h == "" {
		return nil, ErrNotFound
	}
	return []byte(h), err
}

// StorageNodePaired reports whether a node joined with a pairing secret.
func (s *Store) StorageNodePaired(ctx context.Context, nodeID int64) bool {
	var p bool
	_ = s.DB.QueryRowContext(ctx, "SELECT paired FROM storage_nodes WHERE id = ?", nodeID).Scan(&p)
	return p
}
