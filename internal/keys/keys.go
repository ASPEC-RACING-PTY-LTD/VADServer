// Package keys holds the server key custody of backup-format-v1 §4.6: the
// server KEK (a 32-byte file outside the database), the SIK (Ed25519 server
// identity) and the versioned SEK (X25519 server escrow key). Private keys
// are stored only sealed by the KEK; the SEK public key of every version is
// signed by the SIK. Every server-side unwrap is recorded in key_audit
// before the key material is released (MASTER_PLAN §7.2).
//
// Owner: SRV-K (P1-12).
package keys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// KEKSize is the size of the server KEK file.
const KEKSize = 32

var (
	// ErrUnavailable means the server runs without a KEK: escrow and key
	// routes answer 409 escrow_unavailable (api-v1 §1.1).
	ErrUnavailable = errors.New("keys: server key custody unavailable (no KEK)")
	// ErrKEKMismatch means the KEK does not open the stored server keys.
	ErrKEKMismatch = errors.New("keys: the server KEK does not open the stored server keys")
	// ErrWrongServer means an escrow key file names a SIK or SEK this server
	// does not hold.
	ErrWrongServer = errors.New("keys: key file is not wrapped to this server's escrow key")
	// ErrPurpose means an audit record has no permitted purpose.
	ErrPurpose = errors.New("keys: unwrap purpose not permitted")
)

// Fingerprint is the SIK fingerprint of api-v1 §14 Rev 1.1b: uppercase hex
// of the first 16 bytes of SHA-256(sik_pub), in 8 groups of 4 joined by "-".
func Fingerprint(sikPub []byte) string {
	sum := sha256.Sum256(sikPub)
	h := strings.ToUpper(hex.EncodeToString(sum[:16]))
	groups := make([]string, 0, 8)
	for i := 0; i < len(h); i += 4 {
		groups = append(groups, h[i:i+4])
	}
	return strings.Join(groups, "-")
}

// Signer signs with the server identity key (SIK).
type Signer interface {
	SIKPublic() ed25519.PublicKey
	Sign(msg []byte) ([]byte, error)
}

// Manager is the loaded key custody. A nil *Manager is valid and means
// custody is unavailable: every method returns ErrUnavailable.
type Manager struct {
	st  *store.Store
	kek vdcrypt.Key

	mu      sync.RWMutex
	sikPriv ed25519.PrivateKey
	sikPub  ed25519.PublicKey
	sekPub  vdcrypt.Key
	sekVer  uint32
	sekSig  []byte
}

var _ Signer = (*Manager)(nil)

// Load reads the KEK at cfg.KEKFile and opens (or, on first start, creates)
// the SIK and SEK. Startup rules:
//   - KEK missing and server_keys has rows: error (refuse to start);
//   - KEK missing, no rows, dev mode or a test binary: a KEK is generated;
//   - KEK missing otherwise: (nil, nil), custody unavailable (degraded);
//   - KEK present but not opening the stored keys: error.
func Load(ctx context.Context, st *store.Store, cfg *config.Config) (*Manager, error) {
	n, err := st.CountServerKeys(ctx)
	if err != nil {
		return nil, err
	}
	kek, err := readKEK(cfg.KEKFile)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist) && n > 0:
		return nil, fmt.Errorf("server KEK %q is missing but the database holds %d sealed server keys; restore the KEK file (VAULTDOCK_KEK_FILE) and restart", cfg.KEKFile, n)
	case errors.Is(err, fs.ErrNotExist) && cfg.KEKFile != "" && (cfg.DevMode || testing.Testing()):
		if kek, err = generateKEK(cfg.KEKFile); err != nil {
			return nil, err
		}
		slog.Warn("generated a server KEK (development mode); back it up, losing it makes server escrow unusable", "path", cfg.KEKFile)
	case errors.Is(err, fs.ErrNotExist):
		slog.Warn("server KEK not found; server escrow and key routes are unavailable until it is created (install.sh)", "path", cfg.KEKFile)
		return nil, nil
	default:
		return nil, err
	}
	m := &Manager{st: st, kek: kek}
	if err := m.init(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

// New returns a manager over an explicit KEK (tests and tools). It opens or
// creates the server keys like Load.
func New(ctx context.Context, st *store.Store, kek vdcrypt.Key) (*Manager, error) {
	m := &Manager{st: st, kek: kek}
	if err := m.init(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func readKEK(path string) (vdcrypt.Key, error) {
	if path == "" {
		return vdcrypt.Key{}, fs.ErrNotExist
	}
	fi, err := os.Stat(path)
	if err != nil {
		return vdcrypt.Key{}, err
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		// Docker secrets are often 0444; warn rather than refuse.
		slog.Warn("server KEK file is readable by group or others; restrict it to mode 0600", "path", path, "mode", fi.Mode().Perm().String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return vdcrypt.Key{}, err
	}
	defer clear(b)
	if len(b) != KEKSize {
		return vdcrypt.Key{}, fmt.Errorf("server KEK %q must be exactly %d bytes, found %d", path, KEKSize, len(b))
	}
	return vdcrypt.KeyFromBytes(b)
}

func generateKEK(path string) (vdcrypt.Key, error) {
	k, err := vdcrypt.RandomKey(rand.Reader)
	if err != nil {
		return k, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return vdcrypt.Key{}, fmt.Errorf("server KEK dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return vdcrypt.Key{}, fmt.Errorf("server KEK: %w", err)
	}
	if _, err := f.Write(k[:]); err != nil {
		f.Close()
		os.Remove(path)
		return vdcrypt.Key{}, fmt.Errorf("server KEK: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return vdcrypt.Key{}, fmt.Errorf("server KEK: %w", err)
	}
	return k, nil
}

// init opens the current SIK and SEK, verifying that the KEK opens them and
// that the SEK signature is valid, or creates them on first start.
func (m *Manager) init(ctx context.Context) error {
	sik, err := m.st.CurrentServerKey(ctx, store.ServerKeySIK)
	if errors.Is(err, store.ErrNotFound) {
		if err := m.createSIK(ctx); err != nil {
			return err
		}
		sik, err = m.st.CurrentServerKey(ctx, store.ServerKeySIK)
	}
	if err != nil {
		return err
	}
	seed, err := m.unseal(sik)
	if err != nil {
		return err
	}
	if len(seed) != ed25519.SeedSize {
		clear(seed)
		return ErrKEKMismatch
	}
	priv := ed25519.NewKeyFromSeed(seed)
	clear(seed)
	pub := priv.Public().(ed25519.PublicKey)
	if !pub.Equal(ed25519.PublicKey(sik.Public)) {
		return ErrKEKMismatch
	}
	m.sikPriv, m.sikPub = priv, pub

	sek, err := m.st.CurrentServerKey(ctx, store.ServerKeySEK)
	if errors.Is(err, store.ErrNotFound) {
		if err := m.createSEK(ctx, 1, false); err != nil {
			return err
		}
		sek, err = m.st.CurrentServerKey(ctx, store.ServerKeySEK)
	}
	if err != nil {
		return err
	}
	if err := m.checkSEK(sek); err != nil {
		return err
	}
	m.setSEK(sek)
	return nil
}

func (m *Manager) checkSEK(sek *store.ServerKey) error {
	priv, err := m.unsealX25519(sek)
	if err != nil {
		return err
	}
	priv.Wipe()
	pub, _ := vdcrypt.KeyFromBytes(sek.Public)
	if !vdcrypt.VerifySEK(m.sikPub, pub, sek.Version, sek.Signature) {
		return fmt.Errorf("keys: SEK version %d is not signed by the current SIK", sek.Version)
	}
	return nil
}

func (m *Manager) setSEK(sek *store.ServerKey) {
	pub, _ := vdcrypt.KeyFromBytes(sek.Public)
	m.sekPub, m.sekVer, m.sekSig = pub, sek.Version, append([]byte(nil), sek.Signature...)
}

func (m *Manager) createSIK(ctx context.Context) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	seed := priv.Seed()
	defer clear(seed)
	defer clear(priv)
	sealed, err := seal(m.kek, store.ServerKeySIK, 1, seed)
	if err != nil {
		return err
	}
	err = m.st.InsertServerKeys(ctx, &store.ServerKey{Kind: store.ServerKeySIK, Version: 1, Public: pub, PrivateSealed: sealed, Fingerprint: Fingerprint(pub)})
	if err == nil {
		slog.Info("created server identity key", "sik_fingerprint", Fingerprint(pub))
	}
	return err
}

// createSEK generates SEK version, signs it with the SIK and stores it, as
// the first SEK or (rotate) replacing the current one atomically.
func (m *Manager) createSEK(ctx context.Context, version uint32, rotate bool) error {
	priv, pub, err := vdcrypt.GenerateX25519(rand.Reader)
	if err != nil {
		return err
	}
	defer priv.Wipe()
	sealed, err := seal(m.kek, store.ServerKeySEK, version, priv[:])
	if err != nil {
		return err
	}
	k := &store.ServerKey{Kind: store.ServerKeySEK, Version: version, Public: pub[:], PrivateSealed: sealed,
		Signature: vdcrypt.SignSEK(m.sikPriv, pub, version)}
	if rotate {
		return m.st.RotateServerKey(ctx, k)
	}
	return m.st.InsertServerKeys(ctx, k)
}

func (m *Manager) unseal(k *store.ServerKey) ([]byte, error) {
	b, err := open(m.kek, k.Kind, k.Version, k.PrivateSealed)
	if err != nil {
		return nil, ErrKEKMismatch
	}
	return b, nil
}

func (m *Manager) unsealX25519(k *store.ServerKey) (vdcrypt.Key, error) {
	b, err := m.unseal(k)
	if err != nil {
		return vdcrypt.Key{}, err
	}
	defer clear(b)
	priv, err := vdcrypt.KeyFromBytes(b)
	if err != nil {
		return vdcrypt.Key{}, ErrKEKMismatch
	}
	pub, err := vdcrypt.X25519Public(priv)
	want, werr := vdcrypt.KeyFromBytes(k.Public)
	if err != nil || werr != nil || !pub.Equal(want) {
		priv.Wipe()
		return vdcrypt.Key{}, ErrKEKMismatch
	}
	return priv, nil
}

// Available reports whether key custody is loaded.
func (m *Manager) Available() bool { return m != nil }

// SIKPublic returns the SIK public key (nil when unavailable).
func (m *Manager) SIKPublic() ed25519.PublicKey {
	if m == nil {
		return nil
	}
	return append(ed25519.PublicKey(nil), m.sikPub...)
}

// Sign signs msg with the SIK.
func (m *Manager) Sign(msg []byte) ([]byte, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	return ed25519.Sign(m.sikPriv, msg), nil
}

// Identity returns the current server identity (api-v1 §5A), as served by
// GET /keys/server and the enrolment poll.
func (m *Manager) Identity() (wire.ServerIdentity, error) {
	if m == nil {
		return wire.ServerIdentity{}, ErrUnavailable
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	enc := base64.StdEncoding.EncodeToString
	return wire.ServerIdentity{
		SIKPub: enc(m.sikPub), SIKFingerprint: Fingerprint(m.sikPub),
		SEKPub: enc(m.sekPub[:]), SEKVersion: m.sekVer, SEKSig: enc(m.sekSig),
	}, nil
}

// CurrentSEK returns the current SEK public key and version, and the escrow
// key file "server" member for wrapping to it (backup-format §4.3).
func (m *Manager) CurrentSEK() (pub vdcrypt.Key, server vdcrypt.KeyFileServer, err error) {
	if m == nil {
		return vdcrypt.Key{}, vdcrypt.KeyFileServer{}, ErrUnavailable
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sekPub, vdcrypt.KeyFileServer{SIKPub: append([]byte(nil), m.sikPub...), SEKSig: append([]byte(nil), m.sekSig...), SEKVersion: m.sekVer}, nil
}

// SEKPublic returns the public key and SIK signature of SEK version (current
// or retired), or store.ErrNotFound.
func (m *Manager) SEKPublic(ctx context.Context, version uint32) (vdcrypt.Key, []byte, error) {
	if m == nil {
		return vdcrypt.Key{}, nil, ErrUnavailable
	}
	k, err := m.st.ServerKeyVersion(ctx, store.ServerKeySEK, version)
	if err != nil {
		return vdcrypt.Key{}, nil, err
	}
	pub, err := vdcrypt.KeyFromBytes(k.Public)
	return pub, k.Signature, err
}

// RotateSEK replaces the current SEK with a new SIK-signed version (api-v1
// §14 Rev 1.1b; the SIK is not rotated). Retired versions stay openable. The
// rotation is recorded in key_audit. It returns the new version.
func (m *Manager) RotateSEK(ctx context.Context, a Audit) (uint32, error) {
	if m == nil {
		return 0, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.sekVer + 1
	if err := m.createSEK(ctx, next, true); err != nil {
		return 0, err
	}
	sek, err := m.st.CurrentServerKey(ctx, store.ServerKeySEK)
	if err != nil {
		return 0, err
	}
	m.setSEK(sek)
	a.Purpose = PurposeRotation
	if err := m.audit(ctx, ActionRotateSEK, store.ServerKeySEK, next, a, nil); err != nil {
		// The rotation is committed; the missing audit row is logged loudly.
		slog.Error("key audit of SEK rotation failed", "sek_version", next, "err", err)
	}
	slog.Info("rotated server escrow key", "sek_version", next)
	return next, nil
}

// KEK returns a copy of the server KEK for the one-time superuser export
// (GET /admin/keys/kek). Nil manager: ErrUnavailable.
func (m *Manager) KEK() (vdcrypt.Key, error) {
	if m == nil {
		return vdcrypt.Key{}, ErrUnavailable
	}
	return m.kek, nil
}

// Derive returns a 32-byte key for label derived from the KEK
// (HMAC-SHA-256), for data the server encrypts with a key that must survive
// restarts and is backed up with the KEK (copies stored on nodes).
func (m *Manager) Derive(label string) ([]byte, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	mac := hmac.New(sha256.New, m.kek[:])
	mac.Write([]byte("vaultdock derive v1\x00" + label))
	return mac.Sum(nil), nil
}
