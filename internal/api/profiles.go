// Owner: SRV-M (P1-20). Scope: api-v1 §6B Windows profiles, consent,
// blacklist (feature profiles.v1); MASTER_PLAN §7.8.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func init() { implementFeature(wire.FeatureProfiles) }

// mountProfiles registers the routes of this feature area.
func (s *Server) mountProfiles(rt routes) {
	rt.Dev.Put("/device/profiles", s.handleDeviceProfilesReport)
	rt.Dev.Post("/device/profiles/{sid}/security-event", s.handleProfileSecurityEvent)
	rt.Sess.Get("/devices/{id}/profiles", s.handleDeviceProfiles) // device owner, admin
	rt.Admin.Post("/devices/{id}/profiles/requests", s.handleProfileRequest)
	rt.Super.Post("/admin/device-profiles/{id}/clear", s.handleProfileClear)
}

// createProfileConsentTask queues a profile_consent_request device task and
// returns its id. INT may point it at the P1-17 device-task store; the
// default writes the task row directly.
var createProfileConsentTask = func(ctx context.Context, s *Server, deviceID, createdBy int64, payload wire.TaskProfileConsentRequestPayload) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	return s.st.QueueProfileConsentTask(ctx, deviceID, createdBy, raw)
}

// notifyProfileBlacklisted tells superusers about a blacklisted profile. The
// default writes an entry into each superuser's activity feed; SRV-7 may
// replace it with real notifications.
var notifyProfileBlacklisted = func(r *http.Request, s *Server, p *store.DeviceProfile) {
	ids, err := s.st.SuperuserIDs(r.Context())
	if err != nil {
		slog.Warn("profile blacklist: list superusers", "err", err)
		return
	}
	for _, id := range ids {
		s.activity(r, id, "profile.blacklisted_notice", profileTarget(p), p.BlacklistReason, false)
	}
}

// requireProfileAuthorised gates every profile-bound device operation
// (snapshot commit, sync, restore into a profile): nil only when the
// profile of (deviceID, sid) is active_default or authorised with a recorded
// consent. A blacklisted profile gets 423 profile_blacklisted; anything else
// (unknown, requested, cleared_pending_consent, revoked) gets
// 403 profile_not_authorised.
func (s *Server) requireProfileAuthorised(ctx context.Context, deviceID int64, sid string) error {
	_, err := s.authorisedProfile(ctx, deviceID, sid)
	return err
}

// requireProfileConsent is requireProfileAuthorised plus a check that
// consentID is the profile's active consent (snapshot commit
// profile_consent_id, api-v1 §5.2).
func (s *Server) requireProfileConsent(ctx context.Context, deviceID int64, sid, consentID string) error {
	p, err := s.authorisedProfile(ctx, deviceID, sid)
	if err != nil {
		return err
	}
	if consentID == "" || consentID != p.ConsentID {
		return errf(403, wire.CodeProfileNotAuthorised, "The consent for this Windows profile is not active")
	}
	return nil
}

func (s *Server) authorisedProfile(ctx context.Context, deviceID int64, sid string) (*store.DeviceProfile, error) {
	p, err := s.st.DeviceProfileBySID(ctx, deviceID, sid)
	if errors.Is(err, store.ErrNotFound) {
		return nil, errf(403, wire.CodeProfileNotAuthorised, "This Windows profile has not consented to VaultDock")
	}
	if err != nil {
		return nil, err
	}
	if p.State == wire.ProfileStateBlacklisted {
		return nil, errf(423, wire.CodeProfileBlacklisted, "This Windows profile is blacklisted after a security change; a superuser must clear it and the user must consent again")
	}
	if !p.Consented() {
		return nil, errf(403, wire.CodeProfileNotAuthorised, "This Windows profile has not consented to VaultDock")
	}
	return p, nil
}

// --- views and validation ---

func profileView(p *store.DeviceProfile) wire.Profile {
	opt := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	return wire.Profile{
		ID: p.ID, DeviceID: p.DeviceID, SID: p.SID, Username: p.Username, ProfilePath: p.ProfilePath,
		VaultDockUserID: p.VaultDockUserID, State: p.State, ConsentID: opt(p.ConsentID), ConsentAt: p.ConsentAt,
		RequestedByUserID: p.RequestedByUserID, RequestScope: opt(p.RequestScope),
		BlacklistedAt: p.BlacklistedAt, BlacklistReason: opt(p.BlacklistReason),
		ClearedBy: p.ClearedBy, ClearedAt: p.ClearedAt, UpdatedAt: p.UpdatedAt,
	}
}

func profileTarget(p *store.DeviceProfile) string {
	if p.Username != "" {
		return fmt.Sprintf("device %d %s (%s)", p.DeviceID, p.Username, p.SID)
	}
	return fmt.Sprintf("device %d %s", p.DeviceID, p.SID)
}

// sidPattern accepts Windows SIDs in string form (S-1-<authority>-<subs>).
var sidPattern = regexp.MustCompile(`^S-1-[0-9]{1,14}(-[0-9]{1,10}){1,15}$`)

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	maxReportProfiles = 256
	maxConsentIDLen   = 128
)

func validSID(sid string) bool { return len(sid) <= 184 && sidPattern.MatchString(sid) }

func oneOf(v string, set ...string) bool {
	for _, s := range set {
		if v == s {
			return true
		}
	}
	return false
}

var profileStates = []string{wire.ProfileStateActiveDefault, wire.ProfileStateRequested, wire.ProfileStateAuthorised,
	wire.ProfileStateBlacklisted, wire.ProfileStateClearedPendingConsent, wire.ProfileStateRevoked}

var profileSignals = []string{wire.ProfileSignalPasswordChanged, wire.ProfileSignalPasswordReset, wire.ProfileSignalSIDChanged,
	wire.ProfileSignalAccountDisabled, wire.ProfileSignalAccountRemoved, wire.ProfileSignalProfileMoved, wire.ProfileSignalConsentUnverifiable}

var profileEvidence = []string{wire.ProfileEvidenceLocalPasswordAge, wire.ProfileEvidenceDPAPIUnprotectFailed, wire.ProfileEvidenceLDAPPwdLastSet,
	wire.ProfileEvidenceEventLog4723, wire.ProfileEvidenceEventLog4724, wire.ProfileEvidenceSIDLookup}

func validationErr(fields map[string]string) error {
	return &apiError{Status: 422, Code: "validation", Message: "Invalid request", Details: map[string]any{"fields": fields}}
}

func validateProfileReport(rep *wire.ProfileReport) error {
	fields := map[string]string{}
	if len(rep.Profiles) > maxReportProfiles {
		fields["profiles"] = "too many profiles"
	}
	if len(rep.Consents) > maxReportProfiles {
		fields["consents"] = "too many consents"
	}
	seen := map[string]bool{}
	for i, p := range rep.Profiles {
		k := fmt.Sprintf("profiles[%d]", i)
		switch {
		case !validSID(p.SID):
			fields[k+".sid"] = "invalid SID"
		case seen[p.SID]:
			fields[k+".sid"] = "duplicate SID"
		}
		seen[p.SID] = true
		if len(p.Username) > 256 {
			fields[k+".username"] = "too long"
		}
		if len(p.ProfilePath) > 1024 {
			fields[k+".profile_path"] = "too long"
		}
		if !oneOf(p.State, profileStates...) {
			fields[k+".state"] = "unknown state"
		}
	}
	consentSeen := map[string]bool{}
	for i, c := range rep.Consents {
		k := fmt.Sprintf("consents[%d]", i)
		if !seen[c.SID] {
			fields[k+".sid"] = "consent for a profile missing from the report"
		} else if consentSeen[c.SID] {
			fields[k+".sid"] = "duplicate consent"
		}
		consentSeen[c.SID] = true
		if c.ConsentID == "" || len(c.ConsentID) > maxConsentIDLen {
			fields[k+".consent_id"] = "required, at most 128 characters"
		}
		if c.ConsentAt <= 0 {
			fields[k+".consent_at"] = "required"
		}
		if !hexDigest.MatchString(c.ProofDigest) {
			fields[k+".proof_digest"] = "must be 64 lowercase hex characters"
		}
	}
	if len(fields) > 0 {
		return validationErr(fields)
	}
	return nil
}

func (s *Server) profileList(ctx context.Context, deviceID int64, r *http.Request) (map[string]any, error) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 500 {
			return nil, errf(400, "bad_limit", "limit must be 1..500")
		}
		limit = n
	}
	var after int64
	if v := r.URL.Query().Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return nil, errf(400, "bad_cursor", "Invalid cursor")
		}
		after = n
	}
	list, err := s.st.ListDeviceProfiles(ctx, deviceID, after, limit+1)
	if err != nil {
		return nil, err
	}
	next := ""
	if len(list) > limit {
		list = list[:limit]
		next = strconv.FormatInt(list[limit-1].ID, 10)
	}
	items := make([]wire.Profile, 0, len(list))
	for i := range list {
		items = append(items, profileView(&list[i]))
	}
	return map[string]any{"items": items, "next_cursor": next}, nil
}

// --- handlers ---

// handleDeviceProfilesReport is PUT /device/profiles (dev). It replies with
// the server's view of the device's profiles (same shape as the session
// list), so the agent learns blacklist and clear decisions.
func (s *Server) handleDeviceProfilesReport(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var rep wire.ProfileReport
	if err := decode(r, &rep); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := validateProfileReport(&rep); err != nil {
		writeErr(w, r, err)
		return
	}
	changes, err := s.st.ApplyProfileReport(r.Context(), p.Device.ID, p.Device.UserID, rep)
	if errors.Is(err, store.ErrProfileState) {
		err = errf(400, "invalid_profile_state", "%s", err.Error())
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	for _, c := range changes {
		action := "profile.state_changed"
		if c.To == wire.ProfileStateActiveDefault || c.To == wire.ProfileStateAuthorised {
			action = "profile.consented"
		}
		from := c.From
		if from == "" {
			from = "new"
		}
		s.activity(r, p.Device.UserID, action, fmt.Sprintf("device %d %s", p.Device.ID, c.SID), from+" -> "+c.To, true)
	}
	out, err := s.profileList(r.Context(), p.Device.ID, r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

// handleProfileSecurityEvent is POST /device/profiles/{sid}/security-event
// (dev): the profile becomes blacklisted, its jobs are suspended and the
// superusers are notified. Repeated events are idempotent.
func (s *Server) handleProfileSecurityEvent(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	sid := chi.URLParam(r, "sid")
	if !validSID(sid) {
		writeErr(w, r, validationErr(map[string]string{"sid": "invalid SID"}))
		return
	}
	var req wire.ProfileSecurityEventRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	fields := map[string]string{}
	if !oneOf(req.Signal, profileSignals...) {
		fields["signal"] = "unknown signal"
	}
	if !oneOf(req.Evidence, profileEvidence...) {
		fields["evidence"] = "unknown evidence"
	}
	if req.ObservedAt <= 0 {
		fields["observed_at"] = "required"
	}
	if len(fields) > 0 {
		writeErr(w, r, validationErr(fields))
		return
	}
	prof, already, err := s.st.BlacklistProfile(r.Context(), p.Device.ID, sid, req.Signal, req.Evidence)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, p.Device.UserID, "profile.security_event", profileTarget(prof),
		fmt.Sprintf("signal=%s evidence=%s observed_at=%d", req.Signal, req.Evidence, req.ObservedAt), true)
	if !already {
		slog.Warn("profile blacklisted", "device", p.Device.ID, "sid", sid, "signal", req.Signal, "evidence", req.Evidence)
		notifyProfileBlacklisted(r, s, prof)
	}
	writeJSON(w, 200, profileView(prof))
}

// deviceForProfiles loads device id for its owner or an admin (404
// otherwise, like the other device routes).
func (s *Server) deviceForProfiles(r *http.Request) (*store.Device, error) {
	p := principal(r)
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	d, err := s.st.Device(r.Context(), id)
	if err != nil || (d.UserID != p.User.ID && !p.User.IsAdmin()) {
		return nil, store.ErrNotFound
	}
	return d, nil
}

// handleDeviceProfiles is GET /devices/{id}/profiles (sess: owner, admin).
func (s *Server) handleDeviceProfiles(w http.ResponseWriter, r *http.Request) {
	d, err := s.deviceForProfiles(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out, err := s.profileList(r.Context(), d.ID, r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

// handleProfileRequest is POST /devices/{id}/profiles/requests (admin): it
// records the request and queues a profile_consent_request task. Nothing is
// read until each Windows user consents locally.
func (s *Server) handleProfileRequest(w http.ResponseWriter, r *http.Request) {
	me := principal(r).User
	d, err := s.deviceForProfiles(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.ProfileConsentRequestCreate
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	scope := wire.ProfileRequestScopeSelected
	switch {
	case req.All && len(req.SIDs) > 0:
		writeErr(w, r, validationErr(map[string]string{"sids": "give either sids or all"}))
		return
	case req.All:
		scope = wire.ProfileRequestScopeAll
	case len(req.SIDs) == 0:
		writeErr(w, r, validationErr(map[string]string{"sids": "required unless all is true"}))
		return
	case len(req.SIDs) > maxReportProfiles:
		writeErr(w, r, validationErr(map[string]string{"sids": "too many SIDs"}))
		return
	}
	seen := map[string]bool{}
	var sids []string
	for i, sid := range req.SIDs {
		if !validSID(sid) {
			writeErr(w, r, validationErr(map[string]string{fmt.Sprintf("sids[%d]", i): "invalid SID"}))
			return
		}
		if !seen[sid] {
			seen[sid] = true
			sids = append(sids, sid)
		}
	}
	if err := s.requireModules(r.Context(), d.ID, wire.ModuleProfiles); err != nil {
		writeErr(w, r, err) // before any profile state changes
		return
	}
	pr, err := s.st.CreateProfileRequest(r.Context(), d.ID, me.ID, scope, sids)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	payload := wire.TaskProfileConsentRequestPayload{
		RequestID:   strconv.FormatInt(pr.ID, 10),
		SIDs:        wire.TaskSIDs{All: req.All, SIDs: sids},
		RequestedBy: me.Username,
	}
	taskID, err := createProfileConsentTask(r.Context(), s, d.ID, me.ID, payload)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.SetProfileRequestTask(r.Context(), pr.ID, taskID); err != nil {
		writeErr(w, r, err)
		return
	}
	target := "all"
	if !req.All {
		target = strings.Join(sids, ",")
	}
	s.activity(r, d.UserID, "profile.consent_requested", fmt.Sprintf("device %d", d.ID), target, true)
	writeJSON(w, 201, wire.DeviceTaskCreated{TaskID: taskID})
}

// handleProfileClear is POST /admin/device-profiles/{id}/clear (super). The
// profile moves to cleared_pending_consent; it is never re-enabled here.
// confirm_text must equal the profile's SID or Windows username.
func (s *Server) handleProfileClear(w http.ResponseWriter, r *http.Request) {
	me := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.ProfileClearRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	cur, err := s.st.DeviceProfile(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		err = errf(404, wire.CodeProfileNotFound, "Profile not found")
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	confirm := strings.TrimSpace(req.ConfirmText)
	if confirm == "" || (confirm != cur.SID && (cur.Username == "" || confirm != cur.Username)) {
		writeErr(w, r, validationErr(map[string]string{"confirm_text": "type the profile's username or SID to confirm"}))
		return
	}
	prof, err := s.st.ClearProfileBlacklist(r.Context(), id, me.ID)
	if errors.Is(err, store.ErrProfileState) {
		err = errf(409, "not_blacklisted", "The profile is not blacklisted")
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var owner int64
	if d, err := s.st.Device(r.Context(), prof.DeviceID); err == nil {
		owner = d.UserID
	}
	s.activity(r, owner, "profile.blacklist_cleared", profileTarget(prof), "reason="+prof.BlacklistReason+"; re-consent required", true)
	writeJSON(w, 200, profileView(prof))
}
