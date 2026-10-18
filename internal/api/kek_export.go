package api

// One-time export of the server KEK. Right after setup a superuser downloads
// the key file once from the portal and keeps it wherever they like (password
// manager, encrypted USB); afterwards the server refuses to hand it out again,
// so a stolen session later cannot exfiltrate it. The file is the raw 32 bytes
// that go back at VAULTDOCK_KEK_FILE when the server is rebuilt.

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const settingKEKExported = "kek_exported" // "<unix ms> <user id>"

var kekExportMu sync.Mutex

type kekExportStatus struct {
	Available  bool  `json:"available"`
	Exported   bool  `json:"exported"`
	ExportedAt int64 `json:"exported_at,omitempty"`
	ExportedBy int64 `json:"exported_by,omitempty"`
}

func (s *Server) mountKEKExport(rt routes) {
	rt.Super.Get("/admin/keys/kek", s.handleKEKStatus)
	rt.Super.Post("/admin/keys/kek/export", s.handleKEKExport)
}

func (s *Server) kekStatus(r *http.Request) (kekExportStatus, error) {
	st := kekExportStatus{Available: s.Keys() != nil}
	v, ok, err := s.st.GetSetting(r.Context(), settingKEKExported)
	if err != nil || !ok {
		return st, err
	}
	st.Exported = true
	var at, by int64
	for i, f := range strings.Fields(v) {
		n, _ := strconv.ParseInt(f, 10, 64)
		if i == 0 {
			at = n
		} else {
			by = n
		}
	}
	st.ExportedAt, st.ExportedBy = at, by
	return st, nil
}

func (s *Server) handleKEKStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.kekStatus(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleKEKExport returns the KEK as a file download, once.
func (s *Server) handleKEKExport(w http.ResponseWriter, r *http.Request) {
	kekExportMu.Lock()
	defer kekExportMu.Unlock()
	st, err := s.kekStatus(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if st.Exported {
		writeErr(w, r, errf(http.StatusConflict, "kek_already_exported", "%s",
			"The server key was already downloaded on "+time.UnixMilli(st.ExportedAt).UTC().Format(time.RFC1123)+". It can only be downloaded once."))
		return
	}
	kek, err := s.Keys().KEK()
	if err != nil {
		writeErr(w, r, keysErr(err))
		return
	}
	me := principal(r).User.ID
	// Recorded before the bytes are sent: a failed transfer is not retried
	// through the portal (the file stays on the server at VAULTDOCK_KEK_FILE).
	if err := s.st.SetSetting(r.Context(), settingKEKExported, strconv.FormatInt(time.Now().UnixMilli(), 10)+" "+strconv.FormatInt(me, 10)); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, me, "kek_export", "server", "server key file downloaded (one-time export)", true)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="vaultdock-server.kek"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(kek)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(kek[:])
}
