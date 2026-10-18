package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{st: st, cfg: &config.Config{}}
}

func mkUser(t *testing.T, st *store.Store, name, role string) *store.User {
	t.Helper()
	u, err := st.CreateUser(context.Background(), store.NewUser{Username: name, PasswordHash: "x", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func codeOf(err error) (int, string) {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Status, ae.Code
	}
	return 0, ""
}

func TestSetUserSuperuser(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	root := mkUser(t, s.st, "root", "admin") // first admin: superuser
	a2 := mkUser(t, s.st, "admin2", "admin")
	alice := mkUser(t, s.st, "alice", "user")

	call := func(caller, target *store.User, on bool) error {
		r := httptest.NewRequest("PATCH", "/api/v1/admin/users/1", nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey, &Principal{User: caller, Session: &store.Session{}}))
		return s.setUserSuperuser(r, target, on)
	}

	if st, code := codeOf(call(a2, a2, true)); st != 403 || code != wire.CodeNotSuperuser {
		t.Fatalf("non-superuser grant: %d %s", st, code)
	}
	if st, code := codeOf(call(root, root, false)); st != 409 || code != "last_superuser" {
		t.Fatalf("revoke last: %d %s", st, code)
	}
	if st, code := codeOf(call(root, alice, true)); st != 400 || code != "not_admin" {
		t.Fatalf("grant to user: %d %s", st, code)
	}
	if err := s.guardLastSuperuser(ctx, root.ID); err == nil {
		t.Fatal("guard allowed removing the last superuser")
	}
	if err := call(root, a2, true); err != nil {
		t.Fatal(err)
	}
	if err := s.guardLastSuperuser(ctx, root.ID); err != nil {
		t.Fatalf("guard with two superusers: %v", err)
	}
	if err := call(a2, root, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.st.IsSuperuser(ctx, root.ID); ok {
		t.Fatal("root still superuser")
	}
	// Audited.
	list, err := s.st.ListActivity(ctx, store.ActivityFilter{AuditAll: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range list {
		got = append(got, a.Action)
	}
	if j := strings.Join(got, ","); !strings.Contains(j, "admin.superuser_granted") || !strings.Contains(j, "admin.superuser_revoked") {
		t.Fatalf("audit: %s", j)
	}
}

func TestRequireProfileAuthorised(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	u := mkUser(t, s.st, "alice", "user")
	d := &store.Device{UserID: u.ID, Name: "PC", Identifier: "id", PublicKey: "pk"}
	if err := s.st.CreateDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	sid := "S-1-5-21-1-2-3-1001"
	want := func(err error, status int, code string) {
		t.Helper()
		if status == 0 {
			if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			return
		}
		if st, c := codeOf(err); st != status || c != code {
			t.Fatalf("want %d %s, got %v", status, code, err)
		}
	}

	want(s.requireProfileAuthorised(ctx, d.ID, sid), 403, wire.CodeProfileNotAuthorised)

	s.st.CreateProfileRequest(ctx, d.ID, u.ID, wire.ProfileRequestScopeSelected, []string{sid})
	want(s.requireProfileAuthorised(ctx, d.ID, sid), 403, wire.CodeProfileNotAuthorised)

	rep := wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: sid, State: wire.ProfileStateAuthorised}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: sid, ConsentAt: 1, ProofDigest: strings.Repeat("a", 64)}},
	}
	if _, err := s.st.ApplyProfileReport(ctx, d.ID, u.ID, rep); err != nil {
		t.Fatal(err)
	}
	want(s.requireProfileAuthorised(ctx, d.ID, sid), 0, "")
	want(s.requireProfileConsent(ctx, d.ID, sid, "c1"), 0, "")
	want(s.requireProfileConsent(ctx, d.ID, sid, "other"), 403, wire.CodeProfileNotAuthorised)
	want(s.requireProfileAuthorised(ctx, d.ID+1, sid), 403, wire.CodeProfileNotAuthorised)

	p, _, err := s.st.BlacklistProfile(ctx, d.ID, sid, wire.ProfileSignalSIDChanged, wire.ProfileEvidenceSIDLookup)
	if err != nil {
		t.Fatal(err)
	}
	want(s.requireProfileAuthorised(ctx, d.ID, sid), 423, wire.CodeProfileBlacklisted)
	want(s.requireProfileConsent(ctx, d.ID, sid, "c1"), 423, wire.CodeProfileBlacklisted)

	if _, err := s.st.ClearProfileBlacklist(ctx, p.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	want(s.requireProfileAuthorised(ctx, d.ID, sid), 403, wire.CodeProfileNotAuthorised)
}
