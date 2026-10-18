package repo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

func backends(t *testing.T) map[string]Backend {
	t.Helper()
	d, err := NewDirBackend(filepath.Join(t.TempDir(), "repo", "x"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Backend{"mem": NewMemBackend(), "dir": d}
}

func TestBackends(t *testing.T) {
	ctx := context.Background()
	for name, be := range backends(t) {
		t.Run(name, func(t *testing.T) {
			data := []byte("0123456789")
			oid := vdcrypt.OID(data)
			for _, kind := range []Kind{KindData, KindIndex} {
				if err := be.Put(ctx, kind, oid, data); err != nil {
					t.Fatal(err)
				}
				if err := be.Put(ctx, kind, oid, data); err != nil {
					t.Fatalf("re-put: %v", err)
				}
			}
			if err := be.Put(ctx, KindData, oid, []byte("other")); !errors.Is(err, ErrInvalidObject) {
				t.Fatalf("hash mismatch: %v", err)
			}
			for _, bad := range []string{"../config", "ABC", oid[:63], ConfigOID} {
				if err := be.Put(ctx, KindKeys, bad, data); !errors.Is(err, ErrInvalidObject) {
					t.Fatalf("bad oid %q: %v", bad, err)
				}
			}
			if err := be.Put(ctx, KindConfig, oid, data); !errors.Is(err, ErrInvalidObject) {
				t.Fatal("config under a content oid")
			}
			if err := be.Put(ctx, KindConfig, ConfigOID, []byte("{}")); err != nil {
				t.Fatal(err)
			}
			if got, err := be.Get(ctx, KindData, oid); err != nil || string(got) != string(data) {
				t.Fatalf("get: %q %v", got, err)
			}
			if got, err := be.GetRange(ctx, KindData, oid, 3, 4); err != nil || string(got) != "3456" {
				t.Fatalf("range: %q %v", got, err)
			}
			if got, err := be.GetRange(ctx, KindData, oid, 10, 0); err != nil || len(got) != 0 {
				t.Fatalf("empty range: %q %v", got, err)
			}
			for _, r := range [][2]int64{{8, 3}, {-1, 2}, {11, 0}, {0, -1}} {
				if _, err := be.GetRange(ctx, KindData, oid, r[0], r[1]); !errors.Is(err, ErrRange) {
					t.Fatalf("range %v: %v", r, err)
				}
			}
			if n, err := be.Stat(ctx, KindData, oid); err != nil || n != 10 {
				t.Fatalf("stat: %d %v", n, err)
			}
			missing := vdcrypt.OID([]byte("missing"))
			if _, err := be.Get(ctx, KindData, missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing: %v", err)
			}
			if _, err := be.Stat(ctx, KindSnapshots, missing); !errors.Is(err, ErrNotFound) {
				t.Fatalf("missing stat: %v", err)
			}
			for kind, want := range map[Kind]int{KindData: 1, KindIndex: 1, KindKeys: 0, KindConfig: 1} {
				l, err := be.List(ctx, kind)
				if err != nil || len(l) != want {
					t.Fatalf("list %s: %v %v", kind, l, err)
				}
			}
			if err := be.Delete(ctx, KindData, oid); err != nil {
				t.Fatal(err)
			}
			if err := be.Delete(ctx, KindData, oid); err != nil {
				t.Fatal(err)
			}
			if _, err := be.Get(ctx, KindData, oid); !errors.Is(err, ErrNotFound) {
				t.Fatal("deleted object still readable")
			}
		})
	}
}

func TestDirBackendLayout(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo", "u")
	d, err := NewDirBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("pack")
	oid := vdcrypt.OID(data)
	if err := d.Put(ctx, KindData, oid, data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "data", oid[:2], oid)); err != nil {
		t.Fatal(err)
	}
	// Foreign and temporary files are not listed.
	os.WriteFile(filepath.Join(root, "data", oid[:2], ".tmp-1"), nil, 0o600)
	os.WriteFile(filepath.Join(root, "data", oid), nil, 0o600)
	if l, _ := d.List(ctx, KindData); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}

	// A full repository works on the directory backend.
	k := newKeyset(t)
	r, text := initRepo(t, d, k, EscrowFull)
	w := writeBackup(t, r, k, map[string][]byte{"f": []byte("dir backend")})
	cred, _ := RecoveryCredential(text)
	o, err := Open(ctx, d, cred, Options{})
	if err != nil {
		t.Fatal(err)
	}
	readBack(t, o, w, true)
	if _, err := os.Stat(filepath.Join(root, "config")); err != nil {
		t.Fatal(err)
	}
}

func TestLockTable(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1000, 0)
	lt := &LockTable{Now: func() time.Time { return now }}
	s1, err := lt.Lock(ctx, LockShared, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Lock(ctx, LockShared, time.Minute); err != nil {
		t.Fatal("two shared locks")
	}
	if _, err := lt.Lock(ctx, LockExclusive, time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatal("exclusive over shared")
	}
	now = now.Add(30 * time.Second)
	if _, err := lt.RefreshLock(ctx, s1.ID, time.Minute); err != nil {
		t.Fatal(err)
	}
	now = now.Add(45 * time.Second) // the second shared lock expired
	if _, err := lt.Lock(ctx, LockExclusive, time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatal("exclusive over refreshed shared")
	}
	lt.Unlock(ctx, s1.ID)
	x, err := lt.Lock(ctx, LockExclusive, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lt.Lock(ctx, LockShared, time.Minute); !errors.Is(err, ErrLocked) {
		t.Fatal("shared over exclusive")
	}
	now = now.Add(2 * time.Minute)
	if _, err := lt.RefreshLock(ctx, x.ID, time.Minute); !errors.Is(err, ErrNoLock) {
		t.Fatal("refresh of expired lock")
	}

	// Rotation needs the exclusive lock.
	be := NewMemBackend()
	k := newKeyset(t)
	r, _ := initRepo(t, be, k, EscrowFull)
	l, _ := be.Lock(ctx, LockShared, time.Minute)
	if _, err := r.Rotate(ctx, RotateOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("rotate under shared lock: %v", err)
	}
	be.Unlock(ctx, l.ID)
	if _, err := r.Rotate(ctx, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
}
