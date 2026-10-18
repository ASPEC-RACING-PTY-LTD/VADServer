package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func newTaskDevice(t *testing.T, st *store.Store, u *store.User, name string) *store.Device {
	t.Helper()
	d := &store.Device{UserID: u.ID, Name: name, Identifier: "id-" + name, PublicKey: "pk-" + name, KeyFingerprint: "fp-" + name, CreatedAt: store.Now()}
	if err := st.CreateDevice(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDeviceTaskClaimAndComplete(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d1, d2 := newTaskDevice(t, st, u, "pc1"), newTaskDevice(t, st, u, "pc2")

	task := &store.DeviceTask{UserID: u.ID, DeviceID: d1.ID, Kind: wire.TaskKindBackupNow, Payload: `{"job_id":1}`, CreatedBy: &u.ID}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if task.ID == 0 || task.Status != wire.TaskStatusPending {
		t.Fatalf("created: %+v", task)
	}
	// Another device cannot claim it.
	if _, err := st.ClaimTask(ctx, task.ID, d2.ID, time.Minute); !errors.Is(err, store.ErrTaskNotClaimable) {
		t.Fatalf("foreign claim: %v", err)
	}
	got, err := st.ClaimTask(ctx, task.ID, d1.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != wire.TaskStatusClaimed || got.Attempts != 1 || got.ClaimedAt == nil || got.ExpiresAt == nil {
		t.Fatalf("claimed: %+v", got)
	}
	// Double claim fails.
	if _, err := st.ClaimTask(ctx, task.ID, d1.ID, time.Minute); !errors.Is(err, store.ErrTaskNotClaimable) {
		t.Fatalf("double claim: %v", err)
	}
	// Only the claiming device completes it, once.
	if _, err := st.CompleteTask(ctx, task.ID, d2.ID, wire.TaskStatusDone, ""); !errors.Is(err, store.ErrTaskNotClaimable) {
		t.Fatalf("foreign complete: %v", err)
	}
	if _, err := st.CompleteTask(ctx, task.ID, d1.ID, "claimed", ""); err == nil {
		t.Fatal("bad result status accepted")
	}
	done, err := st.CompleteTask(ctx, task.ID, d1.ID, wire.TaskStatusDone, `{"ok":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != wire.TaskStatusDone || done.Result != `{"ok":true}` || done.FinishedAt == nil || done.ExpiresAt != nil {
		t.Fatalf("done: %+v", done)
	}
	if _, err := st.CompleteTask(ctx, task.ID, d1.ID, wire.TaskStatusFailed, ""); !errors.Is(err, store.ErrTaskNotClaimable) {
		t.Fatalf("second complete: %v", err)
	}
}

func TestDeviceTaskConcurrentClaims(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newTaskDevice(t, st, u, "pc1")
	for i := 0; i < 5; i++ {
		if err := st.CreateTask(ctx, &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindBackupNow, Payload: `{"job_id":1}`}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	seen := map[int64]int{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := st.ClaimTasks(ctx, d.ID, 10, time.Minute, nil)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			for _, g := range got {
				seen[g.ID]++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(seen) != 5 {
		t.Fatalf("claimed %d distinct tasks, want 5", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("task %d claimed %d times", id, n)
		}
	}
}

func TestDeviceTaskLeaseExpiry(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newTaskDevice(t, st, u, "pc1")
	task := &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindBackupNow, Payload: `{"job_id":1}`}
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= store.MaxTaskAttempts; attempt++ {
		if _, err := st.ClaimTask(ctx, task.ID, d.ID, time.Millisecond); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		time.Sleep(5 * time.Millisecond)
		requeued, failed, err := st.ExpireTaskLeases(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := st.Task(ctx, task.ID)
		if attempt < store.MaxTaskAttempts {
			if len(requeued) != 1 || len(failed) != 0 || got.Status != wire.TaskStatusPending || got.ClaimedAt != nil {
				t.Fatalf("attempt %d: requeued=%v failed=%v %+v", attempt, requeued, failed, got)
			}
			// A late result after the requeue is refused.
			if _, err := st.CompleteTask(ctx, task.ID, d.ID, wire.TaskStatusDone, ""); !errors.Is(err, store.ErrTaskNotClaimable) {
				t.Fatalf("late result: %v", err)
			}
			continue
		}
		if len(failed) != 1 || got.Status != wire.TaskStatusFailed || got.ErrorCode != store.TaskErrorLeaseExpired {
			t.Fatalf("final: requeued=%v failed=%v %+v", requeued, failed, got)
		}
	}
	// A live lease is left alone.
	live := &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindBackupNow, Payload: `{"job_id":2}`}
	if err := st.CreateTask(ctx, live); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimTask(ctx, live.ID, d.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if rq, fl, err := st.ExpireTaskLeases(ctx); err != nil || len(rq)+len(fl) != 0 {
		t.Fatalf("live lease expired: %v %v %v", rq, fl, err)
	}
}

func TestDeviceTaskModulesApprovalAndList(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newTaskDevice(t, st, u, "pc1")

	auto := &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindAutomationOp, RequiredModules: []string{wire.ModuleAutomation}}
	gated := &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindRestore, Payload: `{"restore_id":1}`, Status: store.TaskStatusAwaitingApproval}
	plain := &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindBackupNow, Payload: `{"job_id":1}`}
	for _, tk := range []*store.DeviceTask{auto, gated, plain} {
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateTask(ctx, &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: "x", Status: wire.TaskStatusDone}); err == nil {
		t.Fatal("bad initial status accepted")
	}

	got, err := st.ClaimTasks(ctx, d.ID, 10, time.Minute, map[string]bool{wire.ModuleCore: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != plain.ID {
		t.Fatalf("claimed %+v", got)
	}
	if a, _ := st.Task(ctx, auto.ID); a.Status != wire.TaskStatusFailed || a.ErrorCode != wire.CodeModuleUnavailable {
		t.Fatalf("module-gated task: %+v", a)
	}
	if g, _ := st.Task(ctx, gated.ID); g.Status != store.TaskStatusAwaitingApproval {
		t.Fatalf("gated task: %+v", g)
	}

	// Approval-gated: bound, released, then claimable.
	a := &store.Approval{UserID: u.ID, Kind: wire.ApprovalKindDeviceTask, SubjectType: wire.ApprovalSubjectDeviceTask, SubjectID: gated.ID}
	if err := st.CreateApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTaskApproval(ctx, gated.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideApproval(ctx, a.ID, u.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseTask(ctx, gated.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseTask(ctx, gated.ID); !errors.Is(err, store.ErrTaskNotClaimable) {
		t.Fatalf("second release: %v", err)
	}
	if got, err := st.ClaimTasks(ctx, d.ID, 10, time.Minute, nil); err != nil || len(got) != 1 || got[0].ID != gated.ID {
		t.Fatalf("claim released: %+v %v", got, err)
	}

	// List: newest first, status filter, paging.
	all, err := st.ListTasks(ctx, store.TaskFilter{UserID: u.ID})
	if err != nil || len(all) != 3 || all[0].ID != plain.ID {
		t.Fatalf("list: %d %v", len(all), err)
	}
	if f, _ := st.ListTasks(ctx, store.TaskFilter{UserID: u.ID, Status: wire.TaskStatusFailed}); len(f) != 1 || f[0].ID != auto.ID {
		t.Fatalf("failed filter: %+v", f)
	}
	if p, _ := st.ListTasks(ctx, store.TaskFilter{UserID: u.ID, BeforeID: plain.ID, Limit: 1}); len(p) != 1 || p[0].ID != gated.ID {
		t.Fatalf("page: %+v", p)
	}
	other := newUser(t, st, "bob")
	if o, _ := st.ListTasks(ctx, store.TaskFilter{UserID: other.ID}); len(o) != 0 {
		t.Fatalf("foreign list: %+v", o)
	}
}
