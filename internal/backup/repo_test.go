package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore/objstoretest"
)

const uuid = "0b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"

func TestObjectKey(t *testing.T) {
	oid := "ab" + strings.Repeat("0", 62)
	cases := []struct {
		kind, oid, want string
	}{
		{"config", wire.ObjectConfigOID, "repo/" + uuid + "/config"},
		{"data", oid, "repo/" + uuid + "/data/ab/" + oid},
		{"keys", oid, "repo/" + uuid + "/keys/" + oid},
		{"chain", oid, "repo/" + uuid + "/chain/" + oid},
		{"index", oid, "repo/" + uuid + "/index/" + oid},
		{"snapshots", oid, "repo/" + uuid + "/snapshots/" + oid},
		{"locks", oid, "repo/" + uuid + "/locks/" + oid},
	}
	for _, c := range cases {
		got, err := ObjectKey(uuid, c.kind, c.oid)
		if err != nil || got != c.want {
			t.Errorf("ObjectKey(%s) = %q, %v", c.kind, got, err)
		}
	}
	bad := []struct{ uuid, kind, oid string }{
		{uuid, "config", oid},                                 // config needs the zero oid
		{uuid, "data", wire.ObjectConfigOID[:63]},             // short
		{uuid, "data", strings.ToUpper(oid)},                  // upper-case
		{uuid, "packs", oid},                                  // unknown kind
		{strings.ToUpper(uuid), "data", oid},                  // non-canonical uuid
		{"0b4f6c1e-3d2a-1f5b-8c9d-0e1f2a3b4c5d", "data", oid}, // not v4
		{"../x", "data", oid},
	}
	for _, b := range bad {
		if _, err := ObjectKey(b.uuid, b.kind, b.oid); !errors.Is(err, objstore.ErrInvalidKey) {
			t.Errorf("ObjectKey(%q, %q, %q) = %v, want ErrInvalidKey", b.uuid, b.kind, b.oid, err)
		}
	}
}

func TestStorePutGetConfig(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "backup")
	s, err := OpenLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := OpenLocal(root); err != nil || again != s {
		t.Fatalf("OpenLocal is not cached per root: %v", err)
	}
	if _, err := OpenLocal("relative"); err == nil {
		t.Fatal("relative root accepted")
	}

	pack := []byte("pack bytes")
	oid := objstoretest.Sum(pack)
	if err := s.Put(ctx, uuid, "data", oid, bytes.NewReader(pack), int64(len(pack))); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, uuid, "data", oid, bytes.NewReader(pack), int64(len(pack))); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "repo", uuid, "data", oid[:2], oid)); err != nil {
		t.Fatalf("on-disk layout: %v", err)
	}
	other := objstoretest.Sum([]byte("other"))
	if err := s.Put(ctx, uuid, "index", other, bytes.NewReader(pack), int64(len(pack))); !errors.Is(err, objstore.ErrHashMismatch) {
		t.Fatalf("hash mismatch: %v", err)
	}
	if err := s.Put(ctx, uuid, "config", wire.ObjectConfigOID, bytes.NewReader(pack), int64(len(pack))); err == nil {
		t.Fatal("Put accepted config")
	}
	rc, err := s.GetRange(ctx, uuid, "data", oid, 5, -1)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	buf.ReadFrom(rc)
	rc.Close()
	if buf.String() != "bytes" {
		t.Fatalf("range = %q", buf.String())
	}
	if info, err := s.Stat(ctx, uuid, "data", oid); err != nil || info.Size != int64(len(pack)) {
		t.Fatalf("stat: %+v %v", info, err)
	}

	// Config: absent, written, replaced.
	if _, ok, err := s.ConfigSHA256(ctx, uuid); err != nil || ok {
		t.Fatalf("absent config: %v %v", ok, err)
	}
	for _, c := range []string{`{"v":1}`, `{"v":1,"escrow_mode":"metadata"}`} {
		b := []byte(c)
		if err := s.PutConfig(ctx, uuid, bytes.NewReader(b), int64(len(b)), objstoretest.Sum(b)); err != nil {
			t.Fatal(err)
		}
		sum, ok, err := s.ConfigSHA256(ctx, uuid)
		if err != nil || !ok || sum != objstoretest.Sum(b) {
			t.Fatalf("config sum: %s %v %v", sum, ok, err)
		}
	}
	bad := []byte("x")
	if err := s.PutConfig(ctx, uuid, bytes.NewReader(bad), 1, objstoretest.Sum([]byte("y"))); !errors.Is(err, objstore.ErrHashMismatch) {
		t.Fatalf("config hash mismatch: %v", err)
	}
}

// putOnly hides the Replacer capability.
type putOnly struct{ objstore.Store }

func TestPutConfigNeedsReplacer(t *testing.T) {
	s, err := OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := []byte("{}")
	if err := New(putOnly{s.Backend()}).PutConfig(context.Background(), uuid, bytes.NewReader(b), 2, objstoretest.Sum(b)); !errors.Is(err, ErrNotReplaceable) {
		t.Fatalf("PutConfig without Replacer = %v", err)
	}
}
