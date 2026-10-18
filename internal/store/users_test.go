package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func openStore(t *testing.T) (*store.Store, *store.Location) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	l := &store.Location{Name: "Default", Path: t.TempDir(), Enabled: true, IsDefault: true}
	if err := st.CreateLocation(ctx, l); err != nil {
		t.Fatal(err)
	}
	return st, l
}

func sha(c byte) string { return strings.Repeat(string(c), 64) }

func TestCreateUserValidation(t *testing.T) {
	st, l := openStore(t)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.NewUser{Username: "neg", PasswordHash: "x", Role: "user", QuotaBytes: -1}); !errors.Is(err, store.ErrInvalidQuota) {
		t.Fatalf("negative quota: %v", err)
	}
	u, err := st.CreateUser(ctx, store.NewUser{Username: "noloc", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if u.LocationID != nil {
		t.Fatalf("location id 0 must be stored as NULL, got %v", *u.LocationID)
	}
	u, err = st.CreateUser(ctx, store.NewUser{Username: "withloc", PasswordHash: "x", Role: "user", LocationID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	if u.LocationID == nil || *u.LocationID != l.ID {
		t.Fatalf("location not stored: %v", u.LocationID)
	}
	if _, err := st.CreateUser(ctx, store.NewUser{Username: "withloc", PasswordHash: "x", Role: "user"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate username: %v", err)
	}
}

func TestUpdateUserQuota(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, store.NewUser{Username: "alice", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		quota int64
		want  error
	}{
		{"positive", 1000, nil},
		{"unlimited", 0, nil},
		{"negative", -5, store.ErrInvalidQuota},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.quota
			if err := st.UpdateUser(ctx, u.ID, store.UserPatch{QuotaBytes: &q}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
	got, _ := st.UserByID(ctx, u.ID)
	if got.QuotaBytes != 0 {
		t.Fatalf("negative quota must not be stored: %d", got.QuotaBytes)
	}
	q := int64(1)
	if err := st.UpdateUser(ctx, 9999, store.UserPatch{QuotaBytes: &q}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestQuotaAccountingConsistency(t *testing.T) {
	st, l := openStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, store.NewUser{Username: "bob", PasswordHash: "x", Role: "user", QuotaBytes: 250, LocationID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(name string, c byte, size int64, keep bool) (*store.CommitResult, error) {
		return st.CommitFile(ctx, store.CommitRequest{User: u, Name: name, Size: size, SHA256: sha(c), LocationID: l.ID, OnConflict: "replace", KeepReplaced: keep})
	}
	check := func(want int64) {
		t.Helper()
		stored, actual, err := st.UsageDrift(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored != want || actual != want {
			t.Fatalf("usage stored=%d actual=%d want %d", stored, actual, want)
		}
	}
	if _, err := commit("a.bin", 'a', 100, false); err != nil {
		t.Fatal(err)
	}
	// the same content under another name counts again (per node accounting)
	if _, err := commit("b.bin", 'a', 100, false); err != nil {
		t.Fatal(err)
	}
	check(200)
	if _, err := commit("c.bin", 'c', 100, false); !errors.Is(err, store.ErrQuota) {
		t.Fatalf("expected quota error, got %v", err)
	}
	check(200)
	// replace without keeping: old size is released before the quota check
	if _, err := commit("a.bin", 'd', 120, false); err != nil {
		t.Fatal(err)
	}
	check(220)
	// replace keeping the old version in trash: trash still counts
	if _, err := commit("b.bin", 'e', 10, true); err != nil {
		t.Fatal(err)
	}
	check(230)
	n, freed, err := st.PurgeTrash(ctx, u.ID, nil, 0)
	if err != nil || n == 0 {
		t.Fatalf("purge trash: %d %v", n, err)
	}
	// the trashed b.bin held the last reference to content 'a'
	if len(freed) != 1 || freed[0].SHA256 != sha('a') {
		t.Fatalf("freed=%v", freed)
	}
	check(130)

	// drift is repaired by RecalculateUsage
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET used_bytes = 999 WHERE id = ?", u.ID); err != nil {
		t.Fatal(err)
	}
	used, err := st.RecalculateUsage(ctx, u.ID)
	if err != nil || used != 130 {
		t.Fatalf("recalculate: %d %v", used, err)
	}
	check(130)
	if _, err := st.RecalculateUsage(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("recalculate unknown user: %v", err)
	}
	if _, _, err := st.UsageDrift(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("drift unknown user: %v", err)
	}
}
