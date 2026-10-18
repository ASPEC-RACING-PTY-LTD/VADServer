package client

import (
	"context"
	"net/http"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestProfiles(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	sid := "S-1-5-21-1000"
	f.handle("PUT /api/v1/device/profiles", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var rep wire.ProfileReport
		if err := strictDecode(body, &rep); err != nil || rep.Consents == nil || len(rep.Profiles) != 1 {
			writeErr(w, 400, "invalid", nil)
			return
		}
		w.WriteHeader(204)
	})
	f.handle("POST /api/v1/device/profiles/"+sid+"/security-event", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var ev wire.ProfileSecurityEventRequest
		if err := strictDecode(body, &ev); err != nil || ev.Signal != wire.ProfileSignalPasswordReset {
			writeErr(w, 400, "invalid", nil)
			return
		}
		w.WriteHeader(204)
	})
	rep := wire.ProfileReport{Profiles: []wire.ProfileReportEntry{{SID: sid, State: wire.ProfileStateActiveDefault}}}
	if err := c.ReportProfiles(ctx, rep); err != nil {
		t.Fatal(err)
	}
	ev := wire.ProfileSecurityEventRequest{Signal: wire.ProfileSignalPasswordReset, ObservedAt: 1, Evidence: wire.ProfileEvidenceEventLog4724}
	if err := c.ReportSecurityEvent(ctx, sid, ev); err != nil {
		t.Fatal(err)
	}
}
