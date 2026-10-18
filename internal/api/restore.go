// Owner: SRV-3 (Phase 2, P2-06). Scope: api-v1 §7 restore plus the rev 1.1c
// amendment (device routes, cancel, list). Paths per rev 1.0
// (sdk/contracts/history/api-v1-rev1.0.md §7).
//
// Flow: POST /restore/plan computes a plan from the encrypted catalog;
// POST /restore/execute stores the restore (awaiting_approval) behind an
// approval of kind restore decided by the owner; on approval a restore
// device task is queued; the device fetches the restore, runs it and
// reports progress and items until a terminal status.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/restore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// planKindRestore is the store plan kind of restore plans.
const planKindRestore = "restore"

func init() {
	implementFeature(wire.FeatureRestore)
	RegisterApprovalHandler(wire.ApprovalKindRestore, ApprovalHandler{
		Resume: func(ctx context.Context, s *Server, a *store.Approval, _ *OpProgress) (any, error) {
			return nil, s.queueRestore(ctx, a.SubjectID)
		},
		Rejected: func(ctx context.Context, s *Server, a *store.Approval) error {
			_, _, err := s.st.CancelRestore(ctx, a.SubjectID, "the restore was not approved")
			if errors.Is(err, store.ErrRestoreFinal) {
				return nil
			}
			return err
		},
	})
}

// mountRestore registers the routes of this feature area.
func (s *Server) mountRestore(rt routes) {
	rt.Sess.Post("/restore/plan", s.handleRestorePlan)
	rt.Sess.Post("/restore/execute", s.handleRestoreExecute) // {plan_token}
	rt.Sess.Get("/restore", s.handleListRestores)
	rt.Sess.Get("/restore/{id}", s.handleGetRestore)
	rt.Sess.Get("/restore/{id}/items", s.handleRestoreItems)
	rt.Sess.Post("/restore/{id}/cancel", s.handleCancelRestore)
	rt.Dev.Get("/device/restores/{id}", s.handleDeviceRestore)
	rt.Dev.Post("/device/restores/{id}/progress", s.handleRestoreProgress)
}

func errRestoreNotFound() *apiError { return errf(404, "not_found", "Restore not found") }

// storedRestorePlan is what a restore plan token stores: the plan the
// caller sees plus the normalised request (clients ignore the extra field).
type storedRestorePlan struct {
	wire.RestorePlan
	Request wire.RestorePlanRequest `json:"request"`
}

// guardRoots are Windows locations a restore never writes to (api-v1
// §10.3). The device's guard is authoritative; the server only reports what
// it can see from the plan (override targets and system tokens).
var guardRoots = []struct{ prefix, reason string }{
	{`\windows`, "Windows folder"},
	{`\program files`, "Program Files"},
	{`\program files (x86)`, "Program Files"},
	{`\programdata\microsoft`, "ProgramData\\Microsoft"},
	{`\system volume information`, "System Volume Information"},
	{`\$recycle.bin`, "Recycle Bin"},
}

// guardReason returns why the server knows a target path is protected.
func guardReason(p string) string {
	p = strings.ToLower(strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`))
	if len(p) == 2 && p[1] == ':' {
		return "drive root"
	}
	if len(p) >= 2 && p[1] == ':' {
		rest := p[2:]
		for _, g := range guardRoots {
			if rest == g.prefix || strings.HasPrefix(rest, g.prefix+`\`) {
				return g.reason
			}
		}
	}
	return ""
}

// protectedTokens are snapshot tokens whose targets are always protected.
var protectedTokens = map[string]string{
	snapshot.TokenProgramFiles:    "Program Files",
	snapshot.TokenProgramFilesX86: "Program Files",
}

// restoreSnapshot loads a snapshot of the caller's.
func (s *Server) restoreSnapshot(ctx context.Context, userID, id int64) (*store.Snapshot, error) {
	sn, err := s.st.GetSnapshot(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sn.UserID != userID) {
		return nil, errSnapshotNotFound()
	}
	return sn, err
}

// computeRestorePlan validates req for userID and builds the plan. req is
// normalised in place (defaults filled).
func (s *Server) computeRestorePlan(ctx context.Context, userID int64, req *wire.RestorePlanRequest) (*storedRestorePlan, *store.Snapshot, error) {
	sn, err := s.restoreSnapshot(ctx, userID, req.SnapshotID)
	if err != nil {
		return nil, nil, err
	}
	dev, err := s.st.Device(ctx, req.TargetDeviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (dev.UserID != userID || dev.Status != "approved")) {
		return nil, nil, errf(404, "not_found", "Target device not found")
	}
	if err != nil {
		return nil, nil, err
	}
	if req.TargetProfileSID == "" {
		req.TargetProfileSID = req.TargetUser.SID
	}
	if req.TargetProfileSID == "" {
		req.TargetProfileSID = sn.ProfileSID
	}
	if req.TargetProfileSID == "" {
		return nil, nil, validation("target_profile_sid", "choose the Windows profile to restore into")
	}
	prof, err := s.st.DeviceProfileBySID(ctx, dev.ID, req.TargetProfileSID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, errf(403, wire.CodeProfileNotAuthorised, "That Windows profile has not allowed VaultDock on the target device")
	}
	if err != nil {
		return nil, nil, err
	}
	if prof.State == wire.ProfileStateBlacklisted {
		return nil, nil, errf(423, wire.CodeProfileBlacklisted, "That Windows profile is blocked until a superuser clears it")
	}
	if !prof.Consented() {
		return nil, nil, errf(403, wire.CodeProfileNotAuthorised, "That Windows profile has not allowed VaultDock on the target device")
	}
	if req.ConflictPolicy == "" {
		req.ConflictPolicy = wire.RestoreConflictRename
	}
	if !restore.ValidPolicy(req.ConflictPolicy) {
		return nil, nil, validation("conflict_policy", "must be skip, overwrite, rename or newer")
	}
	sel, err := restore.NewSelector(req.Selection)
	if err != nil {
		return nil, nil, validation("selection", "%s", err.Error())
	}
	mapper, err := restore.NewMapper(nil, req.Mapping.Overrides)
	if err != nil {
		return nil, nil, validation("mapping.overrides", "%s", err.Error())
	}
	if req.Selection == nil {
		req.Selection = []wire.RestoreSelection{}
	}
	if req.Mapping.Overrides == nil {
		req.Mapping.Overrides = []wire.RestorePathMapping{}
	}
	if req.ReinstallApps == nil {
		req.ReinstallApps = []string{}
	}
	switch sn.IngestState {
	case wire.SnapshotIngestDone:
	case wire.SnapshotIngestPending:
		return nil, nil, errf(409, wire.CodeCatalogPending, "The snapshot is still being indexed; try again shortly")
	default:
		return nil, nil, errf(409, wire.CodeCatalogPending, "The snapshot's file list is not available on the server; restore it from the device instead")
	}

	plan := &storedRestorePlan{Request: *req}
	p := &plan.RestorePlan
	p.Mappings, p.Actions, p.Warnings, p.NotPortable, p.Guard = []wire.RestorePlanMapping{}, []wire.RestorePlanAction{}, []wire.RestoreWarning{}, []string{}, []wire.RestoreGuardItem{}
	tokens := map[string]bool{}
	guarded := map[string]bool{}
	err = s.Catalog().WalkAll(ctx, sn, func(e wire.CatalogEntry) error {
		if e.Type != wire.CatalogEntryFile || !sel.Selected(e.Path) {
			return nil
		}
		tok, _, err := snapshot.SplitTokenPath(e.Path)
		if err != nil {
			return nil
		}
		tokens[tok] = true
		target, terr := mapper.Target(e.Path)
		if terr != nil {
			target = "" // resolved by the device for the target profile
		}
		reason := protectedTokens[tok]
		if reason == "" && target != "" {
			reason = guardReason(target)
		}
		if reason != "" {
			if len(p.Guard) < wire.RestorePlanActionsMax && !guarded[e.Path] {
				guarded[e.Path] = true
				p.Guard = append(p.Guard, wire.RestoreGuardItem{Path: e.Path, Reason: reason})
			}
			return nil
		}
		p.Summary.Files++
		p.Summary.Bytes += e.Size
		if len(p.Actions) < wire.RestorePlanActionsMax {
			p.Actions = append(p.Actions, wire.RestorePlanAction{Path: e.Path, TargetPath: target, Action: wire.RestoreActionRestore, Size: e.Size})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, 0, len(tokens))
	for t := range tokens {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		m := wire.RestorePlanMapping{Token: t, Auto: true}
		if t == snapshot.TokenProfile {
			m.From = sn.ProfilePath
		}
		p.Mappings = append(p.Mappings, m)
	}
	if req.TargetProfileSID != sn.ProfileSID && sn.ProfileSID != "" {
		p.Warnings = append(p.Warnings, wire.RestoreWarning{Code: "other_profile", Message: "Files are restored into a different Windows profile than they were backed up from"})
	}
	if len(req.ReinstallApps) > 0 {
		p.Warnings = append(p.Warnings, wire.RestoreWarning{Code: wire.CodeModuleUnavailable, Message: "Reinstalling apps is not available yet; only files are restored"})
		p.NotPortable = append(p.NotPortable, req.ReinstallApps...)
	}
	if req.IncludeRegistry {
		p.Warnings = append(p.Warnings, wire.RestoreWarning{Code: "registry_unsupported", Message: "Settings (registry) restore is not available yet"})
	}
	if p.Summary.Files == 0 {
		p.Warnings = append(p.Warnings, wire.RestoreWarning{Code: "nothing_selected", Message: "The selection contains no files that can be restored"})
	}
	return plan, sn, nil
}

func (s *Server) handleRestorePlan(w http.ResponseWriter, r *http.Request) {
	var req wire.RestorePlanRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	plan, sn, err := s.computeRestorePlan(r.Context(), principal(r).User.ID, &req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp, err := s.createPlan(r, planKindRestore, "snapshot", &sn.ID, plan, 0)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleRestoreExecute(w http.ResponseWriter, r *http.Request) {
	var req wire.PlanExecuteRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	p := principal(r)
	var plan *storedRestorePlan
	_, err := s.executePlan(r, planKindRestore, req.PlanToken, func(sp *store.Plan) (any, error) {
		var stored storedRestorePlan
		if err := jsonUnmarshal(sp.Plan, &stored); err != nil {
			return nil, err
		}
		cur, _, err := s.computeRestorePlan(r.Context(), p.User.ID, &stored.Request)
		if err != nil {
			return nil, err
		}
		plan = cur
		return cur, nil
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if plan.Summary.Files == 0 {
		writeErr(w, r, validation("selection", "the selection contains no files that can be restored"))
		return
	}
	pr := plan.Request
	uid := p.User.ID
	planJSON, _ := jsonMarshal(plan.RestorePlan)
	guardJSON, _ := jsonMarshal(plan.Guard)
	rs := &store.Restore{UserID: uid, DeviceID: &pr.TargetDeviceID, SnapshotID: &pr.SnapshotID, TargetProfileSID: pr.TargetProfileSID,
		ConflictPolicy: pr.ConflictPolicy, Plan: string(planJSON), Guard: string(guardJSON), CreatedBy: &uid,
		FilesTotal: plan.Summary.Files, BytesTotal: plan.Summary.Bytes,
		Options: store.RestoreOptions{Selection: pr.Selection, Overrides: pr.Mapping.Overrides, Mappings: plan.Mappings}}
	if err := s.st.CreateRestore(r.Context(), rs); err != nil {
		writeErr(w, r, err)
		return
	}
	dev, _ := s.st.Device(r.Context(), pr.TargetDeviceID)
	devName := fmt.Sprintf("device %d", pr.TargetDeviceID)
	if dev != nil {
		devName = dev.Name
	}
	a, err := s.createApproval(r.Context(), ApprovalRequest{UserID: uid, Kind: wire.ApprovalKindRestore, SubjectType: wire.ApprovalSubjectRestore,
		SubjectID: rs.ID, SubjectName: fmt.Sprintf("Restore %d", rs.ID), Risk: wire.ApprovalRiskMedium, Plan: plan.RestorePlan,
		Summary:     fmt.Sprintf("Restore %d files (%d bytes) from snapshot %d to %s", plan.Summary.Files, plan.Summary.Bytes, pr.SnapshotID, devName),
		RequestedBy: wire.ApprovalRequestedByUser, RequestedByUserID: &uid})
	if err != nil {
		_, _, _ = s.st.CancelRestore(context.WithoutCancel(r.Context()), rs.ID, "approval could not be created")
		writeErr(w, r, err)
		return
	}
	if err := s.st.SetRestoreApproval(r.Context(), rs.ID, a.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, uid, "restore.request", "restore", fmt.Sprintf("restore_id=%d snapshot_id=%d device_id=%d files=%d", rs.ID, pr.SnapshotID, pr.TargetDeviceID, plan.Summary.Files), true)
	writeJSON(w, http.StatusAccepted, wire.RestoreExecuteResponse{RestoreID: rs.ID, ApprovalID: a.ID})
}

// queueRestore creates the device task of an approved restore.
func (s *Server) queueRestore(ctx context.Context, id int64) error {
	rs, err := s.st.RestoreByID(ctx, id)
	if err != nil {
		return err
	}
	if rs.Status != wire.RestoreStatusAwaitingApproval || rs.DeviceID == nil {
		return errf(409, wire.CodeConflict, "The restore is no longer waiting for approval")
	}
	by := rs.UserID
	res, err := s.CreateTask(ctx, TaskRequest{UserID: rs.UserID, DeviceID: *rs.DeviceID, Kind: wire.TaskKindRestore,
		Payload: wire.TaskRestorePayload{RestoreID: rs.ID}, CreatedBy: &by})
	if err != nil {
		_, _, _ = s.st.CancelRestore(context.WithoutCancel(ctx), id, "the device task could not be queued: "+err.Error())
		return err
	}
	if err := s.st.QueueRestore(ctx, id, res.TaskID); err != nil {
		_ = s.st.CancelTask(context.WithoutCancel(ctx), res.TaskID, "restore no longer waiting")
		return err
	}
	return nil
}

// ownRestore loads a restore of the session user.
func (s *Server) ownRestore(r *http.Request) (*store.Restore, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	rs, err := s.st.RestoreByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rs.UserID != principal(r).User.ID) {
		return nil, errRestoreNotFound()
	}
	return rs, err
}

func (s *Server) handleListRestores(w http.ResponseWriter, r *http.Request) {
	before, limit, err := listParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListRestores(r.Context(), principal(r).User.ID, before, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Restore]{Items: make([]wire.Restore, 0, len(list))}
	for i := range list {
		out.Items = append(out.Items, list[i].Wire())
	}
	if len(list) == limit && limit > 0 {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetRestore(w http.ResponseWriter, r *http.Request) {
	rs, err := s.ownRestore(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, rs.Wire())
}

func (s *Server) handleRestoreItems(w http.ResponseWriter, r *http.Request) {
	rs, err := s.ownRestore(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	var after int64
	if c := q.Get("cursor"); c != "" {
		if after, err = strconv.ParseInt(c, 10, 64); err != nil || after < 0 {
			writeErr(w, r, validation("cursor", "invalid cursor"))
			return
		}
	}
	limit := wire.ListDefaultLimit
	if l := q.Get("limit"); l != "" {
		if limit, err = strconv.Atoi(l); err != nil || limit < 1 || limit > wire.ListMaxLimit {
			writeErr(w, r, validation("limit", "must be 1 to %d", wire.ListMaxLimit))
			return
		}
	}
	status := q.Get("status")
	if status != "" && !validItemStatus(status) {
		writeErr(w, r, validation("status", "unknown item status"))
		return
	}
	rows, err := s.st.RestoreItems(r.Context(), rs.ID, status, after, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.RestoreItem]{Items: make([]wire.RestoreItem, 0, len(rows))}
	for _, it := range rows {
		out.Items = append(out.Items, it.RestoreItem)
	}
	if len(rows) == limit {
		out.NextCursor = strconv.FormatInt(rows[len(rows)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCancelRestore(w http.ResponseWriter, r *http.Request) {
	rs, err := s.ownRestore(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	before, after, err := s.st.CancelRestore(ctx, rs.ID, "cancelled by the user")
	if errors.Is(err, store.ErrRestoreFinal) {
		writeErr(w, r, errf(409, wire.CodeConflict, "The restore has already finished"))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	switch before.Status {
	case wire.RestoreStatusAwaitingApproval:
		if before.ApprovalID != nil {
			if err := s.st.ExpireApproval(ctx, *before.ApprovalID); err != nil && !errors.Is(err, store.ErrApprovalNotPending) {
				slog.Warn("restore cancel: approval not expired", "restore_id", rs.ID, "err", err)
			}
		}
	case wire.RestoreStatusQueued:
		if before.TaskID != nil {
			if err := s.st.CancelTask(ctx, *before.TaskID, "restore cancelled"); err != nil {
				slog.Warn("restore cancel: task not cancelled", "restore_id", rs.ID, "err", err)
			}
		}
	}
	s.activity(r, rs.UserID, "restore.cancel", "restore", fmt.Sprintf("restore_id=%d", rs.ID), true)
	writeJSON(w, 200, after.Wire())
}

// deviceRestore loads a restore whose target is the calling device.
func (s *Server) deviceRestore(r *http.Request) (*store.Restore, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	p := principal(r)
	rs, err := s.st.RestoreByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (p.Device == nil || rs.DeviceID == nil || *rs.DeviceID != p.Device.ID)) {
		return nil, errRestoreNotFound()
	}
	return rs, err
}

func (s *Server) handleDeviceRestore(w http.ResponseWriter, r *http.Request) {
	rs, err := s.deviceRestore(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if rs.SnapshotID == nil {
		writeErr(w, r, errSnapshotNotFound())
		return
	}
	sn, err := s.st.GetSnapshot(r.Context(), *rs.SnapshotID)
	if errors.Is(err, store.ErrNotFound) {
		err = errSnapshotNotFound()
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rp, err := s.st.Repository(r.Context(), sn.RepoID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.DeviceRestore{Restore: rs.Wire(), RepoID: rp.ID, RepoUUID: rp.RepoUUID, SnapshotOID: sn.SnapshotOID,
		Selection: rs.Options.Selection, Mappings: rs.Options.Mappings, Overrides: rs.Options.Overrides,
		TargetProfileSID: rs.TargetProfileSID, ConflictPolicy: rs.ConflictPolicy, CancelRequested: rs.CancelRequested}
	if out.Selection == nil {
		out.Selection = []wire.RestoreSelection{}
	}
	if out.Mappings == nil {
		out.Mappings = []wire.RestorePlanMapping{}
	}
	if out.Overrides == nil {
		out.Overrides = []wire.RestorePathMapping{}
	}
	writeJSON(w, 200, out)
}

func validItemStatus(s string) bool {
	switch s {
	case wire.RestoreItemPending, wire.RestoreItemDone, wire.RestoreItemSkipped, wire.RestoreItemFailed, wire.RestoreItemBlocked:
		return true
	}
	return false
}

func (s *Server) handleRestoreProgress(w http.ResponseWriter, r *http.Request) {
	rs, err := s.deviceRestore(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var p wire.RestoreProgress
	if err := decode(r, &p); err != nil {
		writeErr(w, r, err)
		return
	}
	if p.Status != wire.RestoreStatusRunning && !store.RestoreTerminal(p.Status) {
		writeErr(w, r, validation("status", "must be running, completed, partial, failed or cancelled"))
		return
	}
	if len(p.Items) > wire.RestoreProgressItemsMax {
		writeErr(w, r, validation("items", "at most %d items per report", wire.RestoreProgressItemsMax))
		return
	}
	if p.FilesDone < 0 || p.FilesTotal < 0 || p.BytesDone < 0 || p.BytesTotal < 0 {
		writeErr(w, r, validation("files_done", "counters must not be negative"))
		return
	}
	for i, it := range p.Items {
		if it.Path == "" || !validItemStatus(it.Status) {
			writeErr(w, r, validation(fmt.Sprintf("items[%d]", i), "needs a path and a known status"))
			return
		}
	}
	after, err := s.st.ReportRestoreProgress(r.Context(), rs.ID, p)
	if errors.Is(err, store.ErrRestoreFinal) {
		writeErr(w, r, errf(409, wire.CodeConflict, "The restore is not running"))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if store.RestoreTerminal(after.Status) {
		slog.Info("restore finished", "restore_id", after.ID, "status", after.Status, "files", after.FilesDone, "bytes", after.BytesDone)
	}
	writeJSON(w, 200, wire.RestoreProgressReply{Status: after.Status, CancelRequested: after.CancelRequested})
}

// validation is a 422 validation error on one field.
func validation(field, format string, a ...any) error {
	return validationErr(map[string]string{field: fmt.Sprintf(format, a...)})
}

func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
