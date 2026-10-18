package client

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

func TestPollTasksAndResult(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("GET /api/v1/device/tasks", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("wait") != "25" {
			writeJSON(w, 200, wire.DeviceTaskPollResponse{Items: []wire.DeviceTask{}})
			return
		}
		writeJSON(w, 200, wire.DeviceTaskPollResponse{Items: []wire.DeviceTask{{ID: 3, Kind: wire.TaskKindRewrap, Payload: json.RawMessage(`{"repo_id":1,"epoch":2}`), Status: wire.TaskStatusClaimed}}})
	})
	f.handle("POST /api/v1/device/tasks/3/result", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.DeviceTaskResultRequest
		if err := strictDecode(body, &req); err != nil || string(req.Result) != "{}" {
			writeErr(w, 400, "invalid", nil)
			return
		}
		w.WriteHeader(204)
	})
	tasks, err := c.PollTasks(ctx, time.Minute) // clamped to 25 s
	if err != nil || len(tasks) != 1 {
		t.Fatalf("poll: %v %v", tasks, err)
	}
	p, err := tasks[0].DecodePayload()
	if rw, ok := p.(*wire.TaskRewrapPayload); err != nil || !ok || rw.Epoch != 2 {
		t.Fatalf("payload %#v %v", p, err)
	}
	if tasks, err := c.PollTasks(ctx, 0); err != nil || len(tasks) != 0 {
		t.Fatalf("empty poll: %v %v", tasks, err)
	}
	if err := c.TaskResult(ctx, 3, wire.TaskStatusDone, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.TaskResult(ctx, 3, wire.TaskStatusClaimed, nil); err == nil {
		t.Fatal("bad status accepted")
	}
}
