package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func smartFixture(t *testing.T) (*store.Store, *store.User, *store.SmartQueue, []store.SmartItem) {
	t.Helper()
	st, l := openStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, store.NewUser{Username: "rec", PasswordHash: "x", Role: "user", LocationID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	var items []store.SmartItem
	for i, name := range []string{"a.mp4", "b.mp4", "c.mp4"} {
		res, err := st.CommitFile(ctx, store.CommitRequest{User: u, DirSegs: []string{"Rec"}, Name: name, Size: int64(1000 * (i + 1)), SHA256: sha(byte('a' + i)), Mime: "video/mp4", ModifiedAt: 1, OnConflict: "rename", LocationID: l.ID})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, store.SmartItem{NodeID: res.Node.ID, SHA256: res.Node.SHA256, Name: name, Path: "/Rec/" + name, Size: res.Node.Size})
	}
	q, err := st.CreateSmartQueue(ctx, store.SmartQueue{UserID: u.ID, Name: "Q", State: "active", Filter: `{"rules":[]}`, Presets: "[]", Output: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.AddSmartItems(ctx, u.ID, q.ID, items)
	if err != nil || n != 3 {
		t.Fatalf("added %d: %v", n, err)
	}
	// Adding the same files again changes nothing.
	if n, _ := st.AddSmartItems(ctx, u.ID, q.ID, items); n != 0 {
		t.Fatalf("duplicates added: %d", n)
	}
	got, total, err := st.SmartItems(ctx, u.ID, q.ID, store.SmartItemsQuery{})
	if err != nil || total != 3 {
		t.Fatalf("items %d: %v", total, err)
	}
	return st, u, q, got
}

func TestSmartQueueClaimAnalyseReviewAndRecover(t *testing.T) {
	st, u, q, items := smartFixture(t)
	ctx := context.Background()

	// Claims go in queue order and only from active queues.
	it, err := st.ClaimSmartItem(ctx)
	if err != nil || it.ID != items[0].ID || it.State != "analysing" {
		t.Fatalf("claim: %+v %v", it, err)
	}
	if err := st.SetSmartQueueState(ctx, u.ID, q.ID, "paused"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimSmartItem(ctx); !errors.Is(err, store.ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claimed from a paused queue: %v", err)
	}
	if err := st.SetSmartQueueState(ctx, u.ID, q.ID, "active"); err != nil {
		t.Fatal(err)
	}

	// A restart puts interrupted analysis back in the queue.
	n, _, err := st.RecoverSmart(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	it, _ = st.ClaimSmartItem(ctx)
	if it.ID != items[0].ID {
		t.Fatalf("recovered item not first: %d", it.ID)
	}

	// Analysis stores suggestions.
	segs := []store.SmartSegment{{Start: 1, End: 5, Score: 0.9, Category: "emotion", Label: "Laughter", Reasons: "[]"}, {Start: 10, End: 20, Score: 0.7, Category: "audio", Label: "Loud", Reasons: "[]"}}
	if err := st.FinishSmartAnalysis(ctx, it, store.SmartAnalysed{State: "ready", SHA256: it.SHA256, Duration: 30, Detectors: "[]", Warnings: "[]", Events: 2, EvalRev: 0, ElapsedMS: 1500, Segments: segs}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.SmartSegments(ctx, it.ID)
	if len(got) != 2 || got[0].State != "suggested" || got[0].Origin != "auto" {
		t.Fatalf("segments: %+v", got)
	}
	// The user edits one and rejects the other; a re-evaluation keeps both
	// decisions and adds only new, non-overlapping suggestions.
	got[0].Start = 0.5
	if _, err := st.UpdateSmartSegment(ctx, u.ID, got[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSmartSegmentsState(ctx, u.ID, []int64{got[1].ID}, "rejected"); err != nil {
		t.Fatal(err)
	}
	again := []store.SmartSegment{{Start: 1, End: 5, Score: 0.95, Reasons: "[]"}, {Start: 11, End: 19, Score: 0.7, Reasons: "[]"}, {Start: 25, End: 28, Score: 0.6, Reasons: "[]"}}
	if err := st.FinishSmartAnalysis(ctx, it, store.SmartAnalysed{State: "ready", SHA256: it.SHA256, Duration: 30, Detectors: "[]", Warnings: "[]", EvalRev: 1, Segments: again}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.SmartSegments(ctx, it.ID)
	if len(got) != 3 || got[0].Origin != "edited" || got[0].Start != 0.5 || got[1].State != "rejected" || got[2].Start != 25 {
		t.Fatalf("after re-evaluation: %+v", got)
	}

	// A failed analysis is recorded with its error and counted.
	it2, _ := st.ClaimSmartItem(ctx)
	if err := st.FinishSmartAnalysis(ctx, it2, store.SmartAnalysed{Error: "decoding audio failed", ElapsedMS: 10}); err != nil {
		t.Fatal(err)
	}
	errs, _ := st.SmartErrors(ctx, q.ID, 10)
	if len(errs) != 1 || errs[0].Stage != "analysis" {
		t.Fatalf("errors: %+v", errs)
	}
	qq, _ := st.SmartQueue(ctx, u.ID, q.ID)
	if qq.Counters.Failures != 1 || qq.Counters.AnalysisMS != 1510 {
		t.Fatalf("counters: %+v", qq.Counters)
	}
	// Retry: failed items go back to pending.
	if n, _ := st.SetQueueItemsState(ctx, q.ID, []string{"failed"}, "pending"); n != 1 {
		t.Fatalf("retry reset %d", n)
	}

	// Changing the filter re-evaluates items waiting for review only.
	if err := st.SetSmartItemState(ctx, u.ID, it.ID, "reviewed", "ready"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSmartQueueFilter(ctx, u.ID, q.ID, `{"rules":[]}`, "[]"); err != nil {
		t.Fatal(err)
	}
	if x, _ := st.SmartItem(ctx, u.ID, it.ID); x.State != "reviewed" {
		t.Fatalf("reviewed item reset: %s", x.State)
	}
	tally, _ := st.SmartTally(ctx, q.ID)
	if tally.Items != 3 || tally.Segments["rejected"] != 1 {
		t.Fatalf("tally: %+v", tally)
	}
}

func TestSmartRendersAreNotDuplicated(t *testing.T) {
	st, u, q, items := smartFixture(t)
	ctx := context.Background()
	it, _ := st.ClaimSmartItem(ctx)
	_ = st.FinishSmartAnalysis(ctx, it, store.SmartAnalysed{State: "ready", SHA256: it.SHA256, Duration: 30, Detectors: "[]", Warnings: "[]",
		Segments: []store.SmartSegment{{Start: 1, End: 5, Score: 0.9, Reasons: "[]"}}, Approve: true})
	segs, _ := st.SmartSegments(ctx, it.ID)
	if segs[0].State != "approved" {
		t.Fatalf("auto-approve: %+v", segs[0])
	}
	r, err := st.ClaimSmartRender(ctx)
	if err != nil || r.ID != it.ID {
		t.Fatalf("render claim: %+v %v", r, err)
	}
	keys := map[int64]string{segs[0].ID: "1.00-5.00-auto"}
	todo, err := st.PlanSmartRenders(ctx, r, keys)
	if err != nil || len(todo) != 1 {
		t.Fatalf("plan: %+v %v", todo, err)
	}
	if err := st.StartSmartOutput(ctx, todo[0].ID, "a - Laughter 00-01.mp4"); err != nil {
		t.Fatal(err)
	}
	// A restart mid-render: the output is pending again, the item waits.
	if _, outs, err := st.RecoverSmart(ctx); err != nil || outs != 1 {
		t.Fatalf("recover outputs %d %v", outs, err)
	}
	r, _ = st.ClaimSmartRender(ctx)
	todo2, _ := st.PlanSmartRenders(ctx, r, keys)
	if len(todo2) != 1 || todo2[0].ID != todo[0].ID {
		t.Fatalf("replanned: %+v", todo2)
	}
	// The clip is committed with its output in one transaction.
	o := todo2[0]
	o.SHA256, o.Size, o.Name, o.Path, o.Mode, o.RenderMS = sha('z'), 500, "a - Laughter 00-01.mp4", "/Rec/a - Laughter 00-01.mp4", "copy", 120
	loc, _ := st.DefaultLocation(ctx)
	_, err = st.CommitFile(ctx, store.CommitRequest{User: u, DirSegs: []string{"Rec"}, Name: o.Name, Size: o.Size, SHA256: o.SHA256, Mime: "video/mp4", ModifiedAt: 1, OnConflict: "rename", LocationID: loc.ID,
		AfterTx: func(tx *sql.Tx, nodeID int64) error { return store.FinishSmartOutputTx(ctx, tx, &o, nodeID) }})
	if err != nil {
		t.Fatal(err)
	}
	// Planning again finds nothing to render: a retry never makes a copy.
	if todo3, _ := st.PlanSmartRenders(ctx, r, keys); len(todo3) != 0 {
		t.Fatalf("rendered twice: %+v", todo3)
	}
	outs, _ := st.SmartOutputs(ctx, it.ID)
	if len(outs) != 1 || outs[0].State != "done" || outs[0].NodeID == nil {
		t.Fatalf("outputs: %+v", outs)
	}
	qq, _ := st.SmartQueue(ctx, u.ID, q.ID)
	if qq.Counters.GeneratedCount != 1 || qq.Counters.GeneratedBytes != 500 {
		t.Fatalf("generated counters: %+v", qq.Counters)
	}
	// A changed boundary is a new render key: a new clip, the old one kept.
	keys[segs[0].ID] = "1.00-6.00-auto"
	if todo4, _ := st.PlanSmartRenders(ctx, r, keys); len(todo4) != 1 {
		t.Fatalf("new key not planned: %+v", todo4)
	}

	// Cleanup bookkeeping: removed originals and freed space are counted
	// once, and other queues holding the file see it gone.
	q2, _ := st.CreateSmartQueue(ctx, store.SmartQueue{UserID: u.ID, Name: "Q2", State: "active", Filter: "{}", Presets: "[]", Output: "{}"})
	_, _ = st.AddSmartItems(ctx, u.ID, q2.ID, items[:1])
	if err := st.MarkSmartOriginal(ctx, it, "deleted", 1000, 1000); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSmartSourceGone(ctx, u.ID, it.NodeID, it.ID); err != nil {
		t.Fatal(err)
	}
	qq, _ = st.SmartQueue(ctx, u.ID, q.ID)
	if qq.Counters.DeletedCount != 1 || qq.Counters.FreedBytes != 1000 {
		t.Fatalf("cleanup counters: %+v", qq.Counters)
	}
	other, _, _ := st.SmartItems(ctx, u.ID, q2.ID, store.SmartItemsQuery{})
	if other[0].State != "gone" || other[0].OriginalState != "removed" {
		t.Fatalf("other queue: %+v", other[0])
	}
}

func TestSmartRecordsAreScopedToTheirUser(t *testing.T) {
	st, u, q, items := smartFixture(t)
	ctx := context.Background()
	other, err := st.CreateUser(ctx, store.NewUser{Username: "other", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SmartQueue(ctx, other.ID, q.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user sees the queue: %v", err)
	}
	if _, err := st.SmartItem(ctx, other.ID, items[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user sees the item: %v", err)
	}
	if err := st.DeleteSmartQueue(ctx, other.ID, q.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other user deleted the queue: %v", err)
	}
	if n, _ := st.SetSmartSegmentsState(ctx, other.ID, []int64{1, 2, 3}, "approved"); n != 0 {
		t.Fatalf("other user changed segments: %d", n)
	}
	if qs, _ := st.SmartQueues(ctx, u.ID, []string{"active"}); len(qs) != 1 {
		t.Fatalf("queues: %+v", qs)
	}
}
