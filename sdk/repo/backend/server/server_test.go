package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// sleeps records backoff delays instead of waiting.
type sleeps struct{ d []time.Duration }

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.d = append(s.d, d)
	return nil
}

func newBackend(t *testing.T) (*fakeServer, *Backend, *sleeps) {
	t.Helper()
	f, c := newFake(t)
	s := &sleeps{}
	return f, New(c, testRepoID, Options{Sleep: s.sleep, BaseDelay: 10 * time.Millisecond}), s
}

func oidOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestObjects(t *testing.T) {
	ctx := context.Background()
	f, be, _ := newBackend(t)
	data := []byte(strings.Repeat("0123456789", 100))
	oid := oidOf(data)

	if _, err := be.Stat(ctx, repo.KindData, oid); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("stat missing: %v", err)
	}
	if _, err := be.Get(ctx, repo.KindData, oid); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if err := be.Put(ctx, repo.KindData, oid, data); err != nil {
		t.Fatal(err)
	}
	if err := be.Put(ctx, repo.KindData, oid, data); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if err := be.Put(ctx, repo.KindData, oid, []byte("other")); !errors.Is(err, repo.ErrInvalidObject) {
		t.Fatalf("local hash check: %v", err)
	}
	if n, err := be.Stat(ctx, repo.KindData, oid); err != nil || n != int64(len(data)) {
		t.Fatalf("stat: %d %v", n, err)
	}
	if got, err := be.Get(ctx, repo.KindData, oid); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get: %v", err)
	}

	for _, tc := range []struct {
		name        string
		off, length int64
		err         error
	}{
		{"head", 0, 10, nil},
		{"middle", 100, 250, nil},
		{"tail", 990, 10, nil},
		{"empty at end", 1000, 0, nil},
		{"past end", 995, 10, repo.ErrRange},
		{"start past end", 1001, 0, repo.ErrRange},
		{"negative", -1, 5, repo.ErrRange},
		{"beyond", 2000, 5, repo.ErrRange},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := be.GetRange(ctx, repo.KindData, oid, tc.off, tc.length)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, data[tc.off:tc.off+tc.length]) {
				t.Fatalf("range: %q %v", got, err)
			}
		})
	}
	f.noRange = true
	if _, err := be.GetRange(ctx, repo.KindData, oid, 100, 10); err == nil {
		t.Fatal("ignored Range accepted")
	}
	f.noRange = false

	// 422 object_hash_mismatch from the server.
	other := []byte("server says no")
	f.hash422 = true
	var hm *HashMismatchError
	if err := be.Put(ctx, repo.KindIndex, oidOf(other), other); !errors.As(err, &hm) || !errors.Is(err, repo.ErrInvalidObject) || hm.OID != oidOf(other) {
		t.Fatalf("422: %v", err)
	}

	if err := be.Delete(ctx, repo.KindData, oid); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := be.Get(ctx, repo.KindData, "nothex"); !errors.Is(err, repo.ErrInvalidObject) {
		t.Fatalf("bad oid: %v", err)
	}
}

func TestListPaged(t *testing.T) {
	ctx := context.Background()
	_, be, _ := newBackend(t)
	var want []string
	for i := range 5 {
		b := []byte{byte(i), 'x'}
		want = append(want, oidOf(b))
		if err := be.Put(ctx, repo.KindIndex, oidOf(b), b); err != nil {
			t.Fatal(err)
		}
	}
	infos, err := be.List(ctx, repo.KindIndex)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, i := range infos {
		if i.Size != 2 {
			t.Fatalf("size %d", i.Size)
		}
		got = append(got, i.OID)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("list %v, want %v", got, want)
	}
	if infos, err := be.List(ctx, repo.KindKeys); err != nil || len(infos) != 0 {
		t.Fatalf("empty list: %v %v", infos, err)
	}
}

func TestRetry(t *testing.T) {
	ctx := context.Background()
	f, be, s := newBackend(t)
	data := []byte("retry me")
	oid := oidOf(data)
	f.fail503["PUT data"] = 2
	if err := be.Put(ctx, repo.KindData, oid, data); err != nil {
		t.Fatal(err)
	}
	// Retry-After: 1 exceeds the 10ms/20ms backoff.
	if !slices.Equal(s.d, []time.Duration{time.Second, time.Second}) {
		t.Fatalf("delays %v", s.d)
	}
	f.fail503["GET data"] = 10
	_, err := be.Get(ctx, repo.KindData, oid)
	var ae *client.APIError
	if !errors.As(err, &ae) || ae.Status != 503 || f.fail503["GET data"] != 5 {
		t.Fatalf("bounded retries: %v (left %d)", err, f.fail503["GET data"])
	}
	f.fail503["HEAD data"] = 1
	f.fail503["LIST data"] = 1
	if _, err := be.Stat(ctx, repo.KindData, oid); err != nil {
		t.Fatal(err)
	}
	if l, err := be.List(ctx, repo.KindData); err != nil || len(l) != 1 {
		t.Fatalf("list: %v %v", l, err)
	}
}

func TestLocks(t *testing.T) {
	ctx := context.Background()
	f, be, _ := newBackend(t)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	other := New(f.client(t, f.url, 8, priv2), testRepoID, Options{MaxRetries: -1})

	cfg := []byte(`{"config":1}`)
	if err := be.Put(ctx, repo.KindConfig, repo.ConfigOID, cfg); !errors.Is(err, repo.ErrLocked) {
		t.Fatalf("config without lock: %v", err)
	}
	s1, err := be.Lock(ctx, repo.LockShared, 1500*time.Millisecond)
	if err != nil || s1.Kind != repo.LockShared || time.Until(s1.Expires) < time.Second {
		t.Fatalf("shared: %+v %v", s1, err)
	}
	if _, err := other.Lock(ctx, repo.LockShared, time.Minute); err != nil {
		t.Fatalf("second shared: %v", err)
	}
	_, err = other.Lock(ctx, repo.LockExclusive, time.Minute)
	var le *LockedError
	if !errors.As(err, &le) || !errors.Is(err, repo.ErrLocked) || le.Holder == "" || le.Expires.IsZero() {
		t.Fatalf("exclusive while shared: %v", err)
	}
	r1, err := be.RefreshLock(ctx, s1.ID, time.Hour)
	if err != nil || r1.Kind != repo.LockShared || time.Until(r1.Expires) < 59*time.Minute {
		t.Fatalf("refresh: %+v %v", r1, err)
	}
	if _, err := be.RefreshLock(ctx, "nope", time.Minute); !errors.Is(err, repo.ErrNoLock) {
		t.Fatalf("refresh unknown: %v", err)
	}
	if err := be.Unlock(ctx, "nope"); err != nil {
		t.Fatalf("unlock unknown: %v", err)
	}
	f.mu.Lock()
	f.locks = map[string]fakeLock{}
	f.mu.Unlock()

	ex, err := be.Lock(ctx, repo.LockExclusive, time.Minute)
	if err != nil || ex.Kind != repo.LockExclusive {
		t.Fatal(err)
	}
	// The exclusive lock belongs to device 7: device 8 cannot write config.
	if err := other.Put(ctx, repo.KindConfig, repo.ConfigOID, cfg); !errors.Is(err, repo.ErrLocked) {
		t.Fatalf("config by other device: %v", err)
	}
	if err := be.Put(ctx, repo.KindConfig, repo.ConfigOID, cfg); err != nil {
		t.Fatalf("config with lock: %v", err)
	}
	if err := be.Unlock(ctx, ex.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := be.RefreshLock(ctx, ex.ID, time.Minute); !errors.Is(err, repo.ErrNoLock) {
		t.Fatalf("refresh released: %v", err)
	}
	if got, err := be.Get(ctx, repo.KindConfig, repo.ConfigOID); err != nil || !bytes.Equal(got, cfg) {
		t.Fatalf("config read: %v", err)
	}
}

func TestCommitSnapshot(t *testing.T) {
	ctx := context.Background()
	f, be, _ := newBackend(t)
	root := []byte("snapshot root")
	req := wire.SnapshotCommitRequest{SnapshotOID: oidOf(root), JobID: 1, RunID: 2, Kind: "folder", Mode: "full", Epoch: 1, ProfileConsentID: "c1"}
	_, _, err := be.CommitSnapshot(ctx, req)
	if !client.IsCode(err, wire.CodePackMissing) {
		t.Fatalf("missing root: %v", err)
	}
	if err := be.Put(ctx, repo.KindSnapshots, oidOf(root), root); err != nil {
		t.Fatal(err)
	}
	f.fail503["POST /snapshots"] = 1
	resp, created, err := be.CommitSnapshot(ctx, req)
	if err != nil || !created || resp.SnapshotID != 1 {
		t.Fatalf("commit: %+v %v %v", resp, created, err)
	}
	resp, created, err = be.CommitSnapshot(ctx, req)
	if err != nil || created || resp.SnapshotID != 1 {
		t.Fatalf("replay: %+v %v %v", resp, created, err)
	}
	if _, _, err := be.CommitSnapshot(ctx, wire.SnapshotCommitRequest{SnapshotOID: "x"}); !errors.Is(err, repo.ErrInvalidObject) {
		t.Fatalf("bad oid: %v", err)
	}
}

// --- full repository round trip over the server backend ---

type keyset struct {
	boxPriv, boxPub vdcrypt.Key
	sekPub          vdcrypt.Key
	sign            ed25519.PrivateKey
	sik             ed25519.PrivateKey
}

func newKeyset(t *testing.T) keyset {
	t.Helper()
	var k keyset
	var err error
	if k.boxPriv, k.boxPub, err = vdcrypt.X25519FromSeed(vdcrypttest.DeviceBoxSeed); err != nil {
		t.Fatal(err)
	}
	if _, k.sekPub, err = vdcrypt.X25519FromSeed(vdcrypttest.SEKSeed); err != nil {
		t.Fatal(err)
	}
	k.sign = ed25519.NewKeyFromSeed(vdcrypttest.DeviceSignSeed[:])
	k.sik = ed25519.NewKeyFromSeed(vdcrypttest.SIKSeed[:])
	return k
}

func (k keyset) recipients() []repo.Recipient {
	return []repo.Recipient{
		repo.DeviceRecipient(k.boxPub, 3, k.sign.Public().(ed25519.PublicKey), "DESKTOP-ABC"),
		repo.EscrowRecipient(k.sekPub, k.sik.Public().(ed25519.PublicKey), vdcrypt.SignSEK(k.sik, k.sekPub, 1), 1, "server"),
	}
}

// writeBackup stores one data pack, one meta pack with a tree and a signed
// snapshot root; it returns the snapshot oid and the chunk id per file.
func writeBackup(t *testing.T, r *repo.Repo, k keyset, files map[string][]byte) (string, map[string]pack.BlobID) {
	t.Helper()
	ctx := context.Background()
	keys, err := r.Keys()
	if err != nil {
		t.Fatal(err)
	}
	chunks := map[string]pack.BlobID{}
	dw, err := r.NewPackWriter(pack.ClassData)
	if err != nil {
		t.Fatal(err)
	}
	var entries []snapshot.Entry
	for name, content := range files {
		id := pack.ComputeID(keys.ID, content)
		chunks[name] = id
		if !r.HasBlob(id) && !dw.Has(id) {
			if _, err := dw.Add(pack.TypeData, content, false); err != nil {
				t.Fatal(err)
			}
		}
		h := snapshot.FileHashBytes(keys.FileHash, content)
		entries = append(entries, snapshot.Entry{N: name, T: snapshot.TypeFile, S: int64(len(content)),
			M: 1760745600000000000, C: 1760745600000000000, A: 32,
			H: snapshot.HexPtr(pack.BlobID(h).String()), Ch: []string{id.String()}})
	}
	if dw.Len() > 0 {
		p, err := dw.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SavePack(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	trees, err := snapshot.EncodeTrees(keys.ID, entries)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := r.NewPackWriter(pack.ClassMeta)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range trees {
		if _, err := mw.Add(pack.TypeTree, tb.Raw, false); err != nil {
			t.Fatal(err)
		}
	}
	mp, err := mw.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SavePack(ctx, mp); err != nil {
		t.Fatal(err)
	}
	root := &snapshot.Root{
		V: snapshot.Version, Time: 1760745600000,
		Job:    snapshot.Job{ID: 12, Name: "Documents", Kind: "folder", Mode: "full"},
		Device: snapshot.Device{ID: 3, Name: "DESKTOP-ABC"},
		Host: snapshot.Host{Hostname: "DESKTOP-ABC", OS: "Windows 11", User: "ali",
			SID: "S-1-5-21-1000", MachineGUIDHash: strings.Repeat("ab", 32)},
		Tokens:      snapshot.Tokens{snapshot.TokenDocuments: `C:\Users\ali\Documents`},
		Roots:       []snapshot.RootRef{{TokenPath: snapshot.TokenDocuments, Tree: trees[0].IDHex()}},
		Stats:       snapshot.Stats{Files: int64(len(files)), Dirs: 1},
		Profile:     snapshot.Profile{SID: "S-1-5-21-1000", Username: "ali", ConsentID: "c1"},
		InstallMode: snapshot.InstallModeSimple,
	}
	snap, err := r.SaveSnapshot(ctx, root, k.sign)
	if err != nil {
		t.Fatal(err)
	}
	return snap, chunks
}

func readBack(t *testing.T, r *repo.Repo, snap string, files map[string][]byte, chunks map[string]pack.BlobID) {
	t.Helper()
	ctx := context.Background()
	snaps, err := r.ListSnapshots(ctx)
	if err != nil || !slices.Contains(snaps, snap) {
		t.Fatalf("snapshot %s not listed in %v (%v)", snap, snaps, err)
	}
	root, js, err := r.LoadSnapshot(ctx, snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.VerifySnapshot(root, js); err != nil {
		t.Fatal(err)
	}
	tree, err := r.ReadTree(ctx, root.Roots[0].Tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != len(files) {
		t.Fatalf("tree has %d entries, want %d", len(tree.Entries), len(files))
	}
	for _, e := range tree.Entries {
		got, err := r.ReadBlob(ctx, chunks[e.N])
		if err != nil || !bytes.Equal(got, files[e.N]) {
			t.Fatalf("%s: %v", e.N, err)
		}
	}
}

func TestRepoRoundTrip(t *testing.T) {
	ctx := context.Background()
	f, be, _ := newBackend(t)
	k := newKeyset(t)
	r, _, err := repo.Init(ctx, be, repo.InitOptions{EscrowMode: repo.EscrowFull, Recipients: k.recipients()})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"a.txt": []byte(strings.Repeat("VaultDock server round trip ", 200)),
		"b.bin": bytes.Repeat([]byte{1, 2, 3}, 1000),
		"c.txt": []byte("small"),
	}
	snap, chunks := writeBackup(t, r, k, files)
	readBack(t, r, snap, files, chunks)
	r.Close()
	if _, _, err := repo.Init(ctx, be, repo.InitOptions{Recipients: k.recipients()}); !errors.Is(err, repo.ErrExists) {
		t.Fatalf("second init: %v", err)
	}
	if len(f.locks) != 0 {
		t.Fatalf("locks left behind: %v", f.locks)
	}

	// Reopen with the device credential over a fresh client and Backend.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	be2 := New(f.client(t, f.url, 7, priv), testRepoID, Options{})
	o, err := repo.Open(ctx, be2, repo.DeviceCredential(k.boxPriv), repo.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if o.MetadataOnly() || o.Stale() || o.Epoch() != 1 {
		t.Fatalf("open: meta %v stale %v epoch %d", o.MetadataOnly(), o.Stale(), o.Epoch())
	}
	readBack(t, o, snap, files, chunks)

	resp, created, err := be2.CommitSnapshot(ctx, wire.SnapshotCommitRequest{SnapshotOID: snap, JobID: 12, Epoch: 1, ProfileConsentID: "c1"})
	if err != nil || !created || resp.SnapshotID == 0 {
		t.Fatalf("commit: %v", err)
	}
}
