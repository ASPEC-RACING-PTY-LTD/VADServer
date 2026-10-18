package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// TestProfilesBlacklistClearReconsent covers api-v1 §6B end to end:
// device report, security event → blacklist, superuser-only clear,
// re-consent, session views and admin consent requests.
func TestProfilesBlacklistClearReconsent(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	admin.must("POST", "/api/v1/admin/users", map[string]any{"username": "admin2", "password": "password-admin2", "role": "admin"}, 201)
	admin2 := e.web()
	admin2.must("POST", "/api/v1/auth/login", map[string]any{"login": "admin2", "password": "password-admin2"}, 200)
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	sid := "S-1-5-21-1000-2000-3000-1001"
	digest := strings.Repeat("a", 64)
	listPath := fmt.Sprintf("/api/v1/devices/%d/profiles", dc.DeviceID)

	report := func(state, consentID string, consentAt int64) (*wire.Profile, error) {
		rep := wire.ProfileReport{Profiles: []wire.ProfileReportEntry{{SID: sid, Username: "alice", ProfilePath: `C:\Users\alice`, State: state}}}
		if consentID != "" {
			rep.Consents = []wire.ProfileConsentRecord{{ConsentID: consentID, SID: sid, ConsentAt: consentAt, ProofDigest: digest}}
		}
		var out struct {
			Items []wire.Profile `json:"items"`
		}
		if err := dc.Call(ctx, "PUT", "/api/v1/device/profiles", rep, &out); err != nil {
			return nil, err
		}
		if len(out.Items) != 1 {
			t.Fatalf("report reply: %+v", out)
		}
		return &out.Items[0], nil
	}

	// Validation and the consent requirement.
	if st, code := apiCode(t, dc.Call(ctx, "PUT", "/api/v1/device/profiles", wire.ProfileReport{Profiles: []wire.ProfileReportEntry{{SID: "bogus", State: "x"}}}, nil)); st != 422 || code != "validation" {
		t.Fatalf("bad report: %d %s", st, code)
	}
	if _, err := report(wire.ProfileStateAuthorised, "", 0); err == nil {
		t.Fatal("authorised without consent accepted")
	} else if st, code := apiCode(t, err); st != 400 || code != "invalid_profile_state" {
		t.Fatalf("authorised without consent: %d %s", st, code)
	}
	// Sessions cannot use the device route.
	if code, out := alice.req("PUT", "/api/v1/device/profiles", map[string]any{"profiles": []any{}}); code != 403 || errCode(out) != "device_required" {
		t.Fatalf("session on dev route: %d %v", code, out)
	}

	p, err := report(wire.ProfileStateActiveDefault, "c1", 1000)
	if err != nil || p.State != wire.ProfileStateActiveDefault || p.ConsentID == nil || *p.ConsentID != "c1" {
		t.Fatalf("active_default: %+v %v", p, err)
	}

	// Session views: owner and admin see the list, others get 404.
	out := alice.must("GET", listPath, nil, 200)
	items := out["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["state"] != wire.ProfileStateActiveDefault {
		t.Fatalf("owner list: %v", out)
	}
	admin.must("GET", listPath, nil, 200)
	if code, _ := bob.req("GET", listPath, nil); code != 404 {
		t.Fatalf("other user list: %d", code)
	}

	// Security event → blacklisted.
	evPath := "/api/v1/device/profiles/" + sid + "/security-event"
	if st, code := apiCode(t, dc.Call(ctx, "POST", evPath, map[string]any{"signal": "nope", "observed_at": 1, "evidence": "sid_lookup"}, nil)); st != 422 || code != "validation" {
		t.Fatalf("bad signal: %d %s", st, code)
	}
	var bl wire.Profile
	if err := dc.Call(ctx, "POST", evPath, wire.ProfileSecurityEventRequest{Signal: wire.ProfileSignalPasswordReset, ObservedAt: time.Now().UnixMilli(), Evidence: wire.ProfileEvidenceDPAPIUnprotectFailed}, &bl); err != nil {
		t.Fatal(err)
	}
	if bl.State != wire.ProfileStateBlacklisted || bl.BlacklistReason == nil || *bl.BlacklistReason != wire.ProfileSignalPasswordReset || bl.BlacklistedAt == nil {
		t.Fatalf("blacklisted: %+v", bl)
	}
	// The device cannot re-enable it by reporting a new consent.
	if p, err := report(wire.ProfileStateAuthorised, "c2", time.Now().UnixMilli()); err != nil || p.State != wire.ProfileStateBlacklisted {
		t.Fatalf("report while blacklisted: %+v %v", p, err)
	}

	// Clear is superuser-only.
	clearPath := fmt.Sprintf("/api/v1/admin/device-profiles/%d/clear", bl.ID)
	for name, w := range map[string]*web{"admin2": admin2, "alice": alice} {
		if code, out := w.req("POST", clearPath, map[string]any{"confirm_text": "alice"}); code != 403 || errCode(out) != "not_superuser" {
			t.Fatalf("%s clear: %d %v", name, code, out)
		}
	}
	if code, out := admin.req("POST", clearPath, map[string]any{"confirm_text": "wrong"}); code != 422 || errCode(out) != "validation" {
		t.Fatalf("wrong confirm: %d %v", code, out)
	}
	if code, out := admin.req("POST", "/api/v1/admin/device-profiles/9999/clear", map[string]any{"confirm_text": "x"}); code != 404 || errCode(out) != "profile_not_found" {
		t.Fatalf("unknown profile: %d %v", code, out)
	}
	cl := admin.must("POST", clearPath, map[string]any{"confirm_text": "alice"}, 200)
	if cl["state"] != wire.ProfileStateClearedPendingConsent || cl["cleared_by"] == nil {
		t.Fatalf("cleared: %v", cl)
	}
	if code, out := admin.req("POST", clearPath, map[string]any{"confirm_text": sid}); code != 409 || errCode(out) != "not_blacklisted" {
		t.Fatalf("second clear: %d %v", code, out)
	}
	// Not re-enabled by the clear itself, nor by the old consent.
	if p, err := report(wire.ProfileStateAuthorised, "c1", time.Now().UnixMilli()+1000); err != nil || p.State != wire.ProfileStateClearedPendingConsent {
		t.Fatalf("old consent after clear: %+v %v", p, err)
	}
	// Re-consent authorises.
	if p, err := report(wire.ProfileStateAuthorised, "c5", time.Now().UnixMilli()+1000); err != nil || p.State != wire.ProfileStateAuthorised || *p.ConsentID != "c5" {
		t.Fatalf("re-consent: %+v %v", p, err)
	}

	// Admin consent requests.
	reqPath := fmt.Sprintf("/api/v1/devices/%d/profiles/requests", dc.DeviceID)
	if code, out := admin.req("POST", reqPath, map[string]any{"all": true, "sids": []string{sid}}); code != 422 {
		t.Fatalf("sids and all: %d %v", code, out)
	}
	if code, out := admin.req("POST", reqPath, map[string]any{"sids": []string{"x"}}); code != 422 {
		t.Fatalf("bad sid: %d %v", code, out)
	}
	if code, _ := alice.req("POST", reqPath, map[string]any{"all": true}); code != 403 {
		t.Fatalf("non-admin request: %d", code)
	}
	// Consent requests need the device's profiles module.
	mods := wire.ModuleReport{InstallMode: wire.ModuleInstallModeAdvanced, CoreVersion: "1.0.0", Modules: []wire.ModuleStatus{
		{ID: wire.ModuleCore, Version: "1.0.0", State: wire.ModuleStateEnabled},
		{ID: wire.ModuleProfiles, Version: "1.0.0", State: wire.ModuleStateEnabled},
	}}
	if err := dc.Call(ctx, "PUT", "/api/v1/device/modules", mods, nil); err != nil {
		t.Fatal(err)
	}
	created := admin.must("POST", reqPath, map[string]any{"sids": []string{"S-1-5-21-1000-2000-3000-1002"}}, 201)
	if created["task_id"] == nil {
		t.Fatalf("request reply: %v", created)
	}
	admin.must("POST", reqPath, map[string]any{"all": true}, 201)
	out = alice.must("GET", listPath, nil, 200)
	if n := len(out["items"].([]any)); n != 2 {
		t.Fatalf("profiles after request: %v", out)
	}

	// Everything is audited.
	raw, _ := json.Marshal(admin.must("GET", "/api/v1/admin/audit?limit=200", nil, 200))
	for _, action := range []string{"profile.consented", "profile.security_event", "profile.blacklist_cleared", "profile.consent_requested"} {
		if !strings.Contains(string(raw), action) {
			t.Fatalf("audit log lacks %s", action)
		}
	}
	// Superusers were notified.
	raw, _ = json.Marshal(admin.must("GET", "/api/v1/activity", nil, 200))
	if !strings.Contains(string(raw), "profile.blacklisted_notice") {
		t.Fatalf("superuser not notified: %s", raw)
	}

	info := admin.must("GET", "/api/v1/info", nil, 200)
	if !strings.Contains(fmt.Sprint(info["features"]), wire.FeatureProfiles) {
		t.Fatalf("features: %v", info["features"])
	}
}
