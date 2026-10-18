package client

import (
	"context"
	"net/http"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestModules(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("PUT /api/v1/device/modules", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var rep wire.ModuleReport
		if err := strictDecode(body, &rep); err != nil || rep.Modules == nil || rep.InstallMode != wire.ModuleInstallModeSimple {
			writeErr(w, 400, "invalid", nil)
			return
		}
		w.WriteHeader(204)
	})
	f.handle("GET /api/v1/modules/catalog", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, []wire.ModuleCatalogEntry{{ID: wire.ModuleVSS, Delivery: wire.ModuleDeliveryComponent}})
	})
	if err := c.ReportModules(ctx, wire.ModuleReport{InstallMode: wire.ModuleInstallModeSimple, CoreVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	cat, err := c.ModuleCatalog(ctx)
	if err != nil || len(cat) != 1 || cat[0].ID != wire.ModuleVSS {
		t.Fatalf("catalog: %+v %v", cat, err)
	}
}
