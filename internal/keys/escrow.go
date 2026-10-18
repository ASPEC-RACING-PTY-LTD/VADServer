package keys

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Unwrap purposes permitted by backup-format-v1 §4.6 (key_audit.purpose).
const (
	PurposeCatalog       = "catalog_ingest" // (a) catalog ingestion of metadata
	PurposeRewrap        = "rewrap"         // (b) re-wrap to a newly approved device
	PurposeDataOperation = "data_operation" // (c) full-escrow browse, download, verify, compaction
	PurposeRotation      = "rotation"       // (d) rotation
)

// key_audit actions.
const (
	ActionUnwrap    = "unwrap"     // an escrow key file was opened
	ActionOpenSEK   = "open_sek"   // a SEK private key was released to a caller
	ActionRotateSEK = "rotate_sek" // a new SEK version replaced the current one
)

func validPurpose(p string) bool {
	switch p {
	case PurposeCatalog, PurposeRewrap, PurposeDataOperation, PurposeRotation:
		return true
	}
	return false
}

// Audit describes who unwraps and why; it becomes a key_audit row. Optional
// ids are nil when not applicable. Purpose is required for unwraps.
type Audit struct {
	Purpose       string
	ActorUserID   *int64
	ActorDeviceID *int64
	UserID        *int64 // owner of the repository
	RepoID        *int64
	OperationID   *int64
	Details       string
}

// EscrowOpener opens escrow key files in memory. Every call is audited
// before any key material is released; if the audit row cannot be written
// the call fails (fail closed).
type EscrowOpener interface {
	OpenKeyFile(ctx context.Context, kf *vdcrypt.KeyFile, a Audit) (vdcrypt.Bundle, error)
}

var _ EscrowOpener = (*Manager)(nil)

func (m *Manager) audit(ctx context.Context, action, kind string, version uint32, a Audit, epoch *int64) error {
	v := int64(version)
	return m.st.AppendKeyAudit(ctx, &store.KeyAudit{
		Action: action, Purpose: a.Purpose, ActorUserID: a.ActorUserID, ActorDeviceID: a.ActorDeviceID,
		UserID: a.UserID, RepoID: a.RepoID, Epoch: epoch, KeyKind: kind, KeyVersion: &v,
		OperationID: a.OperationID, Details: a.Details,
	})
}

// OpenSEK returns the private key of SEK version (current or retired) after
// auditing the release. The caller wipes it as soon as possible.
func (m *Manager) OpenSEK(ctx context.Context, version uint32, a Audit) (vdcrypt.Key, error) {
	if m == nil {
		return vdcrypt.Key{}, ErrUnavailable
	}
	if !validPurpose(a.Purpose) {
		return vdcrypt.Key{}, ErrPurpose
	}
	k, err := m.st.ServerKeyVersion(ctx, store.ServerKeySEK, version)
	if err != nil {
		return vdcrypt.Key{}, err
	}
	if err := m.audit(ctx, ActionOpenSEK, store.ServerKeySEK, version, a, nil); err != nil {
		return vdcrypt.Key{}, fmt.Errorf("key audit: %w", err)
	}
	return m.unsealX25519(k)
}

// OpenKeyFile unwraps an escrow key file (full or meta bundle) with the SEK
// version it names, after checking that it names this server's SIK and a
// SEK the server holds, and after writing the key_audit row.
func (m *Manager) OpenKeyFile(ctx context.Context, kf *vdcrypt.KeyFile, a Audit) (vdcrypt.Bundle, error) {
	if m == nil {
		return vdcrypt.Bundle{}, ErrUnavailable
	}
	if !validPurpose(a.Purpose) {
		return vdcrypt.Bundle{}, ErrPurpose
	}
	if kf == nil || kf.Kind != vdcrypt.KindEscrow || kf.Server == nil {
		return vdcrypt.Bundle{}, vdcrypt.ErrKeyFile
	}
	if err := kf.VerifyEscrow(m.sikPub); err != nil {
		return vdcrypt.Bundle{}, ErrWrongServer
	}
	k, err := m.st.ServerKeyVersion(ctx, store.ServerKeySEK, kf.Server.SEKVersion)
	if err != nil {
		return vdcrypt.Bundle{}, ErrWrongServer
	}
	want, err := vdcrypt.KeyFromBytes(k.Public)
	got, gerr := vdcrypt.KeyFromBytes(kf.RecipientPub)
	if err != nil || gerr != nil || !want.Equal(got) {
		return vdcrypt.Bundle{}, ErrWrongServer
	}
	epoch := int64(kf.Epoch)
	if err := m.audit(ctx, ActionUnwrap, store.ServerKeySEK, k.Version, a, &epoch); err != nil {
		return vdcrypt.Bundle{}, fmt.Errorf("key audit: %w", err)
	}
	priv, err := m.unsealX25519(k)
	if err != nil {
		return vdcrypt.Bundle{}, err
	}
	defer priv.Wipe()
	b, err := kf.Open(priv)
	if err != nil {
		slog.Warn("escrow key file did not open", "key_id", kf.KeyID, "sek_version", k.Version, "err", err)
		return vdcrypt.Bundle{}, err
	}
	return b, nil
}
