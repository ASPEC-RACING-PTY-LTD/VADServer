package catalog

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Token rules of the blind index (C3: whole-word and prefix search only).
const (
	maxTokenRunes    = 32 // longer tokens are cut; queries are cut the same way
	minPrefixRunes   = 2  // shorter prefixes are not indexed (a 1-rune token is indexed whole)
	maxTokensPerName = 96
	tokenSize        = 16 // bytes of HMAC kept per token
)

// kekIDPrefix names DEKs wrapped under a SEK-derived wrapping key; the
// suffix is the SEK version.
const kekIDPrefix = "sek:"

// userKeys are the per-user catalog keys derived from the DEK.
type userKeys struct {
	enc vdcrypt.Key // XChaCha20-Poly1305 for path/name ciphertexts
	bi  vdcrypt.Key // HMAC-SHA256 blind index
}

func (k *userKeys) wipe() { k.enc.Wipe(); k.bi.Wipe() }

func derive(secret []byte, info string) (vdcrypt.Key, error) {
	b, err := hkdf.Key(sha256.New, secret, []byte("vaultdock.catalog/1"), info, vdcrypt.KeySize)
	if err != nil {
		return vdcrypt.Key{}, err
	}
	k, err := vdcrypt.KeyFromBytes(b)
	clear(b)
	return k, err
}

func newUserKeys(dek vdcrypt.Key) (*userKeys, error) {
	enc, err := derive(dek[:], "enc")
	if err != nil {
		return nil, err
	}
	bi, err := derive(dek[:], "blind-index")
	if err != nil {
		return nil, err
	}
	return &userKeys{enc: enc, bi: bi}, nil
}

func fieldAD(snapshotID int64, field string) []byte {
	return fmt.Appendf(nil, "vaultdock.catalog/1:%d:%s", snapshotID, field)
}

func sealWith(key vdcrypt.Key, plaintext, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return aead.Seal(out, out, plaintext, ad), nil
}

func openWith(key vdcrypt.Key, sealed, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	n := chacha20poly1305.NonceSizeX
	if len(sealed) < n+aead.Overhead() {
		return nil, errors.New("catalog: ciphertext too short")
	}
	return aead.Open(nil, sealed[:n], sealed[n:], ad)
}

// sealField encrypts a path or name of a snapshot's catalog row; the AD
// binds it to the snapshot and the column.
func (k *userKeys) sealField(snapshotID int64, field, v string) ([]byte, error) {
	return sealWith(k.enc, []byte(v), fieldAD(snapshotID, field))
}

func (k *userKeys) openField(snapshotID int64, field string, ct []byte) (string, error) {
	b, err := openWith(k.enc, ct, fieldAD(snapshotID, field))
	if err != nil {
		return "", fmt.Errorf("catalog: open %s: %w", field, err)
	}
	return string(b), nil
}

func (k *userKeys) mac(prefix, v string) []byte {
	m := hmac.New(sha256.New, k.bi[:])
	m.Write([]byte(prefix))
	m.Write([]byte(v))
	return m.Sum(nil)
}

// pathBI is the blind index of a full token path; "" is the snapshot top
// level (the parent of the backed-up roots).
func (k *userKeys) pathBI(path string) []byte { return k.mac("p\x00", path) }

func (k *userKeys) tokenBI(tok string) []byte { return k.mac("t\x00", tok)[:tokenSize] }

// splitWords lowercases s and splits it into runs of letters and digits,
// each cut to maxTokenRunes.
func splitWords(s string) []string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, w := range words {
		if r := []rune(w); len(r) > maxTokenRunes {
			words[i] = string(r[:maxTokenRunes])
		}
	}
	return words
}

// nameTokens returns the blind tokens of a name: every word, and every
// prefix of at least minPrefixRunes of it, deduplicated.
func (k *userKeys) nameTokens(name string) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	add := func(t string) {
		if !seen[t] && len(out) < maxTokensPerName {
			seen[t] = true
			out = append(out, k.tokenBI(t))
		}
	}
	for _, w := range splitWords(name) {
		r := []rune(w)
		add(w)
		for n := minPrefixRunes; n < len(r); n++ {
			add(string(r[:n]))
		}
	}
	return out
}

// queryTokens returns the distinct blind tokens a search query must all
// match. A one-rune word matches only one-rune names words.
func (k *userKeys) queryTokens(q string) [][]byte {
	seen := map[string]bool{}
	var out [][]byte
	for _, w := range splitWords(q) {
		if !seen[w] {
			seen[w] = true
			out = append(out, k.tokenBI(w))
		}
	}
	return out
}

// --- DEK custody ---

// wrapKey returns the key that wraps catalog DEKs under SEK version v. It is
// derived from the SEK private key, which only server key custody (sealed
// by the KEK) can open; the open is audited with purpose catalog. Derived
// keys are cached for the life of the process.
func (s *Service) wrapKey(ctx context.Context, m *keys.Manager, version uint32) (vdcrypt.Key, error) {
	s.wrapMu.Lock()
	defer s.wrapMu.Unlock()
	if k, ok := s.wrapKeys[version]; ok {
		return k, nil
	}
	sek, err := m.OpenSEK(ctx, version, keys.Audit{Purpose: keys.PurposeCatalog, Details: "catalog DEK wrapping key"})
	if err != nil {
		return vdcrypt.Key{}, err
	}
	defer sek.Wipe()
	k, err := derive(sek[:], "dek-wrap")
	if err != nil {
		return vdcrypt.Key{}, err
	}
	s.wrapKeys[version] = k
	return k, nil
}

func dekAD(userID int64, kekID string) []byte {
	return fmt.Appendf(nil, "vaultdock.catalog-dek/1:%d:%s", userID, kekID)
}

// userKeysFor returns the catalog keys of a user, creating and storing the
// DEK on first use when create is set. ErrNoCatalogKey means the user has
// none yet (nothing was ingested).
func (s *Service) userKeysFor(ctx context.Context, userID int64, create bool) (*userKeys, error) {
	m := s.keys()
	if m == nil {
		return nil, keys.ErrUnavailable
	}
	row, err := s.st.CatalogKey(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		if !create {
			return nil, ErrNoCatalogKey
		}
		if row, err = s.createDEK(ctx, m, userID); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	v, ok := strings.CutPrefix(row.KEKID, kekIDPrefix)
	ver, perr := strconv.ParseUint(v, 10, 32)
	if !ok || perr != nil {
		return nil, fmt.Errorf("catalog: unknown kek_id %q", row.KEKID)
	}
	wk, err := s.wrapKey(ctx, m, uint32(ver))
	if err != nil {
		return nil, err
	}
	raw, err := openWith(wk, row.Wrapped, dekAD(userID, row.KEKID))
	if err != nil {
		return nil, fmt.Errorf("catalog: unwrap DEK: %w", err)
	}
	dek, err := vdcrypt.KeyFromBytes(raw)
	clear(raw)
	if err != nil {
		return nil, err
	}
	defer dek.Wipe()
	return newUserKeys(dek)
}

func (s *Service) createDEK(ctx context.Context, m *keys.Manager, userID int64) (*store.CatalogKey, error) {
	_, srv, err := m.CurrentSEK()
	if err != nil {
		return nil, err
	}
	wk, err := s.wrapKey(ctx, m, srv.SEKVersion)
	if err != nil {
		return nil, err
	}
	dek, err := vdcrypt.RandomKey(nil)
	if err != nil {
		return nil, err
	}
	defer dek.Wipe()
	kekID := kekIDPrefix + strconv.FormatUint(uint64(srv.SEKVersion), 10)
	wrapped, err := sealWith(wk, dek[:], dekAD(userID, kekID))
	if err != nil {
		return nil, err
	}
	if _, err := s.st.InsertCatalogKey(ctx, &store.CatalogKey{UserID: userID, KEKID: kekID, Wrapped: wrapped}); err != nil {
		return nil, err
	}
	return s.st.CatalogKey(ctx, userID) // the winner of a concurrent insert
}
