// Owner: SRV-M (P1-20). Windows profile records per device, consent and
// blacklist state (api-v1 §6B, MASTER_PLAN §7.8).

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// DeviceProfile is a row of device_profiles. Empty strings stand for the
// contract's null values (consent_id, request_scope, blacklist_reason).
type DeviceProfile struct {
	ID                int64
	DeviceID          int64
	SID               string
	Username          string
	ProfilePath       string
	VaultDockUserID   *int64
	State             string
	ConsentID         string
	ConsentAt         *int64
	ProofDigest       string // SHA-256 (hex) of the DPAPI-bound consent blob
	RequestedByUserID *int64
	RequestScope      string
	BlacklistedAt     *int64
	BlacklistReason   string
	BlacklistEvidence string
	ClearedBy         *int64
	ClearedAt         *int64
	CreatedAt         int64
	UpdatedAt         int64
}

// Consented reports whether the agent may touch the profile (§6B rule 2).
func (p *DeviceProfile) Consented() bool {
	return (p.State == wire.ProfileStateActiveDefault || p.State == wire.ProfileStateAuthorised) && p.ConsentID != ""
}

// ProfileTransition is one state change caused by a device report.
type ProfileTransition struct {
	SID, From, To string // From is "" for a new profile
}

// ProfileRequest is a row of profile_requests (admin consent request).
type ProfileRequest struct {
	ID                int64
	DeviceID          int64
	RequestedByUserID *int64
	Scope             string
	SIDs              []string
	TaskID            *int64
	Status            string
	CreatedAt         int64
}

var (
	// ErrProfileState is returned for a transition the profile's current
	// state does not allow (for example clearing a profile that is not
	// blacklisted, or a report claiming consent without a consent record).
	ErrProfileState = errors.New("invalid profile state")
)

const profileCols = "id, device_id, sid, username, profile_path, vaultdock_user_id, state, consent_id, consent_at, proof_digest, requested_by_user_id, request_scope, blacklisted_at, blacklist_reason, blacklist_evidence, cleared_by, cleared_at, created_at, updated_at"

func scanProfile(sc interface{ Scan(...any) error }) (*DeviceProfile, error) {
	var p DeviceProfile
	var vu, cat, rb, bat, cb, cla sql.NullInt64
	err := sc.Scan(&p.ID, &p.DeviceID, &p.SID, &p.Username, &p.ProfilePath, &vu, &p.State, &p.ConsentID, &cat, &p.ProofDigest,
		&rb, &p.RequestScope, &bat, &p.BlacklistReason, &p.BlacklistEvidence, &cb, &cla, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.VaultDockUserID, p.ConsentAt, p.RequestedByUserID = nullInt(vu), nullInt(cat), nullInt(rb)
	p.BlacklistedAt, p.ClearedBy, p.ClearedAt = nullInt(bat), nullInt(cb), nullInt(cla)
	return &p, nil
}

type profileQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func profileBySID(ctx context.Context, q profileQuerier, deviceID int64, sid string) (*DeviceProfile, error) {
	return scanProfile(q.QueryRowContext(ctx, "SELECT "+profileCols+" FROM device_profiles WHERE device_id = ? AND sid = ?", deviceID, sid))
}

// DeviceProfile returns a profile by id.
func (s *Store) DeviceProfile(ctx context.Context, id int64) (*DeviceProfile, error) {
	return scanProfile(s.DB.QueryRowContext(ctx, "SELECT "+profileCols+" FROM device_profiles WHERE id = ?", id))
}

// DeviceProfileBySID returns the profile of (device, sid).
func (s *Store) DeviceProfileBySID(ctx context.Context, deviceID int64, sid string) (*DeviceProfile, error) {
	return profileBySID(ctx, s.DB, deviceID, sid)
}

// ListDeviceProfiles lists a device's profiles by id, after afterID.
func (s *Store) ListDeviceProfiles(ctx context.Context, deviceID, afterID int64, limit int) ([]DeviceProfile, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+profileCols+" FROM device_profiles WHERE device_id = ? AND id > ? ORDER BY id LIMIT ?", deviceID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// setProfileJobs moves the device's jobs for sid between active and
// suspended_by_profile (api-v1 §6B: blacklisting suspends jobs).
func setProfileJobs(ctx context.Context, tx *sql.Tx, deviceID int64, sid string, suspend bool) error {
	from, to := "active", "suspended_by_profile"
	if !suspend {
		from, to = to, from
	}
	_, err := tx.ExecContext(ctx, `UPDATE backup_jobs SET state = ?, updated_at = ? WHERE device_id = ? AND profile_sid = ? AND state = ?`,
		to, now(), deviceID, sid, from)
	return err
}

func consented(state string) bool {
	return state == wire.ProfileStateActiveDefault || state == wire.ProfileStateAuthorised
}

// ApplyProfileReport stores the agent's full profile report (PUT
// /device/profiles) for a device owned by ownerID. The caller validates the
// format. Server-side rules:
//   - a blacklisted profile keeps its state (only a superuser clears it);
//   - a reported "blacklisted" state is honoured (it only restricts);
//   - a reported "cleared_pending_consent" never changes the server state;
//   - moving to active_default/authorised needs a consent record; from
//     cleared_pending_consent or revoked it needs a NEW consent (a different
//     consent_id, and for a cleared profile consent_at after cleared_at),
//     otherwise the server state is kept;
//   - a report claiming active_default/authorised for a profile with no
//     consent record at all fails with ErrProfileState.
//
// Profiles missing from the report are left unchanged.
func (s *Store) ApplyProfileReport(ctx context.Context, deviceID, ownerID int64, rep wire.ProfileReport) ([]ProfileTransition, error) {
	consents := map[string]wire.ProfileConsentRecord{}
	for _, c := range rep.Consents {
		consents[c.SID] = c
	}
	var changes []ProfileTransition
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		for _, e := range rep.Profiles {
			cur, err := profileBySID(ctx, tx, deviceID, e.SID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			c, hasConsent := consents[e.SID]
			if cur == nil {
				state := e.State
				if state == wire.ProfileStateClearedPendingConsent {
					return fmt.Errorf("%w: %s: unknown profile cannot be cleared", ErrProfileState, e.SID)
				}
				if consented(state) && !hasConsent {
					return fmt.Errorf("%w: %s: %s without a consent record", ErrProfileState, e.SID, state)
				}
				var owner, consentAt, blAt *int64
				consentID, digest := "", ""
				if consented(state) {
					consentID, digest, consentAt = c.ConsentID, c.ProofDigest, &c.ConsentAt
					if state == wire.ProfileStateActiveDefault {
						owner = &ownerID
					}
				}
				if state == wire.ProfileStateBlacklisted {
					blAt = &t
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO device_profiles(device_id, sid, username, profile_path, vaultdock_user_id, state, consent_id, consent_at, proof_digest, blacklisted_at, created_at, updated_at)
					VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, deviceID, e.SID, e.Username, e.ProfilePath, owner, state, consentID, consentAt, digest, blAt, t, t); err != nil {
					return err
				}
				if state == wire.ProfileStateBlacklisted {
					if err := setProfileJobs(ctx, tx, deviceID, e.SID, true); err != nil {
						return err
					}
				}
				changes = append(changes, ProfileTransition{SID: e.SID, To: state})
				continue
			}

			next := cur.State
			takeConsent := false
			switch {
			case cur.State == wire.ProfileStateBlacklisted:
				// Only a superuser clear (plus re-consent) leaves blacklisted.
			case e.State == wire.ProfileStateBlacklisted:
				next = wire.ProfileStateBlacklisted
			case e.State == wire.ProfileStateClearedPendingConsent:
				// Server-controlled state; the device only echoes it.
			case consented(e.State):
				fresh := hasConsent && c.ConsentID != cur.ConsentID
				switch cur.State {
				case wire.ProfileStateClearedPendingConsent:
					if fresh && cur.ClearedAt != nil && c.ConsentAt > *cur.ClearedAt {
						next, takeConsent = e.State, true
					}
				case wire.ProfileStateRevoked:
					if fresh {
						next, takeConsent = e.State, true
					}
				default:
					if !hasConsent && cur.ConsentID == "" {
						return fmt.Errorf("%w: %s: %s without a consent record", ErrProfileState, e.SID, e.State)
					}
					next, takeConsent = e.State, hasConsent
				}
			default: // requested, revoked: downgrades are always allowed
				next = e.State
			}

			q := `UPDATE device_profiles SET username = ?, profile_path = ?, state = ?, updated_at = ?`
			args := []any{e.Username, e.ProfilePath, next, t}
			if takeConsent {
				q += `, consent_id = ?, consent_at = ?, proof_digest = ?`
				args = append(args, c.ConsentID, c.ConsentAt, c.ProofDigest)
			}
			if next == wire.ProfileStateBlacklisted && cur.State != next {
				q += `, blacklisted_at = ?`
				args = append(args, t)
			}
			if next == wire.ProfileStateActiveDefault && cur.VaultDockUserID == nil {
				q += `, vaultdock_user_id = ?`
				args = append(args, ownerID)
			}
			q += ` WHERE id = ?`
			args = append(args, cur.ID)
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
			if next != cur.State {
				if next == wire.ProfileStateBlacklisted {
					if err := setProfileJobs(ctx, tx, deviceID, e.SID, true); err != nil {
						return err
					}
				} else if cur.State == wire.ProfileStateClearedPendingConsent && consented(next) {
					if err := setProfileJobs(ctx, tx, deviceID, e.SID, false); err != nil {
						return err
					}
				}
				changes = append(changes, ProfileTransition{SID: e.SID, From: cur.State, To: next})
			}
		}
		return nil
	})
	return changes, err
}

// BlacklistProfile handles a security event (POST
// /device/profiles/{sid}/security-event): the profile becomes blacklisted
// with reason = signal and its jobs are suspended. An unknown SID is recorded
// as a new blacklisted profile. already is true when the profile was
// blacklisted before; its original reason is kept unless it had none.
func (s *Store) BlacklistProfile(ctx context.Context, deviceID int64, sid, reason, evidence string) (p *DeviceProfile, already bool, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		cur, err := profileBySID(ctx, tx, deviceID, sid)
		switch {
		case errors.Is(err, ErrNotFound):
			if _, err := tx.ExecContext(ctx, `INSERT INTO device_profiles(device_id, sid, state, blacklisted_at, blacklist_reason, blacklist_evidence, created_at, updated_at)
				VALUES(?,?,?,?,?,?,?,?)`, deviceID, sid, wire.ProfileStateBlacklisted, t, reason, evidence, t, t); err != nil {
				return err
			}
		case err != nil:
			return err
		case cur.State == wire.ProfileStateBlacklisted:
			already = true
			if cur.BlacklistReason == "" {
				if _, err := tx.ExecContext(ctx, `UPDATE device_profiles SET blacklist_reason = ?, blacklist_evidence = ?, updated_at = ? WHERE id = ?`,
					reason, evidence, t, cur.ID); err != nil {
					return err
				}
			}
		default:
			if _, err := tx.ExecContext(ctx, `UPDATE device_profiles SET state = ?, blacklisted_at = ?, blacklist_reason = ?, blacklist_evidence = ?,
				cleared_by = NULL, cleared_at = NULL, updated_at = ? WHERE id = ?`,
				wire.ProfileStateBlacklisted, t, reason, evidence, t, cur.ID); err != nil {
				return err
			}
		}
		if err := setProfileJobs(ctx, tx, deviceID, sid, true); err != nil {
			return err
		}
		p, err = profileBySID(ctx, tx, deviceID, sid)
		return err
	})
	return p, already, err
}

// ClearProfileBlacklist moves a blacklisted profile to
// cleared_pending_consent (superuser clear). It never re-enables the profile:
// the Windows user must consent again, which a later device report applies.
// Jobs stay suspended until then.
func (s *Store) ClearProfileBlacklist(ctx context.Context, id, by int64) (*DeviceProfile, error) {
	var p *DeviceProfile
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		res, err := tx.ExecContext(ctx, `UPDATE device_profiles SET state = ?, cleared_by = ?, cleared_at = ?, updated_at = ? WHERE id = ? AND state = ?`,
			wire.ProfileStateClearedPendingConsent, by, t, t, id, wire.ProfileStateBlacklisted)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := scanProfile(tx.QueryRowContext(ctx, "SELECT "+profileCols+" FROM device_profiles WHERE id = ?", id)); err != nil {
				return err
			}
			return fmt.Errorf("%w: profile is not blacklisted", ErrProfileState)
		}
		p, err = scanProfile(tx.QueryRowContext(ctx, "SELECT "+profileCols+" FROM device_profiles WHERE id = ?", id))
		return err
	})
	return p, err
}

// CreateProfileRequest records an admin consent request for selected SIDs or
// all profiles (scope wire.ProfileRequestScope*). Known profiles in state
// revoked (and unknown selected SIDs) become "requested"; every targeted
// profile that is not consented or blacklisted records the requester.
// The device task is attached later with SetProfileRequestTask.
func (s *Store) CreateProfileRequest(ctx context.Context, deviceID, by int64, scope string, sids []string) (*ProfileRequest, error) {
	if sids == nil {
		sids = []string{}
	}
	raw, _ := json.Marshal(sids)
	pr := &ProfileRequest{DeviceID: deviceID, RequestedByUserID: &by, Scope: scope, SIDs: sids, Status: "pending"}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		pr.CreatedAt = t
		res, err := tx.ExecContext(ctx, `INSERT INTO profile_requests(device_id, requested_by_user_id, scope, sids, status, created_at, updated_at) VALUES(?,?,?,?,?,?,?)`,
			deviceID, by, scope, string(raw), pr.Status, t, t)
		if err != nil {
			return err
		}
		pr.ID, _ = res.LastInsertId()
		mark := func(where string, args ...any) error {
			_, err := tx.ExecContext(ctx, `UPDATE device_profiles SET requested_by_user_id = ?, request_scope = ?, updated_at = ?,
				state = CASE WHEN state = ? THEN ? ELSE state END
				WHERE device_id = ? AND state IN (?,?,?) `+where,
				append([]any{by, scope, t, wire.ProfileStateRevoked, wire.ProfileStateRequested, deviceID,
					wire.ProfileStateRevoked, wire.ProfileStateRequested, wire.ProfileStateClearedPendingConsent}, args...)...)
			return err
		}
		if scope == wire.ProfileRequestScopeAll {
			return mark("")
		}
		for _, sid := range sids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO device_profiles(device_id, sid, state, requested_by_user_id, request_scope, created_at, updated_at)
				VALUES(?,?,?,?,?,?,?) ON CONFLICT(device_id, sid) DO NOTHING`, deviceID, sid, wire.ProfileStateRequested, by, scope, t, t); err != nil {
				return err
			}
			if err := mark("AND sid = ?", sid); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pr, nil
}

// SetProfileRequestTask links the device task created for a request.
func (s *Store) SetProfileRequestTask(ctx context.Context, requestID, taskID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE profile_requests SET task_id = ?, updated_at = ? WHERE id = ?`, taskID, now(), requestID)
	return err
}

// QueueProfileConsentTask inserts a pending profile_consent_request device
// task for the device's owner (required module "profiles"). It is the
// interim task writer used until INT wires the P1-17 device-task store.
func (s *Store) QueueProfileConsentTask(ctx context.Context, deviceID, createdBy int64, payload []byte) (int64, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO device_tasks(user_id, device_id, kind, payload, required_modules, status, created_by, created_at)
		SELECT user_id, id, ?, ?, ?, ?, ?, ? FROM devices WHERE id = ?`,
		wire.TaskKindProfileConsentRequest, string(payload), `["`+wire.ModuleProfiles+`"]`, wire.TaskStatusPending, createdBy, t, deviceID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return res.LastInsertId()
}
