package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const profileTestSID = "S-1-5-21-1000-2000-3000-1001"

func newProfileDevice(t *testing.T, st *store.Store, userID int64) *store.Device {
	t.Helper()
	d := &store.Device{UserID: userID, Name: "PC", Identifier: "id", PublicKey: "pk"}
	if err := st.CreateDevice(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func profileJobState(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	var s string
	if err := st.DB.QueryRow(`SELECT state FROM backup_jobs WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeviceProfileBlacklistClearReconsent(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newProfileDevice(t, st, u.ID)
	res, err := st.DB.Exec(`INSERT INTO backup_jobs(user_id, device_id, name, kind, profile_sid, created_at, updated_at) VALUES(?,?,?,?,?,1,1)`, u.ID, d.ID, "docs", "folder", profileTestSID)
	if err != nil {
		t.Fatal(err)
	}
	jobID, _ := res.LastInsertId()
	digest := strings.Repeat("a", 64)

	// A consented state needs a consent record.
	bad := wire.ProfileReport{Profiles: []wire.ProfileReportEntry{{SID: profileTestSID, State: wire.ProfileStateAuthorised}}}
	if _, err := st.ApplyProfileReport(ctx, d.ID, u.ID, bad); !errors.Is(err, store.ErrProfileState) {
		t.Fatalf("authorised without consent: %v", err)
	}

	rep := wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: profileTestSID, Username: "alice", ProfilePath: `C:\Users\alice`, State: wire.ProfileStateActiveDefault}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: profileTestSID, ConsentAt: 1000, ProofDigest: digest}},
	}
	ch, err := st.ApplyProfileReport(ctx, d.ID, u.ID, rep)
	if err != nil || len(ch) != 1 || ch[0].To != wire.ProfileStateActiveDefault {
		t.Fatalf("first report: %v %v", ch, err)
	}
	p, _ := st.DeviceProfileBySID(ctx, d.ID, profileTestSID)
	if !p.Consented() || p.ConsentID != "c1" || p.ProofDigest != digest || p.VaultDockUserID == nil || *p.VaultDockUserID != u.ID {
		t.Fatalf("active_default: %+v", p)
	}

	// Security event: blacklisted, job suspended, idempotent.
	p, already, err := st.BlacklistProfile(ctx, d.ID, profileTestSID, wire.ProfileSignalPasswordReset, wire.ProfileEvidenceEventLog4724)
	if err != nil || already || p.State != wire.ProfileStateBlacklisted || p.BlacklistReason != wire.ProfileSignalPasswordReset || p.BlacklistEvidence != wire.ProfileEvidenceEventLog4724 || p.Consented() {
		t.Fatalf("blacklist: %+v %v %v", p, already, err)
	}
	if s := profileJobState(t, st, jobID); s != "suspended_by_profile" {
		t.Fatalf("job after blacklist: %s", s)
	}
	if _, already, _ := st.BlacklistProfile(ctx, d.ID, profileTestSID, wire.ProfileSignalSIDChanged, wire.ProfileEvidenceSIDLookup); !already {
		t.Fatal("second event not idempotent")
	}

	// The device cannot leave blacklisted, even with a new consent.
	rep.Consents[0] = wire.ProfileConsentRecord{ConsentID: "c2", SID: profileTestSID, ConsentAt: 2000, ProofDigest: digest}
	rep.Profiles[0].State = wire.ProfileStateAuthorised
	if ch, err := st.ApplyProfileReport(ctx, d.ID, u.ID, rep); err != nil || len(ch) != 0 {
		t.Fatalf("report while blacklisted: %v %v", ch, err)
	}
	p, _ = st.DeviceProfileBySID(ctx, d.ID, profileTestSID)
	if p.State != wire.ProfileStateBlacklisted || p.BlacklistReason != wire.ProfileSignalPasswordReset {
		t.Fatalf("still blacklisted: %+v", p)
	}

	// Superuser clear → cleared_pending_consent, never authorised.
	admin, err := st.CreateUser(ctx, store.NewUser{Username: "root", PasswordHash: "x", Role: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = st.ClearProfileBlacklist(ctx, p.ID, admin.ID)
	if err != nil || p.State != wire.ProfileStateClearedPendingConsent || p.ClearedBy == nil || *p.ClearedBy != admin.ID || p.ClearedAt == nil {
		t.Fatalf("clear: %+v %v", p, err)
	}
	if _, err := st.ClearProfileBlacklist(ctx, p.ID, admin.ID); !errors.Is(err, store.ErrProfileState) {
		t.Fatalf("second clear: %v", err)
	}
	if _, err := st.ClearProfileBlacklist(ctx, 9999, admin.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("clear unknown: %v", err)
	}
	if s := profileJobState(t, st, jobID); s != "suspended_by_profile" {
		t.Fatalf("job after clear: %s", s)
	}

	// The old consent, or a consent older than the clear, does not re-enable.
	rep.Consents[0] = wire.ProfileConsentRecord{ConsentID: "c1", SID: profileTestSID, ConsentAt: *p.ClearedAt + 1, ProofDigest: digest}
	st.ApplyProfileReport(ctx, d.ID, u.ID, rep)
	rep.Consents[0] = wire.ProfileConsentRecord{ConsentID: "c3", SID: profileTestSID, ConsentAt: *p.ClearedAt - 1, ProofDigest: digest}
	st.ApplyProfileReport(ctx, d.ID, u.ID, rep)
	if p, _ = st.DeviceProfileBySID(ctx, d.ID, profileTestSID); p.State != wire.ProfileStateClearedPendingConsent {
		t.Fatalf("stale consent re-enabled: %+v", p)
	}

	// Re-consent: a new consent after the clear authorises and resumes jobs.
	rep.Consents[0] = wire.ProfileConsentRecord{ConsentID: "c4", SID: profileTestSID, ConsentAt: *p.ClearedAt + 5, ProofDigest: strings.Repeat("b", 64)}
	ch, err = st.ApplyProfileReport(ctx, d.ID, u.ID, rep)
	if err != nil || len(ch) != 1 || ch[0].From != wire.ProfileStateClearedPendingConsent || ch[0].To != wire.ProfileStateAuthorised {
		t.Fatalf("re-consent: %v %v", ch, err)
	}
	p, _ = st.DeviceProfileBySID(ctx, d.ID, profileTestSID)
	if !p.Consented() || p.ConsentID != "c4" {
		t.Fatalf("after re-consent: %+v", p)
	}
	if s := profileJobState(t, st, jobID); s != "active" {
		t.Fatalf("job after re-consent: %s", s)
	}
}

func TestDeviceProfileRequests(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	admin, _ := st.CreateUser(ctx, store.NewUser{Username: "root", PasswordHash: "x", Role: "admin"})
	d := newProfileDevice(t, st, u.ID)

	other := "S-1-5-21-1000-2000-3000-1002"
	pr, err := st.CreateProfileRequest(ctx, d.ID, admin.ID, wire.ProfileRequestScopeSelected, []string{other})
	if err != nil || pr.ID == 0 {
		t.Fatalf("request: %+v %v", pr, err)
	}
	p, err := st.DeviceProfileBySID(ctx, d.ID, other)
	if err != nil || p.State != wire.ProfileStateRequested || p.RequestScope != wire.ProfileRequestScopeSelected || p.RequestedByUserID == nil || *p.RequestedByUserID != admin.ID || p.Consented() {
		t.Fatalf("requested profile: %+v %v", p, err)
	}
	taskID, err := st.QueueProfileConsentTask(ctx, d.ID, admin.ID, []byte(`{"request_id":"1","sids":"all","requested_by":"root"}`))
	if err != nil || taskID == 0 {
		t.Fatalf("task: %d %v", taskID, err)
	}
	if err := st.SetProfileRequestTask(ctx, pr.ID, taskID); err != nil {
		t.Fatal(err)
	}
	var kind, mods string
	var owner int64
	st.DB.QueryRow(`SELECT kind, required_modules, user_id FROM device_tasks WHERE id = ?`, taskID).Scan(&kind, &mods, &owner)
	if kind != wire.TaskKindProfileConsentRequest || mods != `["profiles"]` || owner != u.ID {
		t.Fatalf("task row: %s %s %d", kind, mods, owner)
	}
	if _, err := st.QueueProfileConsentTask(ctx, 9999, admin.ID, []byte(`{}`)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("task for unknown device: %v", err)
	}

	// "all" turns revoked profiles into requested ones and leaves blacklisted alone.
	st.BlacklistProfile(ctx, d.ID, profileTestSID, wire.ProfileSignalAccountDisabled, wire.ProfileEvidenceSIDLookup)
	st.DB.Exec(`UPDATE device_profiles SET state = 'revoked' WHERE sid = ?`, other)
	if _, err := st.CreateProfileRequest(ctx, d.ID, admin.ID, wire.ProfileRequestScopeAll, nil); err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListDeviceProfiles(ctx, d.ID, 0, 10)
	got := map[string]string{}
	for _, p := range list {
		got[p.SID] = p.State
	}
	if got[other] != wire.ProfileStateRequested || got[profileTestSID] != wire.ProfileStateBlacklisted {
		t.Fatalf("after all-request: %v", got)
	}
}

func TestSuperuserFlag(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	a1, _ := st.CreateUser(ctx, store.NewUser{Username: "root", PasswordHash: "x", Role: "admin"})
	a2, _ := st.CreateUser(ctx, store.NewUser{Username: "root2", PasswordHash: "x", Role: "admin"})
	u := newUser(t, st, "alice")

	if ok, _ := st.IsSuperuser(ctx, a1.ID); !ok {
		t.Fatal("first admin is not superuser")
	}
	if ok, _ := st.IsSuperuser(ctx, a2.ID); ok {
		t.Fatal("second admin is superuser")
	}
	if last, _ := st.IsLastSuperuser(ctx, a1.ID); !last {
		t.Fatal("IsLastSuperuser")
	}
	if _, err := st.SetSuperuser(ctx, a1.ID, false); !errors.Is(err, store.ErrLastSuperuser) {
		t.Fatalf("revoke last: %v", err)
	}
	if _, err := st.SetSuperuser(ctx, u.ID, true); !errors.Is(err, store.ErrNotAdmin) {
		t.Fatalf("grant to user: %v", err)
	}
	if ch, err := st.SetSuperuser(ctx, a2.ID, true); err != nil || !ch {
		t.Fatalf("grant: %v %v", ch, err)
	}
	if ch, err := st.SetSuperuser(ctx, a2.ID, true); err != nil || ch {
		t.Fatalf("grant again: %v %v", ch, err)
	}
	if _, err := st.SetSuperuser(ctx, a1.ID, false); err != nil {
		t.Fatalf("revoke with another left: %v", err)
	}
	ids, _ := st.SuperuserIDs(ctx)
	if len(ids) != 1 || ids[0] != a2.ID {
		t.Fatalf("superusers: %v", ids)
	}
	if _, err := st.SetSuperuser(ctx, 9999, true); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}
