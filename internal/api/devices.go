package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/automation"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const maxPendingPerUser = 5

func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.enrollLimiter.Allow("enroll:" + s.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		writeErr(w, r, errf(429, "rate_limited", "Too many connection attempts, wait a minute"))
		return
	}
	var req wire.EnrolRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	pub, err := protocol.DecodePublicKey(req.PublicKey)
	if err != nil {
		writeErr(w, r, errf(400, "bad_public_key", "Invalid device public key"))
		return
	}
	// api-v1 §5A key exchange. box_pub is required for new clients; older
	// clients that omit it enrol without key sharing.
	if req.BoxPub != "" {
		if _, err := decodeX25519(req.BoxPub); err != nil {
			writeErr(w, r, errf(400, "bad_box_pub", "Invalid device box_pub (base64 X25519 public key)"))
			return
		}
	}
	switch req.InstallMode {
	case "", wire.ModuleInstallModeSimple, wire.ModuleInstallModeAdvanced:
	default:
		writeErr(w, r, errf(400, "bad_install_mode", "install_mode must be simple or advanced"))
		return
	}
	req.DeviceName = cleanText(req.DeviceName, 64)
	req.Identifier = cleanText(req.Identifier, 128)
	if req.DeviceName == "" || req.Identifier == "" {
		writeErr(w, r, errf(400, "bad_device", "Device name and identifier are required"))
		return
	}
	u, err := s.checkCredentials(r, req.Login, req.Password)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.CountRecentPendingForUser(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n >= maxPendingPerUser {
		writeErr(w, r, errf(429, "too_many_pending", "There are already %d pending device requests for this account. Approve or reject them in the web interface first", n))
		return
	}
	pollToken := auth.RandomToken(32)
	ip := s.clientIP(r)
	t := time.Now()
	ttl := time.Duration(s.settings.Get().DeviceRequestTTLMinutes) * time.Minute
	d := &store.Device{
		UserID: u.ID, Name: req.DeviceName, OS: cleanText(req.OS, 100), ClientVersion: cleanText(req.ClientVersion, 32),
		Identifier: req.Identifier, PublicKey: protocol.EncodePublicKey(pub), KeyFingerprint: protocol.KeyFingerprint(pub),
		UserCode: auth.UserCode(), PollTokenHash: auth.HashToken(pollToken), RequestIP: ip, RequestLocation: s.geo.Describe(ip),
		CreatedAt: t.UnixMilli(), ExpiresAt: t.Add(ttl).UnixMilli(),
	}
	if err := s.st.CreateDevice(r.Context(), d); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.BoxPub != "" {
		if err := s.st.SetDeviceBoxPub(r.Context(), d.ID, req.BoxPub); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	_ = s.st.AddNotification(r.Context(), u.ID, store.Notification{
		Title: "New device wants access", Level: "warning", Source: "devices",
		Message: fmt.Sprintf("%s (%s) requested access from %s. Code %s. Approve or reject it under Devices.", d.Name, d.OS, ip, d.UserCode),
	})
	s.activity(r, u.ID, "device.requested", d.Name, fmt.Sprintf("os=%s ip=%s code=%s", d.OS, ip, d.UserCode), true)
	writeJSON(w, 201, map[string]any{
		"request_id": d.ID, "poll_token": pollToken, "user_code": d.UserCode, "expires_at": d.ExpiresAt,
		"poll_interval_seconds": 3, "key_fingerprint": d.KeyFingerprint, "username": u.Username,
	})
}

func (s *Server) handleEnrollStatus(w http.ResponseWriter, r *http.Request) {
	if !s.pollLimiter.Allow("poll:" + s.clientIP(r)) {
		w.Header().Set("Retry-After", "5")
		writeErr(w, r, errf(429, "rate_limited", "Polling too fast"))
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	d, err := s.st.Device(r.Context(), id)
	if err != nil || token == "" || d.PollTokenHash == "" || subtle.ConstantTimeCompare([]byte(auth.HashToken(token)), []byte(d.PollTokenHash)) != 1 {
		writeErr(w, r, errf(404, "not_found", "Unknown or finished device request"))
		return
	}
	status := d.Status
	if status == "pending" && d.ExpiresAt <= time.Now().UnixMilli() {
		status = "expired"
		_, _ = s.st.ExpirePendingDevices(r.Context())
	}
	resp := map[string]any{"status": status, "expires_at": d.ExpiresAt}
	if status == "approved" {
		resp["device_id"] = d.ID
		// The device pins the SIK fingerprint (api-v1 §5A); omitted when the
		// server runs without key custody.
		if id, err := s.Keys().Identity(); err == nil {
			resp["server_identity"] = id
			if err := s.st.PinDeviceSIK(r.Context(), d.ID, id.SIKFingerprint); err != nil {
				slog.Warn("device SIK pin not recorded", "device", d.ID, "err", err)
			}
		}
	}
	if status != "pending" {
		s.st.ClearPollToken(r.Context(), d.ID)
	}
	writeJSON(w, 200, resp)
}

// --- web device management ---

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	_, _ = s.st.ExpirePendingDevices(r.Context())
	list, err := s.st.ListDevices(r.Context(), principal(r).User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views, err := s.deviceViews(r.Context(), principal(r).User.ID, list)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"devices": views})
}

func (s *Server) ownDevice(w http.ResponseWriter, r *http.Request) *store.Device {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return nil
	}
	d, err := s.st.Device(r.Context(), id)
	if err != nil || d.UserID != principal(r).User.ID {
		writeErr(w, r, store.ErrNotFound)
		return nil
	}
	return d
}

func (s *Server) handleDeviceDecision(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		d := s.ownDevice(w, r)
		if d == nil {
			return
		}
		ok, err := s.st.DecideDevice(r.Context(), d.ID, p.User.ID, status, p.User.ID)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if !ok {
			writeErr(w, r, errf(409, "not_pending", "This request is no longer pending (it may have expired). Ask the device to reconnect"))
			return
		}
		action := "device.approved"
		if status == "rejected" {
			action = "device.rejected"
		}
		s.activity(r, p.User.ID, action, d.Name, "fingerprint "+d.KeyFingerprint, true)
		d2, _ := s.st.Device(r.Context(), d.ID)
		if status == "approved" && d2 != nil {
			// Share the user's repository keys with the new device (api-v1
			// §5A key_share). Failures are logged; keys/status retries.
			s.shareKeysToDevice(context.WithoutCancel(r.Context()), d2)
		}
		writeJSON(w, 200, map[string]any{"device": s.deviceView(r.Context(), d2)})
	}
}

func (s *Server) handleDeviceRevoke(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	d := s.ownDevice(w, r)
	if d == nil {
		return
	}
	ok, err := s.st.RevokeDevice(r.Context(), d.ID, p.User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, r, errf(409, "not_active", "Device is not active"))
		return
	}
	s.cancelDeviceUploads(r, d.ID)
	// Revocation rotates the epoch of the owner's repositories
	// (backup-format §4.5).
	s.rotateAfterRevoke(context.WithoutCancel(r.Context()), d)
	s.activity(r, p.User.ID, "device.revoked", d.Name, "", true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) cancelDeviceUploads(r *http.Request, deviceID int64) {
	uploads, err := s.st.ListUploads(r.Context(), 0)
	if err != nil {
		return
	}
	for i := range uploads {
		if uploads[i].DeviceID != nil && *uploads[i].DeviceID == deviceID {
			s.cancelUpload(r.Context(), &uploads[i], "device revoked")
		}
	}
}

// handleDevicePatch renames a device (owner) and/or sets can_delete (owner
// or administrator).
func (s *Server) handleDevicePatch(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	d, err := s.st.Device(r.Context(), id)
	owner := err == nil && d.UserID == p.User.ID
	if err != nil || (!owner && !p.User.IsAdmin()) {
		writeErr(w, r, store.ErrNotFound)
		return
	}
	var req struct {
		Name      *string `json:"name"`
		CanDelete *bool   `json:"can_delete"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	var name string
	if req.Name != nil || req.CanDelete == nil {
		if !owner {
			writeErr(w, r, errf(403, "forbidden", "Only the device owner can rename it"))
			return
		}
		if req.Name != nil {
			name = cleanText(*req.Name, 64)
		}
		if name == "" {
			writeErr(w, r, errf(400, "bad_name", "Name is required"))
			return
		}
	}
	if name != "" {
		if err := s.st.RenameDevice(r.Context(), d.ID, name); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if req.CanDelete != nil {
		if err := s.setDeviceCanDelete(r.Context(), d.ID, *req.CanDelete); err != nil {
			writeErr(w, r, err)
			return
		}
		s.activity(r, d.UserID, "device.can_delete", d.Name, fmt.Sprintf("can_delete=%t", *req.CanDelete), true)
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	d := s.ownDevice(w, r)
	if d == nil {
		return
	}
	if d.Status == "approved" {
		writeErr(w, r, errf(409, "revoke_first", "Revoke the device before removing it"))
		return
	}
	if err := s.st.DeleteDevice(r.Context(), d.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- delete permission (devices.can_delete, defect B1) ---

// deviceJSON is the device representation of the device routes: the store
// row plus can_delete. Device lists (deviceViews) also carry install_mode and
// modules_summary from device_modules (api-v1 §6A).
type deviceJSON struct {
	*store.Device
	CanDelete      bool                  `json:"can_delete"`
	InstallMode    *string               `json:"install_mode,omitempty"`
	ModulesSummary *deviceModulesSummary `json:"modules_summary,omitempty"`
}

// deviceModulesSummary condenses the last PUT /device/modules report: the
// ids of enabled and failed modules, and when it was received (0 = never;
// install_mode is then "simple", as GET /devices/{id}/modules reports).
type deviceModulesSummary struct {
	Enabled   []string `json:"enabled"`
	Failed    []string `json:"failed"`
	UpdatedAt int64    `json:"updated_at"`
}

func (s *Server) deviceCanDelete(ctx context.Context, id int64) (bool, error) {
	var v int
	if err := s.st.DB.QueryRowContext(ctx, `SELECT can_delete FROM devices WHERE id = ?`, id).Scan(&v); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, store.ErrNotFound
		}
		return false, err
	}
	return v != 0, nil
}

func (s *Server) setDeviceCanDelete(ctx context.Context, id int64, v bool) error {
	n := 0
	if v {
		n = 1
	}
	res, err := s.st.DB.ExecContext(ctx, `UPDATE devices SET can_delete = ? WHERE id = ?`, n, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return store.ErrNotFound
	}
	return nil
}

// deviceView returns d with can_delete, or nil for a nil device. A lookup
// error reports can_delete as false.
func (s *Server) deviceView(ctx context.Context, d *store.Device) *deviceJSON {
	if d == nil {
		return nil
	}
	ok, _ := s.deviceCanDelete(ctx, d.ID)
	return &deviceJSON{Device: d, CanDelete: ok}
}

// deviceViews adds can_delete, install_mode and modules_summary to the
// devices of one user (userID 0: all users).
func (s *Server) deviceViews(ctx context.Context, userID int64, list []store.Device) ([]deviceJSON, error) {
	rows, err := s.st.DB.QueryContext(ctx, `SELECT id, can_delete FROM devices WHERE ? = 0 OR user_id = ?`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	can := map[int64]bool{}
	for rows.Next() {
		var id int64
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		can[id] = v != 0
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	mods, err := s.deviceModuleSummaries(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]deviceJSON, len(list))
	for i := range list {
		v := deviceJSON{Device: &list[i], CanDelete: can[list[i].ID]}
		mode, sum := wire.ModuleInstallModeSimple, &deviceModulesSummary{Enabled: []string{}, Failed: []string{}}
		if m, ok := mods[list[i].ID]; ok {
			mode, sum = m.mode, m.summary
		}
		v.InstallMode, v.ModulesSummary = &mode, sum
		out[i] = v
	}
	return out, nil
}

type deviceModuleRow struct {
	mode    string
	summary *deviceModulesSummary
}

// deviceModuleSummaries reads device_modules for the devices of userID (0:
// all) in one query.
func (s *Server) deviceModuleSummaries(ctx context.Context, userID int64) (map[int64]deviceModuleRow, error) {
	rows, err := s.st.DB.QueryContext(ctx, `SELECT m.device_id, m.install_mode, m.modules, m.updated_at
		FROM device_modules m JOIN devices d ON d.id = m.device_id WHERE ? = 0 OR d.user_id = ?`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]deviceModuleRow{}
	for rows.Next() {
		var id, at int64
		var mode, raw string
		if err := rows.Scan(&id, &mode, &raw, &at); err != nil {
			return nil, err
		}
		var list []wire.ModuleStatus
		_ = json.Unmarshal([]byte(raw), &list) // a corrupt report reads as no modules
		sum := &deviceModulesSummary{Enabled: []string{}, Failed: []string{}, UpdatedAt: at}
		for _, m := range list {
			switch m.State {
			case wire.ModuleStateEnabled:
				sum.Enabled = append(sum.Enabled, m.ID)
			case wire.ModuleStateFailed:
				sum.Failed = append(sum.Failed, m.ID)
			}
		}
		out[id] = deviceModuleRow{mode: mode, summary: sum}
	}
	return out, rows.Err()
}

// --- device API ---

func (s *Server) handleDeviceMe(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	u, _ := s.st.UserByID(r.Context(), p.User.ID)
	writeJSON(w, 200, map[string]any{
		"device":   s.deviceView(r.Context(), p.Device),
		"features": features(),
		"user":     map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName, "quota_bytes": u.QuotaBytes, "used_bytes": u.UsedBytes},
		"server":   map[string]any{"version": Version, "chunk_size": s.cfg.ChunkSize, "max_file_bytes": s.settings.Get().MaxFileBytes, "max_concurrent_chunks": s.cfg.MaxConcurrentChunkWrites, "site_name": s.settings.Get().SiteName},
	})
}

func (s *Server) handleDeviceState(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	raw, err := readLimited(r, 512<<10)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var probe struct {
		ClientVersion string `json:"client_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		writeErr(w, r, errf(400, "bad_json", "Invalid JSON"))
		return
	}
	if err := s.st.SetDeviceSyncState(r.Context(), p.Device.ID, raw, cleanText(probe.ClientVersion, 32)); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func readLimited(r *http.Request, n int64) ([]byte, error) {
	b, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, n))
	if err != nil {
		return nil, errf(413, "too_large", "Request body too large")
	}
	return b, nil
}

func (s *Server) handleDeviceAutomations(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rows, err := s.st.ListAutomations(r.Context(), p.User.ID, ptrBool(false), true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := []map[string]any{}
	for _, row := range rows {
		a, res := automation.Parse([]byte(row.Definition))
		if !res.Valid() {
			continue
		}
		a.Enabled = row.Enabled
		out = append(out, map[string]any{"id": row.ID, "name": row.Name, "updated_at": row.UpdatedAt, "definition": a})
	}
	writeJSON(w, 200, map[string]any{"automations": out})
}

func ptrBool(b bool) *bool { return &b }

func (s *Server) handleDeviceTransfers(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req struct {
		Transfers []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"transfers"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Transfers) > 200 {
		writeErr(w, r, errf(400, "too_many", "At most 200 entries per report"))
		return
	}
	for _, t := range req.Transfers {
		if t.Status != "failed" && t.Status != "skipped" {
			continue
		}
		_ = s.st.RecordTransfer(r.Context(), store.Transfer{UserID: p.User.ID, DeviceID: p.deviceID(), Path: cleanText(t.Path, 4096), Size: t.Size, Status: t.Status, Error: cleanText(t.Error, 500), Source: "device"})
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleDeviceNotifications(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req struct {
		Notifications []struct {
			Title   string `json:"title"`
			Message string `json:"message"`
			Level   string `json:"level"`
		} `json:"notifications"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Notifications) > 50 {
		writeErr(w, r, errf(400, "too_many", "At most 50 notifications per request"))
		return
	}
	for _, n := range req.Notifications {
		lvl := n.Level
		switch lvl {
		case "info", "success", "warning", "error":
		default:
			lvl = "info"
		}
		title := cleanText(n.Title, 200)
		if title == "" {
			continue
		}
		_ = s.st.AddNotification(r.Context(), p.User.ID, store.Notification{Title: title, Message: cleanText(n.Message, 2000), Level: lvl, Source: "automation:" + p.Device.Name})
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
