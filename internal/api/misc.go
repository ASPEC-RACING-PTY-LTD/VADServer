package api

import (
	"net/http"
	"strconv"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	fresh, err := s.st.UserByID(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st, err := s.st.UserStats(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ts, err := s.st.TransferStats(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	recent, err := s.st.RecentFiles(r.Context(), u.ID, 8)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	devices, err := s.st.ListDevices(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	approved, pending := 0, 0
	for _, d := range devices {
		switch d.Status {
		case "approved":
			approved++
		case "pending":
			if d.ExpiresAt > store.Now() {
				pending++
			}
		}
	}
	resp := map[string]any{
		"usage":     map[string]any{"used_bytes": fresh.UsedBytes, "quota_bytes": fresh.QuotaBytes},
		"files":     st,
		"transfers": ts,
		"recent":    recent,
		"devices":   map[string]any{"approved": approved, "pending": pending},
	}
	// Show how much room is actually left on the user's storage device so
	// unlimited quotas still reflect physical capacity.
	if locID, err := s.userLocation(r, u); err == nil {
		if l, ok := s.blobs.Location(locID); ok {
			if sp, err := diskSpaceSafe(l.Path); err == nil {
				resp["device_available_bytes"] = sp.Available
			}
		}
	}
	writeJSON(w, 200, resp)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := s.st.ListActivity(r.Context(), store.ActivityFilter{UserID: principal(r).User.ID, Action: q.Get("action"), Before: before, Limit: limit})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleTransfers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	dev, _ := strconv.ParseInt(q.Get("device"), 10, 64)
	list, err := s.st.ListTransfers(r.Context(), store.TransferFilter{UserID: principal(r).User.ID, DeviceID: dev, Status: q.Get("status"), Before: before, Limit: limit})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleSyncOverview(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	devices, err := s.st.ListDevices(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	type devView struct {
		store.Device
		Online bool `json:"online"`
	}
	out := []devView{}
	now := store.Now()
	for _, d := range devices {
		if d.Status != "approved" {
			continue
		}
		online := d.LastSeenAt != nil && now-*d.LastSeenAt < 3*60*1000
		out = append(out, devView{Device: d, Online: online})
	}
	ts, err := s.st.TransferStats(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	failed, err := s.st.ListTransfers(r.Context(), store.TransferFilter{UserID: u.ID, Status: "failed", Limit: 20})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	active, err := s.st.ListUploads(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views := make([]uploadView, 0, len(active))
	for i := range active {
		views = append(views, viewUpload(&active[i]))
	}
	writeJSON(w, 200, map[string]any{"devices": out, "stats": ts, "recent_failures": failed, "active_uploads": views})
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	list, unread, err := s.st.ListNotifications(r.Context(), principal(r).User.ID, 50)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list, "unread": unread})
}

func (s *Server) handleNotificationsRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int64 `json:"id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.MarkNotificationsRead(r.Context(), principal(r).User.ID, req.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
