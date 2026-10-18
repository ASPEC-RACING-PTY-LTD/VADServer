// Owner: SRV-K (P1-13). Scope: api-v1 §5A repository keys (feature
// escrow.v1): GET /repos/{id}/keys/status, POST /repos/{id}/keys/rotate,
// PATCH /repos/{id} (escrow_mode change behind an escrow_change approval),
// key sharing to newly approved devices (key_share device tasks, rev 1.1b
// payloads), automatic rotation on device revocation and the escrow re-wrap
// after a SEK rotation (MASTER_PLAN §7.2).
//
// Design notes:
//   - repo_keys is a projection of the plaintext key files (repo_objects
//     kind keys), filled lazily by syncRepoKeys. A repository's current epoch
//     is the highest epoch with a key file (raised in repositories too).
//   - Full escrow: the server opens the current escrow wrap in memory
//     (audited, purpose rewrap), writes the new device's wrap itself and
//     sends the device key_share {repo_id, key_file_oid}. Metadata escrow, or
//     no server KEK: key_share {repo_id, target_device_id, target_box_pub,
//     target_sign_pub} goes to an existing device holding the current epoch.
//   - Epoch rotations (manual, escrow change, revocation) are delegated to a
//     device holding the current epoch with rewrap {repo_id, epoch: e+1};
//     the device writes the chain, the wraps and (for an escrow change) the
//     config. The server never generates a new epoch itself in v1.

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func init() {
	implementFeature(wire.FeatureEscrow)
	RegisterApprovalHandler(wire.ApprovalKindEscrowChange, ApprovalHandler{
		Digest: func(ctx context.Context, s *Server, a *store.Approval) (string, error) {
			// Only approvals created by PATCH /repos/{id} carry a target
			// mode; anything else is stale (409 plan_stale).
			var d escrowChangeData
			if err := json.Unmarshal(a.Data, &d); err != nil || d.EscrowMode == "" {
				return "", store.ErrNotFound
			}
			rp, err := s.st.Repository(ctx, a.SubjectID)
			if err != nil {
				return "", err
			}
			_, digest, err := store.PlanDigest(escrowChangePlan(rp, d.EscrowMode))
			return digest, err
		},
		Resume: func(ctx context.Context, s *Server, a *store.Approval, _ *OpProgress) (any, error) {
			return nil, s.applyEscrowChange(ctx, a)
		},
	})
	RegisterTaskResultHook(wire.TaskKindKeyShare, keyTaskResultHook)
	RegisterTaskResultHook(wire.TaskKindRewrap, keyTaskResultHook)
}

// mountRepoKeys registers the repository key routes (called from mountKeys).
func (s *Server) mountRepoKeys(rt routes) {
	rt.User.Get("/repos/{id}/keys/status", s.handleRepoKeyStatus)
	rt.Sess.Post("/repos/{id}/keys/rotate", s.handleRepoKeysRotate) // owner; 202 operation
	rt.Sess.Patch("/repos/{id}", s.handleRepoPatch)                 // owner; escrow_mode change -> 202 approval
}

// Limits of the key file projection.
const (
	maxKeyFileBytes = 64 << 10
	keySyncBatch    = 500
)

func errNoKeyHolder() *apiError {
	return errf(http.StatusConflict, "no_key_holder", "No approved device holds the current keys of this repository; connect one and try again")
}

// --- key file projection ---

// readKeyFile reads and decodes keys/<oid> of a repository.
func readKeyFile(ctx context.Context, bs repoObjectReader, rp *store.Repository, oid string) (*vdcrypt.KeyFile, error) {
	rc, _, err := bs.Get(ctx, rp.RepoUUID, wire.ObjectKindKeys, oid)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxKeyFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxKeyFileBytes || vdcrypt.OID(b) != oid {
		return nil, vdcrypt.ErrKeyFile
	}
	kf, err := vdcrypt.DecodeKeyFile(b)
	if err != nil {
		return nil, err
	}
	if kf.Repo != rp.RepoUUID {
		return nil, fmt.Errorf("%w: key file of another repository", vdcrypt.ErrKeyFile)
	}
	return kf, nil
}

// repoObjectReader is the part of backup.Store used here.
type repoObjectReader interface {
	Get(ctx context.Context, repoUUID, kind, oid string) (io.ReadCloser, int64, error)
}

// syncRepoKeys projects key files not yet in repo_keys and raises the
// repository's current epoch. Undecodable key files are skipped (logged).
func (s *Server) syncRepoKeys(ctx context.Context, rp *store.Repository) error {
	oids, err := s.st.UnprojectedKeyObjects(ctx, rp.ID, keySyncBatch)
	if err != nil || len(oids) == 0 {
		return err
	}
	bs, err := s.repoObjects(ctx, rp)
	if err != nil {
		return err
	}
	var top int64
	for _, oid := range oids {
		kf, err := readKeyFile(ctx, bs, rp, oid)
		if err != nil {
			slog.Warn("repository key file skipped", "repo", rp.ID, "oid", oid, "err", err)
			continue
		}
		if err := s.projectKeyFile(ctx, rp, oid, kf); err != nil {
			return err
		}
		if int64(kf.Epoch) > top {
			top = int64(kf.Epoch)
		}
	}
	if top > rp.CurrentEpoch {
		if err := s.st.RaiseRepoEpoch(ctx, rp.ID, top); err != nil {
			return err
		}
		rp.CurrentEpoch = top
	}
	return nil
}

func (s *Server) projectKeyFile(ctx context.Context, rp *store.Repository, oid string, kf *vdcrypt.KeyFile) error {
	k := &store.RepoKey{RepoID: rp.ID, KeyFileOID: oid, KeyID: kf.KeyID, Kind: string(kf.Kind), Epoch: int64(kf.Epoch),
		Label: cleanText(kf.Label, 200), RecipientPub: base64.StdEncoding.EncodeToString(kf.RecipientPub)}
	if kf.Device != nil {
		// Only devices of the repository owner are linked.
		if d, err := s.st.Device(ctx, kf.Device.ID); err == nil && d.UserID == rp.UserID {
			k.DeviceID = d.ID
		}
	}
	return s.st.AddRepoKey(ctx, k)
}

// writeKeyFile stores a server-written key file and projects it.
func (s *Server) writeKeyFile(ctx context.Context, rp *store.Repository, kf *vdcrypt.KeyFile) (string, error) {
	b, err := kf.Encode()
	if err != nil {
		return "", err
	}
	oid := vdcrypt.OID(b)
	bs, err := s.repoObjects(ctx, rp)
	if err != nil {
		return "", err
	}
	if err := bs.Put(ctx, rp.RepoUUID, wire.ObjectKindKeys, oid, bytes.NewReader(b), int64(len(b))); err != nil {
		return "", err
	}
	if _, err := s.st.PutRepoObject(ctx, rp.ID, wire.ObjectKindKeys, oid, int64(len(b))); err != nil {
		return "", err
	}
	return oid, s.projectKeyFile(ctx, rp, oid, kf)
}

// repoKeyState is the projection of one repository at its current epoch.
type repoKeyState struct {
	all     []store.RepoKey
	epoch   int64                     // 0: no key files yet
	devices map[int64]bool            // devices with a wrap at epoch
	escrow  []store.RepoKey           // escrow wraps at epoch
	byOID   map[string]*store.RepoKey // all rows
}

func (s *Server) repoKeyState(ctx context.Context, rp *store.Repository) (*repoKeyState, error) {
	if err := s.syncRepoKeys(ctx, rp); err != nil {
		return nil, err
	}
	all, err := s.st.ListRepoKeys(ctx, rp.ID)
	if err != nil {
		return nil, err
	}
	st := &repoKeyState{all: all, devices: map[int64]bool{}, byOID: map[string]*store.RepoKey{}}
	for i := range all {
		st.byOID[all[i].KeyFileOID] = &all[i]
		if all[i].Epoch > st.epoch {
			st.epoch = all[i].Epoch
		}
	}
	for _, k := range all {
		if k.Epoch != st.epoch {
			continue
		}
		switch k.Kind {
		case wire.KeyRecipientDevice:
			if k.DeviceID > 0 {
				st.devices[k.DeviceID] = true
			}
		case wire.KeyRecipientEscrow:
			st.escrow = append(st.escrow, k)
		}
	}
	return st, nil
}

// --- SIK pin ---

func normFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(s)))
}

// checkSIKPin answers 409 sik_mismatch to a device whose pinned SIK differs
// from the server's. Sessions, devices without a recorded pin and servers
// without key custody pass.
func (s *Server) checkSIKPin(ctx context.Context, p *Principal) error {
	if p == nil || p.Device == nil {
		return nil
	}
	dk, err := s.st.DeviceKey(ctx, p.Device.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && dk.SIKPinnedFingerprint == "") {
		return nil
	}
	if err != nil {
		return err
	}
	id, err := s.Keys().Identity()
	if err != nil {
		return nil
	}
	if normFingerprint(dk.SIKPinnedFingerprint) != normFingerprint(id.SIKFingerprint) {
		return errf(http.StatusConflict, wire.CodeSIKMismatch, "This device pinned a different server identity; re-pin it from the device with an administrator's confirmation")
	}
	return nil
}

// --- GET /repos/{id}/keys/status ---

func (s *Server) handleRepoKeyStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principal(r)
	if err := s.checkSIKPin(ctx, p); err != nil {
		writeErr(w, r, err)
		return
	}
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ks, err := s.repoKeyState(ctx, rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Devices that lack the current epoch get it shared (idempotent).
	if ks.epoch > 0 {
		s.shareRepoKeysAll(ctx, rp, ks)
	}
	cur := rp.CurrentEpoch
	if ks.epoch > cur {
		cur = ks.epoch
	}
	out := wire.KeyStatus{CurrentEpoch: cur, Recipients: []wire.KeyRecipient{}, EscrowMode: rp.EscrowMode}
	for _, k := range ks.all {
		out.Recipients = append(out.Recipients, wire.KeyRecipient{Kind: k.Kind, Label: k.Label, Epoch: k.Epoch, DeviceID: k.DeviceID})
		if k.Kind == wire.KeyRecipientRecovery && k.Epoch == cur {
			out.RecoveryWrappedCurrent = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// --- key sharing to approved devices ---

// shareKeysToDevice shares the current keys of every repository of the
// device's owner with a newly approved device. Errors are logged.
func (s *Server) shareKeysToDevice(ctx context.Context, d *store.Device) {
	repos, err := s.st.ListRepositories(ctx, d.UserID, 0, 500)
	if err != nil {
		slog.Error("key share: list repositories", "device", d.ID, "err", err)
		return
	}
	for i := range repos {
		rp := &repos[i]
		ks, err := s.repoKeyState(ctx, rp)
		if err != nil {
			slog.Error("key share: repository keys", "repo", rp.ID, "err", err)
			continue
		}
		if err := s.shareRepoKeys(ctx, rp, ks, d); err != nil {
			slog.Error("key share failed", "repo", rp.ID, "device", d.ID, "err", err)
		}
	}
}

// shareRepoKeysAll shares the current epoch with every approved device of
// the owner that has a box_pub and no wrap yet. Errors are logged.
func (s *Server) shareRepoKeysAll(ctx context.Context, rp *store.Repository, ks *repoKeyState) {
	devs, err := s.st.ActiveDeviceKeys(ctx, rp.UserID)
	if err != nil {
		slog.Error("key share: devices", "repo", rp.ID, "err", err)
		return
	}
	ids := make([]int64, 0, len(devs))
	for id := range devs {
		if !ks.devices[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		d, err := s.st.Device(ctx, id)
		if err != nil {
			continue
		}
		if err := s.shareRepoKeys(ctx, rp, ks, d); err != nil {
			slog.Error("key share failed", "repo", rp.ID, "device", id, "err", err)
		}
	}
}

// shareRepoKeys gives device d the repository's current epoch: under full
// escrow the server writes the device wrap and sends key_share {repo_id,
// key_file_oid}; otherwise an existing key holder gets key_share {repo_id,
// target_*}. Nothing happens when d already has a wrap, has no box_pub, the
// repository has no keys yet, or a key_share for (repo, d) is still open.
func (s *Server) shareRepoKeys(ctx context.Context, rp *store.Repository, ks *repoKeyState, d *store.Device) error {
	if ks.epoch == 0 || ks.devices[d.ID] || d.Status != "approved" || d.UserID != rp.UserID {
		return nil
	}
	dk, err := s.st.DeviceKey(ctx, d.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	open, err := s.openKeyShare(ctx, rp, d.ID)
	if err != nil || open {
		return err
	}
	box, err := decodeX25519(dk.BoxPub)
	if err != nil {
		return fmt.Errorf("device box_pub: %w", err)
	}
	sign, err := protocol.DecodePublicKey(d.PublicKey)
	if err != nil {
		return fmt.Errorf("device sign key: %w", err)
	}
	if rp.EscrowMode == wire.EscrowModeFull && len(ks.escrow) > 0 {
		oid, err := s.serverShare(ctx, rp, ks, d, box, sign)
		switch {
		case err == nil:
			ks.devices[d.ID] = true
			_, err = s.CreateTask(ctx, TaskRequest{UserID: rp.UserID, DeviceID: d.ID, Kind: wire.TaskKindKeyShare,
				Payload: wire.TaskKeySharePayload{RepoID: rp.ID, KeyFileOID: oid}})
			return err
		case errors.Is(err, keys.ErrUnavailable), errors.Is(err, keys.ErrMetaBundle), errors.Is(err, keys.ErrWrongServer):
			slog.Warn("key share: server cannot re-wrap, asking a device", "repo", rp.ID, "device", d.ID, "err", err)
		default:
			return err
		}
	}
	holder, err := s.keyHolder(ctx, rp, ks, d.ID)
	if err != nil {
		return err
	}
	if holder == nil {
		slog.Info("key share deferred: no device holds the current epoch", "repo", rp.ID, "device", d.ID)
		return nil
	}
	_, err = s.CreateTask(ctx, TaskRequest{UserID: rp.UserID, DeviceID: holder.ID, Kind: wire.TaskKindKeyShare,
		Payload: wire.TaskKeySharePayload{RepoID: rp.ID, TargetDeviceID: d.ID, TargetBoxPub: dk.BoxPub,
			TargetSignPub: protocol.EncodePublicKey(sign)}})
	return err
}

// serverShare re-wraps the current full-escrow bundle to d and stores the
// device key file.
func (s *Server) serverShare(ctx context.Context, rp *store.Repository, ks *repoKeyState, d *store.Device, box vdcrypt.Key, sign []byte) (string, error) {
	bs, err := s.repoObjects(ctx, rp)
	if err != nil {
		return "", err
	}
	uid, rid, did := rp.UserID, rp.ID, d.ID
	var last error = keys.ErrWrongServer
	for _, e := range ks.escrow {
		kf, err := readKeyFile(ctx, bs, rp, e.KeyFileOID)
		if err != nil {
			last = err
			continue
		}
		nf, err := s.Keys().RewrapToDevice(ctx, kf, keys.DeviceTarget{ID: d.ID, BoxPub: box, SignPub: sign, Label: d.Name},
			keys.Audit{Purpose: keys.PurposeRewrap, UserID: &uid, RepoID: &rid, ActorDeviceID: &did,
				Details: "key share to newly approved device"})
		if err != nil {
			last = err
			if errors.Is(err, keys.ErrUnavailable) {
				return "", err
			}
			continue
		}
		return s.writeKeyFile(ctx, rp, nf)
	}
	return "", last
}

// openKeyShare reports an unfinished key_share for (repo, target device).
func (s *Server) openKeyShare(ctx context.Context, rp *store.Repository, target int64) (bool, error) {
	open, err := s.st.OpenTasks(ctx, rp.UserID, wire.TaskKindKeyShare)
	if err != nil {
		return false, err
	}
	for _, t := range open {
		var p wire.TaskKeySharePayload
		if json.Unmarshal([]byte(t.Payload), &p) != nil || p.RepoID != rp.ID {
			continue
		}
		if p.TargetDeviceID == target || (p.TargetDeviceID == 0 && t.DeviceID == target) {
			return true, nil
		}
	}
	return false, nil
}

// keyHolder picks the approved device (other than exclude) holding the
// current epoch that was seen most recently, or nil.
func (s *Server) keyHolder(ctx context.Context, rp *store.Repository, ks *repoKeyState, exclude int64) (*store.Device, error) {
	var best *store.Device
	for id := range ks.devices {
		if id == exclude {
			continue
		}
		d, err := s.st.Device(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if d.Status != "approved" || d.UserID != rp.UserID {
			continue
		}
		if best == nil || seenAt(d) > seenAt(best) || (seenAt(d) == seenAt(best) && d.ID < best.ID) {
			best = d
		}
	}
	return best, nil
}

func seenAt(d *store.Device) int64 {
	if d.LastSeenAt != nil {
		return *d.LastSeenAt
	}
	return 0
}

func decodeX25519(s string) (vdcrypt.Key, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return vdcrypt.Key{}, err
	}
	k, err := vdcrypt.KeyFromBytes(b)
	if err == nil && k.IsZero() {
		err = vdcrypt.ErrKeyFile
	}
	return k, err
}

// keyTaskResultHook re-projects a repository after a key_share or rewrap
// result, so new wraps and epochs show up in keys/status.
func keyTaskResultHook(ctx context.Context, s *Server, t *store.DeviceTask) error {
	var p struct {
		RepoID int64 `json:"repo_id"`
	}
	if err := json.Unmarshal([]byte(t.Payload), &p); err != nil || p.RepoID == 0 {
		return err
	}
	rp, err := s.st.Repository(ctx, p.RepoID)
	if err != nil {
		return err
	}
	return s.syncRepoKeys(ctx, rp)
}

// --- rotation (delegated to a key-holding device) ---

// rotateResult is the operation result of POST /repos/{id}/keys/rotate.
type rotateResult struct {
	RepoID   int64 `json:"repo_id"`
	Epoch    int64 `json:"epoch"`
	TaskID   int64 `json:"task_id"`
	DeviceID int64 `json:"device_id"`
}

// requestRotation queues rewrap {repo_id, epoch: e+1} for a device holding
// epoch e (excluding exclude), reusing an open rewrap for the same target.
func (s *Server) requestRotation(ctx context.Context, rp *store.Repository, exclude int64, createdBy *int64) (*rotateResult, error) {
	ks, err := s.repoKeyState(ctx, rp)
	if err != nil {
		return nil, err
	}
	if ks.epoch == 0 {
		return nil, errf(http.StatusConflict, "repo_not_initialised", "This repository has no keys yet")
	}
	target := ks.epoch + 1
	open, err := s.st.OpenTasks(ctx, rp.UserID, wire.TaskKindRewrap)
	if err != nil {
		return nil, err
	}
	for _, t := range open {
		var p wire.TaskRewrapPayload
		if json.Unmarshal([]byte(t.Payload), &p) == nil && p.RepoID == rp.ID && p.Epoch >= target && t.DeviceID != exclude {
			return &rotateResult{RepoID: rp.ID, Epoch: p.Epoch, TaskID: t.ID, DeviceID: t.DeviceID}, nil
		}
	}
	holder, err := s.keyHolder(ctx, rp, ks, exclude)
	if err != nil {
		return nil, err
	}
	if holder == nil {
		return nil, errNoKeyHolder()
	}
	res, err := s.CreateTask(ctx, TaskRequest{UserID: rp.UserID, DeviceID: holder.ID, Kind: wire.TaskKindRewrap,
		Payload: wire.TaskRewrapPayload{RepoID: rp.ID, Epoch: target}, CreatedBy: createdBy})
	if err != nil {
		return nil, err
	}
	return &rotateResult{RepoID: rp.ID, Epoch: target, TaskID: res.TaskID, DeviceID: holder.ID}, nil
}

func (s *Server) handleRepoKeysRotate(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Fail fast when no device can rotate.
	ks, err := s.repoKeyState(r.Context(), rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if h, err := s.keyHolder(r.Context(), rp, ks, 0); err != nil || h == nil {
		if err == nil {
			err = errNoKeyHolder()
		}
		writeErr(w, r, err)
		return
	}
	uid := principal(r).User.ID
	s.activity(r, rp.UserID, "repo.keys.rotate", rp.Name, fmt.Sprintf("repo_id=%d", rp.ID), true)
	s.acceptOperation(w, r, wire.OperationKindRotateKeys, map[string]any{"repo_id": rp.ID, "reason": "manual"},
		func(ctx context.Context, prog *OpProgress) (any, error) {
			cur, err := s.st.Repository(ctx, rp.ID)
			if err != nil {
				return nil, err
			}
			res, err := s.requestRotation(ctx, cur, 0, &uid)
			if err != nil {
				return nil, err
			}
			prog.Set(1, 1, 0)
			return res, nil
		})
}

// rotateAfterRevoke asks a remaining key holder to rotate every repository
// of the revoked device's owner (backup-format §4.5: revocation is a
// mandatory trigger). Errors are logged.
func (s *Server) rotateAfterRevoke(ctx context.Context, d *store.Device) {
	repos, err := s.st.ListRepositories(ctx, d.UserID, 0, 500)
	if err != nil {
		slog.Error("revocation rotation: list repositories", "device", d.ID, "err", err)
		return
	}
	for i := range repos {
		res, err := s.requestRotation(ctx, &repos[i], d.ID, nil)
		var ae *apiError
		if errors.As(err, &ae) && (ae.Code == "repo_not_initialised" || ae.Code == "no_key_holder") {
			if ae.Code == "no_key_holder" {
				slog.Warn("revocation rotation pending: no other device holds the keys", "repo", repos[i].ID, "device", d.ID)
			}
			continue
		}
		if err != nil {
			slog.Error("revocation rotation failed", "repo", repos[i].ID, "device", d.ID, "err", err)
			continue
		}
		slog.Info("revocation rotation queued", "repo", res.RepoID, "epoch", res.Epoch, "task_id", res.TaskID)
	}
}

// --- PATCH /repos/{id}: escrow mode change ---

type escrowChangeData struct {
	EscrowMode string `json:"escrow_mode"`
}

func escrowChangePlan(rp *store.Repository, to string) map[string]any {
	return map[string]any{"repo_id": rp.ID, "repo_uuid": rp.RepoUUID, "from": rp.EscrowMode, "to": to}
}

func (s *Server) handleRepoPatch(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.EscrowChangeRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.EscrowMode != wire.EscrowModeFull && req.EscrowMode != wire.EscrowModeMetadata {
		writeErr(w, r, repoValidation(map[string]string{"escrow_mode": "must be full or metadata"}))
		return
	}
	if req.EscrowMode == rp.EscrowMode {
		writeErr(w, r, errf(http.StatusConflict, "no_change", "The repository already uses this escrow mode"))
		return
	}
	ks, err := s.repoKeyState(r.Context(), rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if h, err := s.keyHolder(r.Context(), rp, ks, 0); err != nil || h == nil {
		if err == nil {
			err = errNoKeyHolder()
		}
		writeErr(w, r, err)
		return
	}
	s.activity(r, rp.UserID, "repo.escrow_change.requested", rp.Name, rp.EscrowMode+" -> "+req.EscrowMode, true)
	s.acceptApproval(w, r, ApprovalRequest{
		UserID: rp.UserID, Kind: wire.ApprovalKindEscrowChange, SubjectType: "repository", SubjectID: rp.ID,
		SubjectName: rp.Name, Risk: wire.ApprovalRiskHigh,
		Summary: fmt.Sprintf("Change the key escrow of %q from %s to %s; this rotates the repository keys", rp.Name, rp.EscrowMode, req.EscrowMode),
		Plan:    escrowChangePlan(rp, req.EscrowMode), Data: escrowChangeData{EscrowMode: req.EscrowMode},
	})
}

// applyEscrowChange runs an approved escrow_change: it records the new mode
// and asks a key holder to rotate (the device rewrites config and writes the
// new epoch's escrow wrap for the new mode).
func (s *Server) applyEscrowChange(ctx context.Context, a *store.Approval) error {
	var d escrowChangeData
	if err := json.Unmarshal(a.Data, &d); err != nil {
		return err
	}
	rp, err := s.st.Repository(ctx, a.SubjectID)
	if err != nil {
		return err
	}
	if err := s.st.SetRepoEscrowMode(ctx, rp.ID, d.EscrowMode); err != nil {
		return err
	}
	rp.EscrowMode = d.EscrowMode
	_, err = s.requestRotation(ctx, rp, 0, a.DecidedBy)
	return err
}

// --- SEK rotation: re-wrap the current epoch's escrow wraps ---

// rewrapEscrowAfterSEKRotation writes a new escrow wrap under the current
// SEK for the current epoch of every repository whose escrow wrap names an
// older SEK (MASTER_PLAN §7.2). Old wraps stay (retired SEKs stay openable).
// It returns the number of key files written; failures are logged and
// skipped.
func (s *Server) rewrapEscrowAfterSEKRotation(ctx context.Context, opID, actorUserID int64) int {
	ids, err := s.st.AllRepositoryIDs(ctx)
	if err != nil {
		slog.Error("escrow re-wrap: list repositories", "err", err)
		return 0
	}
	n := 0
	for _, id := range ids {
		rp, err := s.st.Repository(ctx, id)
		if err != nil {
			continue
		}
		ks, err := s.repoKeyState(ctx, rp)
		if err != nil {
			slog.Error("escrow re-wrap: repository keys", "repo", id, "err", err)
			continue
		}
		if len(ks.escrow) == 0 {
			continue
		}
		bs, err := s.repoObjects(ctx, rp)
		if err != nil {
			slog.Error("escrow re-wrap: storage", "repo", id, "err", err)
			continue
		}
		uid, rid := rp.UserID, rp.ID
		a := keys.Audit{Purpose: keys.PurposeRotation, ActorUserID: &actorUserID, UserID: &uid, RepoID: &rid, OperationID: &opID,
			Details: "escrow re-wrap after SEK rotation"}
		current := false
		var files []*vdcrypt.KeyFile
		for _, e := range ks.escrow {
			kf, err := readKeyFile(ctx, bs, rp, e.KeyFileOID)
			if err != nil {
				continue
			}
			if _, srv, err := s.Keys().CurrentSEK(); err == nil && kf.Server != nil && kf.Server.SEKVersion == srv.SEKVersion {
				current = true
				break
			}
			files = append(files, kf)
		}
		if current {
			continue
		}
		for _, kf := range files {
			nf, err := s.Keys().RewrapEscrow(ctx, kf, a)
			if err != nil {
				slog.Error("escrow re-wrap failed", "repo", id, "key_id", kf.KeyID, "err", err)
				continue
			}
			if nf == nil {
				break
			}
			if _, err := s.writeKeyFile(ctx, rp, nf); err != nil {
				slog.Error("escrow re-wrap: store key file", "repo", id, "err", err)
				continue
			}
			n++
			break
		}
	}
	return n
}
