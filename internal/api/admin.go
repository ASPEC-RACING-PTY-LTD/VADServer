package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func diskSpaceSafe(path string) (storage.Space, error) {
	if err := storage.LocationHealthy(path); err != nil {
		return storage.Space{}, err
	}
	return storage.DiskSpace(path)
}

type locationView struct {
	store.Location
	Healthy     bool   `json:"healthy"`
	Problem     string `json:"problem,omitempty"`
	Total       int64  `json:"total_bytes"`
	Available   int64  `json:"available_bytes"`
	StoredBytes int64  `json:"stored_bytes"`
	Blobs       int64  `json:"blob_count"`
	Users       int64  `json:"user_count"`
	Pending     int64  `json:"pending_bytes"`
	LowSpace    bool   `json:"low_space"`
}

func (s *Server) locationViews(ctx context.Context) ([]locationView, error) {
	locs, err := s.st.ListLocations(ctx)
	if err != nil {
		return nil, err
	}
	out := []locationView{}
	for _, l := range locs {
		v := locationView{Location: l, Healthy: true}
		if sp, err := diskSpaceSafe(l.Path); err != nil {
			v.Healthy, v.Problem = false, err.Error()
		} else {
			v.Total, v.Available = sp.Total, sp.Available
			v.LowSpace = sp.Available < max(l.ReserveBytes, s.settings.Get().MinFreeBytes)*2
		}
		v.StoredBytes, v.Blobs, v.Users, _ = s.st.LocationUsage(ctx, l.ID)
		v.Pending, _ = s.st.PendingBytes(ctx, l.ID)
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) handleAdminOverview(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.GlobalStats(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	locs, err := s.locationViews(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ts, _ := s.st.TransferStats(r.Context(), 0)
	audit, _ := s.st.ListActivity(r.Context(), store.ActivityFilter{AuditAll: true, Limit: 10})
	writeJSON(w, 200, map[string]any{"stats": g, "locations": locs, "transfers": ts, "recent_audit": audit, "version": Version, "uptime_seconds": int64(time.Since(s.started).Seconds())})
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.st.ListUsers(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views, err := s.adminUserViews(r.Context(), users)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"users": views})
}

// adminUserJSON is a user as the admin user routes return it: the store row
// plus is_superuser (api-v1 §11B).
type adminUserJSON struct {
	*store.User
	IsSuperuser bool `json:"is_superuser"`
}

// adminUserViews adds users.is_superuser to list.
func (s *Server) adminUserViews(ctx context.Context, list []store.User) ([]adminUserJSON, error) {
	rows, err := s.st.DB.QueryContext(ctx, `SELECT id FROM users WHERE is_superuser = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	super := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		super[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]adminUserJSON, len(list))
	for i := range list {
		out[i] = adminUserJSON{User: &list[i], IsSuperuser: super[list[i].ID]}
	}
	return out, nil
}

// adminUserView returns u with is_superuser (false when the lookup fails).
func (s *Server) adminUserView(ctx context.Context, u *store.User) *adminUserJSON {
	if u == nil {
		return nil
	}
	ok, _ := s.isSuperuser(ctx, u.ID)
	return &adminUserJSON{User: u, IsSuperuser: ok}
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		credentialsReq
		Role       string `json:"role"`
		QuotaBytes *int64 `json:"quota_bytes"`
		LocationID *int64 `json:"location_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if req.Role != "user" && req.Role != "admin" {
		writeErr(w, r, errf(400, "bad_role", "Role must be user or admin"))
		return
	}
	quota := int64(-1)
	if req.QuotaBytes != nil {
		if *req.QuotaBytes < 0 {
			writeErr(w, r, errf(400, "bad_quota", "Quota must not be negative"))
			return
		}
		quota = *req.QuotaBytes
	}
	u, err := s.createUser(r.Context(), req.credentialsReq, req.Role, quota)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if req.LocationID != nil {
		if err := s.checkUserLocation(r.Context(), *req.LocationID); err != nil {
			writeErr(w, r, err)
			return
		}
		if err := s.st.UpdateUser(r.Context(), u.ID, store.UserPatch{LocationID: req.LocationID}); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	s.activity(r, u.ID, "admin.user_created", u.Username, "role="+req.Role, true)
	u, _ = s.st.UserByID(r.Context(), u.ID)
	writeJSON(w, 201, map[string]any{"user": u})
}

// checkUserLocation validates a storage location an administrator assigns
// to a user: it must exist and be enabled.
func (s *Server) checkUserLocation(ctx context.Context, id int64) error {
	l, err := s.st.Location(ctx, id)
	if err != nil {
		return errf(400, "bad_location", "Unknown storage location")
	}
	if !l.Enabled {
		return errf(400, "bad_location", "Storage location %q is disabled", l.Name)
	}
	return nil
}

func (s *Server) adminTargetUser(w http.ResponseWriter, r *http.Request) *store.User {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return nil
	}
	u, err := s.st.UserByID(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return nil
	}
	return u
}

func (s *Server) handleAdminPatchUser(w http.ResponseWriter, r *http.Request) {
	me := principal(r).User
	u := s.adminTargetUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Email       *string `json:"email"`
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Disabled    *bool   `json:"disabled"`
		QuotaBytes  *int64  `json:"quota_bytes"`
		LocationID  *int64  `json:"location_id"`
		IsSuperuser *bool   `json:"is_superuser"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	// Only a superuser may grant or revoke is_superuser (api-v1 §11B); the
	// check runs before any field is changed.
	if req.IsSuperuser != nil {
		if ok, err := s.isSuperuser(r.Context(), me.ID); err != nil || !ok {
			writeErr(w, r, errf(403, wire.CodeNotSuperuser, "Superuser access required"))
			return
		}
	}
	if req.Role != nil && *req.Role != "user" && *req.Role != "admin" {
		writeErr(w, r, errf(400, "bad_role", "Role must be user or admin"))
		return
	}
	if u.ID == me.ID && ((req.Disabled != nil && *req.Disabled) || (req.Role != nil && *req.Role != "admin")) {
		writeErr(w, r, errf(400, "self_lockout", "You cannot disable or demote your own account"))
		return
	}
	if u.IsAdmin() && ((req.Disabled != nil && *req.Disabled) || (req.Role != nil && *req.Role != "admin")) {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			writeErr(w, r, errf(400, "last_admin", "At least one active administrator is required"))
			return
		}
	}
	if (req.Disabled != nil && *req.Disabled) || (req.Role != nil && *req.Role != "admin") || (req.IsSuperuser != nil && !*req.IsSuperuser) {
		if err := s.guardLastSuperuser(r.Context(), u.ID); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if req.IsSuperuser != nil && *req.IsSuperuser {
		role, disabled := u.Role, u.Disabled
		if req.Role != nil {
			role = *req.Role
		}
		if req.Disabled != nil {
			disabled = *req.Disabled
		}
		if role != "admin" || disabled {
			writeErr(w, r, errf(400, "not_admin", "Only active administrators can be superusers"))
			return
		}
	}
	if req.QuotaBytes != nil && *req.QuotaBytes < 0 {
		writeErr(w, r, errf(400, "bad_quota", "Quota must not be negative (0 = unlimited)"))
		return
	}
	if req.Email != nil && *req.Email != "" && !emailPattern.MatchString(strings.TrimSpace(*req.Email)) {
		writeErr(w, r, errf(400, "invalid_email", "Email address is not valid"))
		return
	}
	if req.LocationID != nil {
		if err := s.checkUserLocation(r.Context(), *req.LocationID); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if err := s.st.UpdateUser(r.Context(), u.ID, store.UserPatch{Email: req.Email, DisplayName: req.DisplayName, Role: req.Role, Disabled: req.Disabled, QuotaBytes: req.QuotaBytes, LocationID: req.LocationID}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			err = errf(409, "email_taken", "That email is already in use")
		}
		writeErr(w, r, err)
		return
	}
	if req.IsSuperuser != nil {
		// Applied after the role/disabled update so that one request can
		// promote a user to admin and grant superuser together.
		if cur, err := s.st.UserByID(r.Context(), u.ID); err == nil {
			u = cur
		}
		if err := s.setUserSuperuser(r, u, *req.IsSuperuser); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	var changes []string
	if req.Disabled != nil {
		changes = append(changes, fmt.Sprintf("disabled=%v", *req.Disabled))
		if *req.Disabled {
			_, _ = s.st.DeleteUserSessions(r.Context(), u.ID, "")
		}
	}
	if req.Role != nil {
		changes = append(changes, "role="+*req.Role)
	}
	if req.QuotaBytes != nil {
		changes = append(changes, "quota="+humanBytes(*req.QuotaBytes))
	}
	if req.LocationID != nil {
		changes = append(changes, fmt.Sprintf("location=%d", *req.LocationID))
	}
	s.activity(r, u.ID, "admin.user_updated", u.Username, strings.Join(changes, ", "), true)
	u, _ = s.st.UserByID(r.Context(), u.ID)
	writeJSON(w, 200, map[string]any{"user": s.adminUserView(r.Context(), u)})
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	me := principal(r).User
	u := s.adminTargetUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		ConfirmUsername string `json:"confirm_username"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if !strings.EqualFold(req.ConfirmUsername, u.Username) {
		writeErr(w, r, errf(400, "confirm", "Type the username to confirm permanent deletion"))
		return
	}
	if u.ID == me.ID {
		writeErr(w, r, errf(400, "self_delete", "You cannot delete your own account"))
		return
	}
	if u.IsAdmin() {
		if n, _ := s.st.CountAdmins(r.Context()); n <= 1 {
			writeErr(w, r, errf(400, "last_admin", "At least one active administrator is required"))
			return
		}
	}
	if err := s.guardLastSuperuser(r.Context(), u.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	uploads, _ := s.st.ListUploads(r.Context(), u.ID)
	for i := range uploads {
		s.cancelUpload(r.Context(), &uploads[i], "user deleted")
	}
	blobs, err := s.st.DeleteUserRecords(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.blobs.Remove(r.Context(), blobs)
	s.activity(r, 0, "admin.user_deleted", u.Username, fmt.Sprintf("%d blobs removed", len(blobs)), true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleAdminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	u := s.adminTargetUser(w, r)
	if u == nil {
		return
	}
	except := ""
	if p := principal(r); p.User.ID == u.ID {
		except = p.Session.ID
	}
	n, err := s.st.DeleteUserSessions(r.Context(), u.ID, except)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "admin.sessions_revoked", u.Username, fmt.Sprintf("%d session(s)", n), true)
	writeJSON(w, 200, map[string]any{"revoked": n})
}

func (s *Server) handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	u := s.adminTargetUser(w, r)
	if u == nil {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		writeErr(w, r, errf(400, "weak_password", "%s", err.Error()))
		return
	}
	hash, err := auth.HashPassword(r.Context(), req.Password)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.SetPassword(r.Context(), u.ID, hash); err != nil {
		writeErr(w, r, err)
		return
	}
	_, _ = s.st.DeleteUserSessions(r.Context(), u.ID, "")
	s.activity(r, u.ID, "admin.password_reset", u.Username, "all sessions signed out", true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleAdminRecalc(w http.ResponseWriter, r *http.Request) {
	u := s.adminTargetUser(w, r)
	if u == nil {
		return
	}
	used, err := s.st.RecalculateUsage(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"used_bytes": used})
}

// --- storage ---

func (s *Server) handleAdminStorage(w http.ResponseWriter, r *http.Request) {
	locs, err := s.locationViews(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"locations": locs, "allowed_roots": s.cfg.StorageRoots})
}

func (s *Server) handleAdminCreateStorage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Path         string `json:"path"`
		ReserveBytes int64  `json:"reserve_bytes"`
		IsDefault    bool   `json:"is_default"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name = cleanText(req.Name, 60)
	if req.Name == "" {
		writeErr(w, r, errf(400, "bad_name", "Name is required"))
		return
	}
	p := filepath.Clean(strings.TrimSpace(req.Path))
	if !filepath.IsAbs(p) || !s.cfg.AllowedStoragePath(p) {
		writeErr(w, r, errf(400, "path_not_allowed", "Path must be an absolute directory inside: %s (set VAULTDOCK_STORAGE_ROOTS and mount the volume)", strings.Join(s.cfg.StorageRoots, ", ")))
		return
	}
	if req.ReserveBytes < 0 {
		writeErr(w, r, errf(400, "bad_reserve", "Reserve must not be negative"))
		return
	}
	// The lexical check above is not enough: a symlink or junction inside a
	// root could point anywhere, so the resolved path must stay inside too.
	resolved, err := storage.ResolveLocationPath(p, s.cfg.StorageRoots)
	if errors.Is(err, storage.ErrOutsideRoots) {
		writeErr(w, r, errf(400, "path_not_allowed", "Path resolves outside the allowed storage roots (symlink or junction): %s", strings.Join(s.cfg.StorageRoots, ", ")))
		return
	}
	if err != nil {
		writeErr(w, r, errf(400, "bad_path", "%s", err.Error()))
		return
	}
	existing, err := s.st.ListLocations(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	for _, l := range existing {
		if storage.Overlaps(l.Path, resolved) {
			writeErr(w, r, errf(400, "nested", "Path overlaps existing location %q", l.Name))
			return
		}
	}
	if err := storage.PrepareLocation(p); err != nil {
		writeErr(w, r, errf(400, "bad_path", "%s", err.Error()))
		return
	}
	l := &store.Location{Name: req.Name, Path: p, ReserveBytes: req.ReserveBytes, Enabled: true}
	if err := s.st.CreateLocation(r.Context(), l); err != nil {
		if errors.Is(err, store.ErrConflict) {
			err = errf(409, "exists", "A location with that name or path already exists")
		}
		writeErr(w, r, err)
		return
	}
	if req.IsDefault {
		l.IsDefault = true
		_ = s.st.UpdateLocation(r.Context(), l)
	}
	_ = s.blobs.Reload(r.Context())
	s.activity(r, 0, "admin.storage_added", l.Name, l.Path, true)
	writeJSON(w, 201, map[string]any{"location": l})
}

func (s *Server) handleAdminPatchStorage(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	l, err := s.st.Location(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Name         *string `json:"name"`
		ReserveBytes *int64  `json:"reserve_bytes"`
		Enabled      *bool   `json:"enabled"`
		IsDefault    *bool   `json:"is_default"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Name != nil {
		if n := cleanText(*req.Name, 60); n != "" {
			l.Name = n
		}
	}
	if req.ReserveBytes != nil {
		if *req.ReserveBytes < 0 {
			writeErr(w, r, errf(400, "bad_reserve", "Reserve must not be negative"))
			return
		}
		l.ReserveBytes = *req.ReserveBytes
	}
	wasEnabled, wasDefault := l.Enabled, l.IsDefault
	if req.Enabled != nil {
		l.Enabled = *req.Enabled
	}
	if req.IsDefault != nil && *req.IsDefault {
		l.IsDefault = true
	}
	// Enabling a location or making it the default sends new uploads there,
	// so it must be mounted (marker present) first.
	if (l.Enabled && !wasEnabled) || (l.IsDefault && !wasDefault) {
		if err := storage.LocationHealthy(l.Path); err != nil {
			writeErr(w, r, errf(400, "location_unavailable", "%s", err.Error()))
			return
		}
	}
	if l.IsDefault && !l.Enabled {
		writeErr(w, r, errf(400, "default_disabled", "The default location cannot be disabled; choose another default first"))
		return
	}
	if err := s.st.UpdateLocation(r.Context(), l); err != nil {
		writeErr(w, r, err)
		return
	}
	_ = s.blobs.Reload(r.Context())
	s.activity(r, 0, "admin.storage_updated", l.Name, fmt.Sprintf("enabled=%v reserve=%s default=%v", l.Enabled, humanBytes(l.ReserveBytes), l.IsDefault), true)
	writeJSON(w, 200, map[string]any{"location": l})
}

func (s *Server) handleAdminDeleteStorage(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	l, err := s.st.Location(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	_, blobs, users, err := s.st.LocationUsage(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if l.IsDefault || blobs > 0 || users > 0 {
		writeErr(w, r, errf(409, "in_use", "Only empty, non-default locations without assigned users can be removed (files on disk are never deleted by this action)"))
		return
	}
	if err := s.st.DeleteLocation(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	_ = s.blobs.Reload(r.Context())
	s.activity(r, 0, "admin.storage_removed", l.Name, l.Path, true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- devices, transfers ---

func (s *Server) handleAdminDevices(w http.ResponseWriter, r *http.Request) {
	_, _ = s.st.ExpirePendingDevices(r.Context())
	list, err := s.st.ListDevices(r.Context(), 0)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views, err := s.deviceViews(r.Context(), 0, list)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"devices": views})
}

func (s *Server) handleAdminDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	d, err := s.st.Device(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ok, err := s.st.RevokeDevice(r.Context(), id, principal(r).User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, r, errf(409, "not_active", "Device is not active"))
		return
	}
	s.cancelDeviceUploads(r, id)
	s.activity(r, d.UserID, "admin.device_revoked", d.Name, "", true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleAdminTransfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	user, _ := strconv.ParseInt(q.Get("user"), 10, 64)
	list, err := s.st.ListTransfers(r.Context(), store.TransferFilter{UserID: user, Status: q.Get("status"), Before: before, Limit: 200})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleAdminUploads(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListUploads(r.Context(), 0)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"uploads": list})
}

func (s *Server) handleAdminCancelUpload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	up, err := s.st.UploadAny(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !s.cancelUpload(r.Context(), up, "cancelled by administrator") {
		if cur, err := s.st.UploadAny(r.Context(), id); err == nil && cur.Status == "finalizing" {
			writeErr(w, r, errf(409, "upload_busy", "Upload is being finalised and cannot be cancelled"))
			return
		}
	}
	s.activity(r, up.UserID, "admin.upload_cancelled", up.Path, "", true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- settings, health, audit ---

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"settings": s.settings.Get(), "config": map[string]any{
		"public_url": s.cfg.PublicURL, "storage_roots": s.cfg.StorageRoots, "chunk_size": s.cfg.ChunkSize,
		"max_concurrent_chunks": s.cfg.MaxConcurrentChunkWrites, "upload_ttl_hours": int(s.cfg.UploadSessionTTL.Hours()),
		"tls": s.cfg.TLSCert != "" || s.cfg.TLSSelfSigned, "geoip": s.cfg.GeoIPDB != "", "trusted_proxies": len(s.cfg.TrustedProxies),
	}})
}

// handleAdminSaveSettings merges a partial settings object onto the stored
// settings (B14); absent fields keep their values.
func (s *Server) handleAdminSaveSettings(w http.ResponseWriter, r *http.Request) {
	var body json.RawMessage
	if err := decode(r, &body); err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.settings.Merge(r.Context(), body)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.blobs.SetGlobalMinFree(v.MinFreeBytes)
	s.activity(r, 0, "admin.settings_updated", "", fmt.Sprintf("registration=%v default_quota=%s", v.RegistrationEnabled, humanBytes(v.DefaultQuotaBytes)), true)
	writeJSON(w, 200, map[string]any{"settings": v})
}

func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	dbOK := s.st.DB.PingContext(r.Context()) == nil
	integrity := "not checked"
	if r.URL.Query().Get("deep") == "1" {
		if err := s.st.IntegrityCheck(r.Context()); err != nil {
			integrity = err.Error()
		} else {
			integrity = "ok"
		}
	}
	var dbSize int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if fi, err := os.Stat(s.st.Path + suffix); err == nil {
			dbSize += fi.Size()
		}
	}
	schema, _ := s.st.SchemaVersion(r.Context())
	locs, _ := s.locationViews(r.Context())
	stateSpace, _ := storage.DiskSpace(filepath.Dir(s.st.Path))
	status := "healthy"
	var problems []string
	for _, l := range locs {
		if !l.Healthy {
			problems = append(problems, fmt.Sprintf("storage %q unavailable", l.Name))
		} else if l.LowSpace {
			problems = append(problems, fmt.Sprintf("storage %q is low on space", l.Name))
		}
	}
	if !dbOK {
		problems = append(problems, "database unreachable")
	}
	if stateSpace.Available > 0 && stateSpace.Available < 512<<20 {
		problems = append(problems, "state volume is low on space")
	}
	if len(problems) > 0 {
		status = "degraded"
	}
	writeJSON(w, 200, map[string]any{
		"status": status, "problems": problems, "version": Version, "go": runtime.Version(),
		"uptime_seconds": int64(time.Since(s.started).Seconds()), "goroutines": runtime.NumGoroutine(),
		"memory":       map[string]any{"heap_alloc": ms.HeapAlloc, "sys": ms.Sys, "num_gc": ms.NumGC},
		"database":     map[string]any{"ok": dbOK, "size_bytes": dbSize, "schema_version": schema, "integrity": integrity, "path": s.st.Path},
		"state_volume": stateSpace, "locations": locs,
		"chunk_slots": map[string]any{"in_use": len(s.chunkSem), "capacity": cap(s.chunkSem)},
	})
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	list, err := s.st.ListActivity(r.Context(), store.ActivityFilter{AuditAll: true, Action: q.Get("action"), Before: before, Limit: 200})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleAdminCleanup(w http.ResponseWriter, r *http.Request) {
	res := s.RunMaintenance(r.Context(), true)
	s.activity(r, 0, "admin.maintenance", "", fmt.Sprintf("%v", res), true)
	writeJSON(w, 200, res)
}

// RunMaintenance performs periodic cleanup. orphans also scans storage for
// unreferenced files (slower).
func (s *Server) RunMaintenance(ctx context.Context, orphans bool) map[string]any {
	res := map[string]any{}
	if n, err := s.st.ExpirePendingDevices(ctx); err == nil {
		res["devices_expired"] = n
	}
	if n, err := s.st.PurgeExpiredSessions(ctx); err == nil {
		res["sessions_purged"] = n
	}
	expired, err := s.st.ExpiredUploads(ctx)
	if err == nil {
		cleaned := 0
		for i := range expired {
			u := &expired[i]
			switch u.Status {
			case "finalizing":
				// R5: being hashed and moved; deleting its temp file would
				// break the commit. A finalisation interrupted by a crash is
				// reopened by RecoverAfterRestart and expires normally.
				continue
			case "open":
				if !s.cancelUpload(ctx, u, "upload session expired") {
					continue // became finalizing concurrently
				}
			default:
				_ = s.st.DeleteUpload(ctx, u.ID)
				if tmp, err := s.blobs.TempPath(u.LocationID, u.ID); err == nil {
					storage.RemoveTemp(tmp)
				}
			}
			cleaned++
		}
		res["uploads_cleaned"] = cleaned
	}
	_ = s.st.MarkStaleTransfersFailed(ctx)
	st := s.settings.Get()
	if st.TrashRetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(st.TrashRetentionDays) * 24 * time.Hour).UnixMilli()
		n, freed, err := s.st.PurgeTrash(ctx, 0, nil, cutoff)
		if err == nil {
			s.blobs.Remove(ctx, freed)
			res["trash_purged"] = n
		} else {
			slog.Error("trash purge failed", "err", err)
		}
	}
	// Originals erased after a clip export keep their own (shorter) expiry.
	if n, freed, err := s.st.PurgeExpiredTrash(ctx); err == nil {
		s.blobs.Remove(ctx, freed)
		if n > 0 {
			res["trash_expired"] = n
		}
	} else {
		slog.Error("trash expiry purge failed", "err", err)
	}
	s.st.PruneReuploadBlocks(ctx)
	s.st.PruneVideoExports(ctx, time.Now().Add(-30*24*time.Hour).UnixMilli())
	s.st.PruneActivity(ctx, time.Now().Add(-time.Duration(st.ActivityRetentionDays)*24*time.Hour).UnixMilli())
	if orphans {
		s.sweepMedia(ctx)
		n, err := s.blobs.CleanOrphans(ctx, 2*time.Hour)
		if err != nil {
			slog.Error("orphan cleanup failed", "err", err)
		}
		res["orphans_removed"] = n
	}
	return res
}

// RecoverAfterRestart resets uploads interrupted mid-finalisation.
func (s *Server) RecoverAfterRestart(ctx context.Context) {
	res, err := s.st.DB.ExecContext(ctx, "UPDATE uploads SET status = 'open' WHERE status = 'finalizing'")
	if err == nil {
		if n, _ := res.RowsAffected(); n > 0 {
			slog.Info("recovered interrupted upload finalisations", "count", n)
		}
	}
}
