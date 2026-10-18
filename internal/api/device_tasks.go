// Owner: SRV-3 (P1-17). Scope: api-v1 §6 device tasks (feature
// device_tasks.v1); routes and shapes per rev 1.0
// (sdk/contracts/history/api-v1-rev1.0.md §6). Inventory routes are not
// implemented yet.
//
// Other feature areas queue work for a device with CreateTask; destructive
// kinds can be gated behind an approval in the same call.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func init() {
	implementFeature(wire.FeatureDeviceTasks)
	RegisterApprovalHandler(wire.ApprovalKindDeviceTask, ApprovalHandler{
		Digest: func(ctx context.Context, s *Server, a *store.Approval) (string, error) {
			t, err := s.st.Task(ctx, a.SubjectID)
			if err != nil {
				return "", err
			}
			return taskDigest(t)
		},
		Resume: func(ctx context.Context, s *Server, a *store.Approval, _ *OpProgress) (any, error) {
			if err := s.st.ReleaseTask(ctx, a.SubjectID); err != nil {
				if errors.Is(err, store.ErrTaskNotClaimable) {
					return nil, errf(409, wire.CodeTaskNotClaimable, "The task is no longer waiting for approval")
				}
				return nil, err
			}
			if t, err := s.st.Task(ctx, a.SubjectID); err == nil {
				s.taskHub().notify(t.DeviceID)
			}
			return nil, nil
		},
	})
	// Route the module feature's task queueing (modules.go) through CreateTask
	// so those tasks wake long-polls too.
	queueDeviceTask = func(s *Server, ctx context.Context, t deviceTaskSpec) (int64, error) {
		by := t.CreatedBy
		res, err := s.CreateTask(ctx, TaskRequest{UserID: t.UserID, DeviceID: t.DeviceID, Kind: t.Kind, Payload: t.Payload,
			RequiredModules: t.RequiredModules, CreatedBy: &by})
		if err != nil {
			return 0, err
		}
		return res.TaskID, nil
	}
}

// mountDeviceTasks registers the routes of this feature area.
func (s *Server) mountDeviceTasks(rt routes) {
	rt.Dev.Get("/device/tasks", s.handlePollTasks) // long-poll ?wait=
	rt.Dev.Post("/device/tasks/{id}/result", s.handleTaskResult)
	rt.Sess.Post("/device/tasks", s.handleCreateTask)
	rt.Sess.Get("/device/tasks/list", s.handleListTasks)
	rt.Dev.Put("/device/inventory", notImplemented) // streamed (api-v1 §1.2): verify the body with newStreamedBody before committing state; discovery module
	rt.Sess.Get("/devices/{id}/apps", notImplemented)
}

// Long-poll and lease settings.
const (
	maxTaskWait      = 25 * time.Second // api-v1 §2 long_poll_seconds, rev 1.0 §6 wait=0..25
	maxTasksPerPoll  = 20
	taskLease        = store.DefaultTaskLease
	maxTaskPayload   = 64 << 10
	maxTaskResultLen = 1 << 20
)

// --- in-process wake-up ---

// taskNotifier wakes long-polls of a device when a task becomes claimable.
type taskNotifier struct {
	mu      sync.Mutex
	waiters map[int64]chan struct{}
}

// wait returns a channel closed by the next notify for deviceID.
func (n *taskNotifier) wait(deviceID int64) <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	ch, ok := n.waiters[deviceID]
	if !ok {
		ch = make(chan struct{})
		n.waiters[deviceID] = ch
	}
	return ch
}

// notify wakes every poll of deviceID waiting now.
func (n *taskNotifier) notify(deviceID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if ch, ok := n.waiters[deviceID]; ok {
		close(ch)
		delete(n.waiters, deviceID)
	}
}

// taskHubs holds one notifier per Server (server.go is not owned by this
// feature, so the notifier is not a Server field).
var taskHubs sync.Map // *Server -> *taskNotifier

func (s *Server) taskHub() *taskNotifier {
	if v, ok := taskHubs.Load(s); ok {
		return v.(*taskNotifier)
	}
	v, _ := taskHubs.LoadOrStore(s, &taskNotifier{waiters: map[int64]chan struct{}{}})
	return v.(*taskNotifier)
}

// --- task result hooks ---

// TaskResultHook runs after a device reports the result of a task of its
// kind (for example to move a restore to completed). Errors are logged.
type TaskResultHook func(ctx context.Context, s *Server, t *store.DeviceTask) error

var (
	taskHooksMu sync.RWMutex
	taskHooks   = map[string][]TaskResultHook{}
)

// RegisterTaskResultHook adds a result hook for a task kind; call it from
// the feature's init().
func RegisterTaskResultHook(kind string, h TaskResultHook) {
	taskHooksMu.Lock()
	taskHooks[kind] = append(taskHooks[kind], h)
	taskHooksMu.Unlock()
}

func (s *Server) runTaskHooks(ctx context.Context, t *store.DeviceTask) {
	taskHooksMu.RLock()
	hooks := taskHooks[t.Kind]
	taskHooksMu.RUnlock()
	for _, h := range hooks {
		if err := h(ctx, s, t); err != nil {
			slog.Error("task result hook failed", "task_id", t.ID, "kind", t.Kind, "err", err)
		}
	}
}

// --- creating tasks ---

// TaskRequest describes a task to queue for a device. UserID is the device
// owner. Payload is a typed wire payload (or json.RawMessage) and is
// validated with wire.DecodeTaskPayload. CreatedBy is the creating user (nil
// for the server). RequiredModules are checked against the device's modules
// at creation (409 module_unavailable) and again at delivery. Approval, when
// set, gates the task: it waits until approved and the reply carries the
// approval id; Kind/Subject/PlanDigest of the request are filled in.
type TaskRequest struct {
	UserID          int64
	DeviceID        int64
	Kind            string
	Payload         any
	RequiredModules []string
	CreatedBy       *int64
	Approval        *ApprovalRequest
}

// CreateTask validates and queues a device task, wakes the device's
// long-polls and returns {task_id, approval_id?}.
func (s *Server) CreateTask(ctx context.Context, req TaskRequest) (*wire.DeviceTaskCreated, error) {
	raw, err := taskPayload(req.Kind, req.Payload)
	if err != nil {
		return nil, err
	}
	mods := req.RequiredModules
	if mods == nil {
		mods = defaultTaskModules(req.Kind)
	}
	if err := s.requireModules(ctx, req.DeviceID, mods...); err != nil {
		return nil, err
	}
	t := &store.DeviceTask{UserID: req.UserID, DeviceID: req.DeviceID, Kind: req.Kind, Payload: string(raw),
		RequiredModules: mods, CreatedBy: req.CreatedBy}
	if req.Approval != nil {
		t.Status = store.TaskStatusAwaitingApproval
	}
	if err := s.st.CreateTask(ctx, t); err != nil {
		return nil, err
	}
	out := &wire.DeviceTaskCreated{TaskID: t.ID}
	if req.Approval == nil {
		s.taskHub().notify(t.DeviceID)
		return out, nil
	}
	ar := *req.Approval
	ar.Kind, ar.SubjectType, ar.SubjectID = wire.ApprovalKindDeviceTask, wire.ApprovalSubjectDeviceTask, t.ID
	if ar.UserID == 0 {
		ar.UserID = req.UserID
	}
	if ar.Plan == nil && ar.PlanDigest == "" {
		if ar.PlanDigest, err = taskDigest(t); err != nil {
			return nil, err
		}
	}
	a, err := s.createApproval(ctx, ar)
	if err != nil {
		_ = s.st.CancelTask(context.WithoutCancel(ctx), t.ID, "approval could not be created")
		return nil, err
	}
	if err := s.st.SetTaskApproval(ctx, t.ID, a.ID); err != nil {
		return nil, err
	}
	out.ApprovalID = &a.ID
	return out, nil
}

// taskPayload marshals and validates a payload for kind.
func taskPayload(kind string, payload any) (json.RawMessage, error) {
	var raw json.RawMessage
	switch v := payload.(type) {
	case nil:
		raw = json.RawMessage(`{}`)
	case json.RawMessage:
		raw = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	if len(raw) > maxTaskPayload {
		return nil, taskValidation("payload", "too large")
	}
	if _, err := wire.DecodeTaskPayload(kind, raw); err != nil {
		if errors.Is(err, wire.ErrUnknownTaskKind) {
			return nil, taskValidation("kind", "unknown device task kind")
		}
		return nil, taskValidation("payload", err.Error())
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, taskValidation("payload", "invalid JSON")
	}
	return compact.Bytes(), nil
}

func taskValidation(field, msg string) *apiError {
	return &apiError{Status: 400, Code: "validation", Message: "Invalid device task",
		Details: map[string]any{"fields": map[string]string{field: msg}}}
}

// defaultTaskModules is the module gate per kind when the caller gives none.
func defaultTaskModules(kind string) []string {
	switch kind {
	case wire.TaskKindAutomationOp:
		return []string{wire.ModuleAutomation}
	case wire.TaskKindProfileConsentRequest:
		return []string{wire.ModuleProfiles}
	}
	return []string{}
}

// destructiveTaskKind reports kinds that need an approval when created
// through POST /device/tasks (rev 1.0 §6).
func destructiveTaskKind(kind string) bool {
	return kind == wire.TaskKindRestore || kind == wire.TaskKindPruneCompact
}

// taskDigest is the plan digest bound to a device_task approval: the
// canonical {device_id, kind, payload}.
func taskDigest(t *store.DeviceTask) (string, error) {
	_, d, err := store.PlanDigest(map[string]any{"device_id": t.DeviceID, "kind": t.Kind, "payload": json.RawMessage(t.Payload)})
	return d, err
}

func taskView(t *store.DeviceTask) wire.DeviceTask {
	status := t.Status
	if status == store.TaskStatusAwaitingApproval {
		status = wire.TaskStatusPending
	}
	createdBy := wire.TaskCreatedByServer
	if t.CreatedBy != nil {
		createdBy = wire.TaskCreatedByUser
	}
	v := wire.DeviceTask{ID: t.ID, DeviceID: t.DeviceID, Kind: t.Kind, Payload: json.RawMessage(t.Payload), Status: status,
		ApprovalID: t.ApprovalID, RequiredModules: t.RequiredModules, CreatedBy: createdBy, CreatedAt: t.CreatedAt,
		Result: json.RawMessage(t.Result)}
	if t.ClaimedAt != nil {
		v.ClaimedAt = *t.ClaimedAt
	}
	if t.FinishedAt != nil {
		v.FinishedAt = *t.FinishedAt
	}
	return v
}

// --- routes ---

// handlePollTasks answers GET /device/tasks?wait=0..25: it returns as soon
// as at least one task is claimable (claiming every returned task with a
// lease), else an empty list after wait seconds.
func (s *Server) handlePollTasks(w http.ResponseWriter, r *http.Request) {
	wait := time.Duration(0)
	if v := r.URL.Query().Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, r, taskValidation("wait", "must be 0..25 seconds"))
			return
		}
		wait = min(time.Duration(n)*time.Second, maxTaskWait)
	}
	dev := principal(r).Device
	ctx := r.Context()
	deadline := time.Now().Add(wait)
	hub := s.taskHub()
	for {
		woken := hub.wait(dev.ID) // before claiming, so a task created meanwhile still wakes us
		tasks, err := s.claimTasks(ctx, dev.ID)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		left := time.Until(deadline)
		if len(tasks) > 0 || left <= 0 {
			out := wire.DeviceTaskPollResponse{Items: make([]wire.DeviceTask, 0, len(tasks))}
			for i := range tasks {
				out.Items = append(out.Items, taskView(&tasks[i]))
			}
			writeJSON(w, 200, out)
			return
		}
		timer := time.NewTimer(left)
		select {
		case <-woken:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

// claimTasks expires stale leases and claims the device's claimable tasks.
func (s *Server) claimTasks(ctx context.Context, deviceID int64) ([]store.DeviceTask, error) {
	_, failed, err := s.st.ExpireTaskLeases(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range failed {
		if t, err := s.st.Task(ctx, id); err == nil {
			s.runTaskHooks(ctx, t)
		}
	}
	mods, err := s.enabledModules(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(mods))
	for _, m := range mods {
		enabled[m] = true
	}
	return s.st.ClaimTasks(ctx, deviceID, maxTasksPerPoll, taskLease, enabled)
}

func (s *Server) handleTaskResult(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.DeviceTaskResultRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Status != wire.TaskStatusDone && req.Status != wire.TaskStatusFailed {
		writeErr(w, r, taskValidation("status", "must be done or failed"))
		return
	}
	result := "{}"
	if len(bytes.TrimSpace(req.Result)) > 0 && !bytes.Equal(bytes.TrimSpace(req.Result), []byte("null")) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(req.Result, &obj); err != nil {
			writeErr(w, r, taskValidation("result", "must be a JSON object"))
			return
		}
		if len(req.Result) > maxTaskResultLen {
			writeErr(w, r, taskValidation("result", "too large"))
			return
		}
		var compact bytes.Buffer
		_ = json.Compact(&compact, req.Result)
		result = compact.String()
	}
	dev := principal(r).Device
	t, err := s.st.CompleteTask(r.Context(), id, dev.ID, req.Status, result)
	if errors.Is(err, store.ErrTaskNotClaimable) {
		writeErr(w, r, errf(409, wire.CodeTaskNotClaimable, "This task is not claimed by this device"))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.runTaskHooks(r.Context(), t)
	w.WriteHeader(http.StatusNoContent)
}

// handleCreateTask answers POST /device/tasks (sess). The device's owner or
// an admin may queue a task; destructive kinds reply 202 with an approval.
func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req wire.DeviceTaskCreateRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	p := principal(r)
	d, err := s.st.Device(r.Context(), req.DeviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && d.UserID != p.User.ID && !p.User.IsAdmin()) {
		writeErr(w, r, taskValidation("device_id", "unknown device"))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if d.Status != "approved" {
		writeErr(w, r, errf(409, "device_not_approved", "The device is not approved"))
		return
	}
	uid := p.User.ID
	tr := TaskRequest{UserID: d.UserID, DeviceID: d.ID, Kind: req.Kind, Payload: req.Payload, CreatedBy: &uid}
	if destructiveTaskKind(req.Kind) {
		tr.Approval = &ApprovalRequest{
			Risk: wire.ApprovalRiskHigh, SubjectName: d.Name,
			Summary:           fmt.Sprintf("Run %s on device %s", req.Kind, d.Name),
			RequestedBy:       wire.ApprovalRequestedByUser,
			RequesterRef:      p.User.Username,
			RequestedByUserID: &uid,
		}
	}
	out, err := s.CreateTask(r.Context(), tr)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, d.UserID, "device.task", d.Name, fmt.Sprintf("task_id=%d kind=%s", out.TaskID, req.Kind), false)
	status := http.StatusCreated
	if out.ApprovalID != nil {
		status = http.StatusAccepted
	}
	writeJSON(w, status, out)
}

// handleListTasks answers GET /device/tasks/list?device=&status=&cursor=
// (sess) with the caller's tasks, newest first.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.TaskFilter{UserID: principal(r).User.ID, Status: q.Get("status")}
	switch f.Status {
	case "", wire.TaskStatusPending, wire.TaskStatusClaimed, wire.TaskStatusDone, wire.TaskStatusFailed, wire.TaskStatusCancelled:
	default:
		writeErr(w, r, taskValidation("status", "unknown task status"))
		return
	}
	if v := q.Get("device"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeErr(w, r, taskValidation("device", "invalid device id"))
			return
		}
		f.DeviceID = n
	}
	var err error
	if f.BeforeID, f.Limit, err = listParams(r); err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListTasks(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.DeviceTask]{Items: make([]wire.DeviceTask, 0, len(list))}
	for i := range list {
		out.Items = append(out.Items, taskView(&list[i]))
	}
	if len(list) == f.Limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}
