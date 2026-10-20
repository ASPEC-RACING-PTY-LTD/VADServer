package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// A hard refresh is marked in the page so the web UI starts over; a normal
// refresh or visit is not.
func TestSPAHandlerMarksHardReload(t *testing.T) {
	h := SPAHandler(fstest.MapFS{"index.html": {Data: []byte("<html><head><title>x</title></head></html>")}})
	get := func(hdr map[string]string) string {
		r := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		b, _ := io.ReadAll(w.Body)
		return string(b)
	}
	const mark = `<meta name="vd-reload" content="hard">`
	if strings.Contains(get(nil), mark) || strings.Contains(get(map[string]string{"Cache-Control": "max-age=0"}), mark) {
		t.Fatal("a normal visit or refresh was marked as hard")
	}
	if !strings.Contains(get(map[string]string{"Cache-Control": "no-cache", "Pragma": "no-cache"}), mark) {
		t.Fatal("a hard refresh was not marked")
	}
}
