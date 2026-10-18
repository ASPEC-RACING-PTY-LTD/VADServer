package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestJobsRunsIssues(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("GET /api/v1/backup/jobs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("device") != "7" {
			writeErr(w, 403, "forbidden", nil)
			return
		}
		if r.URL.Query().Get("cursor") == "" {
			writeJSON(w, 200, wire.List[wire.Job]{Items: []wire.Job{{ID: 1}}, NextCursor: "n"})
			return
		}
		writeJSON(w, 200, wire.List[wire.Job]{Items: []wire.Job{{ID: 2, Revision: 3}}})
	})
	f.handle("PATCH /api/v1/backup/jobs/2", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get("If-Match") != "3" {
			writeErr(w, 409, wire.CodeConflict, nil)
			return
		}
		writeJSON(w, 200, wire.Job{ID: 2, Revision: 4})
	})
	f.handle("POST /api/v1/backup/jobs/2/runs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 201, wire.RunStartResponse{RunID: 8})
	})
	f.handle("PATCH /api/v1/backup/runs/8", func(w http.ResponseWriter, r *http.Request, _ []byte) { w.WriteHeader(204) })
	batches := 0
	f.handle("POST /api/v1/backup/runs/8/issues", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.IssueBatchRequest
		json.Unmarshal(body, &req)
		if len(req.Issues) > wire.IssueBatchMax {
			writeErr(w, 400, "too_many", nil)
			return
		}
		batches++
		w.WriteHeader(204)
	})
	f.handle("POST /api/v1/backup/runs/8/retry-skipped", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, wire.DeviceTaskCreated{TaskID: 12})
	})

	j, err := c.Job(ctx, 2)
	if err != nil || j.Revision != 3 {
		t.Fatalf("job: %+v %v", j, err)
	}
	if _, err := c.Job(ctx, 99); !IsCode(err, "not_found") {
		t.Fatalf("missing job: %v", err)
	}
	name := "Docs"
	if j, err = c.UpdateJob(ctx, 2, 3, wire.JobUpdateRequest{Name: &name}); err != nil || j.Revision != 4 {
		t.Fatalf("patch: %v", err)
	}
	if _, err := c.UpdateJob(ctx, 2, 1, wire.JobUpdateRequest{Name: &name}); !IsCode(err, wire.CodeConflict) {
		t.Fatalf("stale patch: %v", err)
	}
	run, err := c.StartRun(ctx, 2, wire.RunStartRequest{ClientRunUUID: "u", Trigger: wire.RunTriggerManual})
	if err != nil || run != 8 {
		t.Fatalf("start: %d %v", run, err)
	}
	if err := c.RunHeartbeat(ctx, 8, wire.RunProgress{Status: wire.RunStatusUploading}); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishRun(ctx, 8, wire.RunProgress{Status: wire.RunStatusUploading}); err == nil {
		t.Fatal("non-terminal finish accepted")
	}
	if err := c.FinishRun(ctx, 8, wire.RunProgress{Status: wire.RunStatusPartial}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReportIssues(ctx, 8, make([]wire.IssueReport, wire.IssueBatchMax+1)); err != nil || batches != 2 {
		t.Fatalf("issues: %d %v", batches, err)
	}
	if tc, err := c.RetrySkipped(ctx, 8); err != nil || tc.TaskID != 12 {
		t.Fatalf("retry: %v", err)
	}
}
