package store

import (
	"context"
	"database/sql"
	"errors"
)

// APIKey is an administrator's key for the read-only diagnostics API.
// Only a hash of the key is stored; Prefix identifies it in lists.
type APIKey struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	Scope      string `json:"scope"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	LastUsedAt *int64 `json:"last_used_at,omitempty"`
	LastUsedIP string `json:"last_used_ip,omitempty"`
	RevokedAt  *int64 `json:"revoked_at,omitempty"`
}

const apiKeyCols = "id, user_id, name, prefix, scope, created_at, expires_at, last_used_at, last_used_ip, revoked_at"

func scanAPIKey(sc interface{ Scan(...any) error }) (*APIKey, error) {
	var k APIKey
	err := sc.Scan(&k.ID, &k.UserID, &k.Name, &k.Prefix, &k.Scope, &k.CreatedAt, &k.ExpiresAt, &k.LastUsedAt, &k.LastUsedIP, &k.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &k, err
}

func (s *Store) CreateAPIKey(ctx context.Context, k APIKey, hash string) (*APIKey, error) {
	k.CreatedAt = now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO api_keys(user_id, name, prefix, hash, scope, created_at, expires_at) VALUES (?,?,?,?,?,?,?)",
		k.UserID, k.Name, k.Prefix, hash, k.Scope, k.CreatedAt, k.ExpiresAt)
	if err != nil {
		return nil, err
	}
	k.ID, _ = res.LastInsertId()
	return &k, nil
}

func (s *Store) APIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+apiKeyCols+" FROM api_keys ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// APIKeyByHash returns a usable key (not revoked, not expired).
func (s *Store) APIKeyByHash(ctx context.Context, hash string) (*APIKey, error) {
	return scanAPIKey(s.DB.QueryRowContext(ctx, "SELECT "+apiKeyCols+" FROM api_keys WHERE hash = ? AND revoked_at IS NULL AND expires_at > ?", hash, now()))
}

func (s *Store) TouchAPIKey(ctx context.Context, id int64, ip string) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ?, last_used_ip = ? WHERE id = ?", now(), ip, id)
}

func (s *Store) RevokeAPIKey(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL", now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
