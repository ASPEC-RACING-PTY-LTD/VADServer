package api

// Smart processing cleanup and statistics.
//
// Cleanup never runs by itself: the user picks originals (one, many, a
// folder's or a queue's) and confirms. Each original is checked again on
// the server right before it is removed: it must be unchanged, every
// approved segment must have a clip that still exists with its recorded
// content and plays back (probed), and it must not itself be a clip of
// another recording. Only the chosen files of the signed-in user are
// touched; folders are never removed.

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// cleanupState says whether an original may be removed.
type cleanupState struct {
	ItemID  int64  `json:"item_id"`
	QueueID int64  `json:"queue_id"`
	NodeID  int64  `json:"node_id"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	// Status: eligible (every approved segment has a verified clip),
	// no_segments (reviewed, nothing was kept), blocked (see Reason) or
	// cleaned (already removed).
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Clips     int    `json:"clips"`
	ClipBytes int64  `json:"clip_bytes"`
	Approved  int    `json:"approved"`
}

// cleanupEligibility checks one item. With verify the clips are opened
// and probed (done before removing anything); without, their records and
// files are checked.
func (s *Server) cleanupEligibility(ctx context.Context, it *store.SmartItem, verify bool) (*cleanupState, error) {
	c := &cleanupState{ItemID: it.ID, QueueID: it.QueueID, NodeID: it.NodeID, Name: it.Name, Path: it.Path, Size: it.Size}
	block := func(format string, a ...any) (*cleanupState, error) {
		c.Status, c.Reason = "blocked", fmt.Sprintf(format, a...)
		return c, nil
	}
	if it.OriginalState != "kept" {
		c.Status = "cleaned"
		return c, nil
	}
	switch it.State {
	case "done":
	case "pending", "analysing":
		return block("not analysed yet")
	case "ready", "skipped":
		return block("waiting for review")
	case "reviewed", "rendering":
		return block("clips are still being rendered")
	case "failed":
		return block("processing failed; retry it first")
	default:
		return block("not processed (%s)", it.State)
	}
	src, err := s.st.Node(ctx, it.UserID, it.NodeID)
	if err != nil {
		return block("the file is no longer in its folder")
	}
	if src.SHA256 != it.SHA256 {
		return block("the file changed after it was processed")
	}
	c.Size, c.Name = src.Size, src.Name
	segs, err := s.st.SmartSegments(ctx, it.ID)
	if err != nil {
		return nil, err
	}
	outs, err := s.st.SmartOutputs(ctx, it.ID)
	if err != nil {
		return nil, err
	}
	bySeg := map[int64][]store.SmartOutput{}
	for _, o := range outs {
		if o.State == "done" && o.NodeID != nil {
			bySeg[o.SegmentID] = append(bySeg[o.SegmentID], o)
		}
	}
	missing := 0
	for _, g := range segs {
		if g.State != "approved" {
			continue
		}
		c.Approved++
		ok := false
		for _, o := range bySeg[g.ID] {
			if s.clipValid(ctx, it.UserID, &o, g.End-g.Start, verify) {
				ok = true
				c.Clips++
				c.ClipBytes += o.Size
				break
			}
		}
		if !ok {
			missing++
		}
	}
	if missing > 0 {
		return block("%d of %d clips are missing or could not be verified", missing, c.Approved)
	}
	var clipOf int
	if err := s.st.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM smart_outputs o JOIN smart_items i ON i.id = o.item_id
		WHERE o.user_id = ? AND o.node_id = ? AND o.state = 'done' AND i.original_state = 'kept'`, it.UserID, it.NodeID).Scan(&clipOf); err != nil {
		return nil, err
	}
	if clipOf > 0 {
		return block("it is a clip of another recording that is still kept")
	}
	if c.Approved == 0 {
		c.Status = "no_segments"
		return c, nil
	}
	c.Status = "eligible"
	return c, nil
}

// clipValid: the clip's file still exists with the content it was
// rendered with and (verify) plays back with the expected length.
func (s *Server) clipValid(ctx context.Context, userID int64, o *store.SmartOutput, want float64, verify bool) bool {
	n, err := s.st.Node(ctx, userID, *o.NodeID)
	if err != nil || n.SHA256 != o.SHA256 || n.SHA256 == "" {
		return false
	}
	if !verify {
		return true
	}
	f, err := s.openBlob(ctx, userID, n.SHA256)
	if err != nil {
		return false
	}
	defer f.Close()
	in, release := f.Media()
	defer release()
	info, err := s.media.tools.Probe(ctx, in)
	if err != nil || info.Duration <= 0 {
		return false
	}
	// Stream copies start at the keyframe before the cut, so they may be a
	// little longer than the segment.
	tol := math.Max(1.5, want*0.05)
	return info.Duration >= want-tol && info.Duration <= want+tol+10
}

// cleanupItems selects the user's items for a cleanup listing: of one
// queue, of a folder's recordings (recursive) or all.
func (s *Server) cleanupItems(r *http.Request) ([]store.SmartItem, error) {
	u := principal(r).User
	qs := r.URL.Query()
	queueID, _ := strconv.ParseInt(qs.Get("queue_id"), 10, 64)
	folderID, _ := strconv.ParseInt(qs.Get("folder_id"), 10, 64)
	var items []store.SmartItem
	var err error
	if queueID > 0 {
		q, qerr := s.st.SmartQueue(r.Context(), u.ID, queueID)
		if qerr != nil {
			return nil, qerr
		}
		items, _, err = s.st.SmartItems(r.Context(), u.ID, q.ID, store.SmartItemsQuery{})
	} else {
		items, err = s.st.SmartItemsOfUser(r.Context(), u.ID, nil)
	}
	if err != nil || folderID <= 0 {
		return items, err
	}
	vids, err := s.resolveVideos(r.Context(), u, queueSources{FolderIDs: []int64{folderID}, Recursive: qs.Get("recursive") != "0"})
	if err != nil {
		return nil, err
	}
	in := map[int64]bool{}
	for _, v := range vids {
		in[v.NodeID] = true
	}
	out := items[:0]
	for _, it := range items {
		if in[it.NodeID] {
			out = append(out, it)
		}
	}
	return out, nil
}

// handleSmartCleanupList shows which originals can be removed and how
// much storage that would give back.
func (s *Server) handleSmartCleanupList(w http.ResponseWriter, r *http.Request) {
	items, err := s.cleanupItems(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := []*cleanupState{}
	totals := map[string]map[string]int64{}
	seen := map[int64]bool{} // a file queued twice counts once
	for i := range items {
		c, err := s.cleanupEligibility(r.Context(), &items[i], false)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if c.Status == "cleaned" {
			continue
		}
		if seen[c.NodeID] && c.Status != "eligible" {
			continue
		}
		out = append(out, c)
		t := totals[c.Status]
		if t == nil {
			t = map[string]int64{}
			totals[c.Status] = t
		}
		if !seen[c.NodeID] {
			t["count"]++
			t["bytes"] += c.Size
			t["clip_bytes"] += c.ClipBytes
		}
		seen[c.NodeID] = true
	}
	writeJSON(w, 200, map[string]any{"items": out, "totals": totals})
}

type cleanupResult struct {
	ItemID int64  `json:"item_id"`
	Name   string `json:"name"`
	Status string `json:"status"` // deleted, trashed, skipped, failed
	Reason string `json:"reason,omitempty"`
	Bytes  int64  `json:"bytes"`
	Freed  int64  `json:"freed"`
}

// handleSmartCleanup removes the chosen originals after checking each one
// again. Mode delete removes them permanently (storage is freed now);
// trash moves them to the trash (restorable for 7 days). Originals with
// no approved segments are only removed when include_no_segments is set.
func (s *Server) handleSmartCleanup(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		ItemIDs           []int64 `json:"item_ids"`
		Mode              string  `json:"mode"`
		IncludeNoSegments bool    `json:"include_no_segments"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Mode == "" {
		req.Mode = "delete"
	}
	if req.Mode != "delete" && req.Mode != "trash" {
		writeErr(w, r, errf(400, "invalid_mode", "Choose delete or trash"))
		return
	}
	if len(req.ItemIDs) == 0 || len(req.ItemIDs) > maxQueueItems {
		writeErr(w, r, errf(400, "invalid_selection", "Choose 1-%d recordings", maxQueueItems))
		return
	}
	ctx := context.WithoutCancel(r.Context()) // a closed tab never leaves a removal half recorded
	results := make([]cleanupResult, 0, len(req.ItemIDs))
	var deleted, bytes, freed int64
	done := map[int64]bool{}
	for _, id := range req.ItemIDs {
		it, err := s.st.SmartItem(ctx, u.ID, id)
		if err != nil {
			results = append(results, cleanupResult{ItemID: id, Status: "skipped", Reason: "not found"})
			continue
		}
		res := cleanupResult{ItemID: it.ID, Name: it.Name}
		if done[it.NodeID] {
			res.Status, res.Reason = "skipped", "already removed with another queue's entry"
			results = append(results, res)
			continue
		}
		c, err := s.cleanupEligibility(ctx, it, true)
		switch {
		case err != nil:
			res.Status, res.Reason = "failed", "it could not be checked"
		case c.Status == "eligible" || (c.Status == "no_segments" && req.IncludeNoSegments):
			res.Bytes = c.Size
			res.Freed, err = s.removeOriginal(ctx, u, it, req.Mode)
			if err != nil {
				res.Status, res.Reason = "failed", exportUserMessage(err)
				break
			}
			res.Status = map[string]string{"delete": "deleted", "trash": "trashed"}[req.Mode]
			done[it.NodeID] = true
			deleted++
			bytes += res.Bytes
			freed += res.Freed
		case c.Status == "no_segments":
			res.Status, res.Reason = "skipped", "no segments were approved; include those explicitly to remove it"
		case c.Status == "cleaned":
			res.Status, res.Reason = "skipped", "already removed"
		default:
			res.Status, res.Reason = "skipped", c.Reason
		}
		results = append(results, res)
	}
	if deleted > 0 {
		s.activity(r, u.ID, "smart.cleanup", fmt.Sprintf("%d original(s)", deleted), fmt.Sprintf("%s; %d bytes, %d bytes freed", req.Mode, bytes, freed), false)
	}
	writeJSON(w, 200, map[string]any{"results": results, "removed": deleted, "bytes": bytes, "freed": freed})
}

// removeOriginal deletes or trashes an original and records it. It
// returns the bytes actually freed on disk (0 when other files share the
// content or it went to the trash).
func (s *Server) removeOriginal(ctx context.Context, u *store.User, it *store.SmartItem, mode string) (int64, error) {
	cur, err := s.st.Node(ctx, u.ID, it.NodeID)
	if err != nil {
		return 0, errf(404, "source_missing", "it is no longer in its folder")
	}
	if cur.SHA256 != it.SHA256 {
		return 0, errf(409, "source_changed", "it changed after it was processed")
	}
	p, _ := s.st.NodePath(ctx, u.ID, cur.ID)
	if err := s.st.Trash(ctx, u, cur.ID); err != nil {
		return 0, err
	}
	// The desktop client would otherwise upload the original again from
	// the device it came from.
	if err := s.st.BlockReupload(ctx, u.ID, p, cur.SHA256, time.Now().Add(reuploadBlockFor).UnixMilli()); err != nil {
		logSmart("re-upload block not stored", "node", cur.ID, "err", err)
	}
	var freed int64
	state := "trashed"
	if mode == "delete" {
		_, refs, err := s.st.PurgeTrash(ctx, u.ID, []int64{cur.ID}, 0)
		if err != nil {
			return 0, err
		}
		for _, ref := range refs {
			if ref.SHA256 == cur.SHA256 {
				freed = cur.Size
			}
		}
		s.blobs.Remove(ctx, refs)
		state = "deleted"
		s.st.LogActivity(ctx, store.Activity{UserID: &u.ID, ActorID: &u.ID, Action: "file.deleted", Target: p, Details: "original permanently deleted by smart cleanup after its clips were verified"})
	} else {
		if err := s.st.SetTrashExpiry(ctx, u.ID, cur.ID, time.Now().Add(eraseTrashDays*24*time.Hour).UnixMilli()); err != nil {
			logSmart("trash expiry not set", "node", cur.ID, "err", err)
		}
		s.st.LogActivity(ctx, store.Activity{UserID: &u.ID, ActorID: &u.ID, Action: "file.trashed", Target: p, Details: "original moved to the trash by smart cleanup after its clips were verified; permanently deleted after 7 days"})
	}
	if err := s.st.MarkSmartOriginal(ctx, it, state, cur.Size, freed); err != nil {
		return freed, err
	}
	if err := s.st.MarkSmartSourceGone(ctx, u.ID, cur.ID, it.ID); err != nil {
		logSmart("other queue entries not updated", "node", cur.ID, "err", err)
	}
	_ = s.smartAnalyzer().Forget(u.ID, cur.SHA256)
	return freed, nil
}

// queueStats are a queue's lifetime numbers.
type queueStats struct {
	QueueID int64  `json:"queue_id"`
	Name    string `json:"name"`
	State   string `json:"state"`
	store.SmartCounters
	Tally *store.SmartQueueTally `json:"tally"`
	// Completion: share of recordings with nothing left to do.
	Completion float64 `json:"completion"`
	// EstimatedBytes: originals that could be removed now (eligible).
	EstimatedBytes int64 `json:"estimated_bytes"`
	EstimatedCount int64 `json:"estimated_count"`
	// NetBytes: storage given back after counting the clips made
	// (freed minus generated; negative while originals are kept).
	NetBytes  int64  `json:"net_bytes"`
	CreatedAt int64  `json:"created_at"`
	DoneAt    *int64 `json:"completed_at,omitempty"`
}

// handleSmartStats reports lifetime statistics per queue and in total.
// Confirmed savings come from the recorded removals (freed bytes), so
// they stay accurate whatever happens to the files later.
func (s *Server) handleSmartStats(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	ctx := r.Context()
	qs, err := s.st.SmartQueues(ctx, u.ID, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	want, _ := strconv.ParseInt(r.URL.Query().Get("queue_id"), 10, 64)
	out := []queueStats{}
	var total queueStats
	total.Name = "All queues"
	for _, q := range qs {
		if want > 0 && q.ID != want {
			continue
		}
		t, err := s.st.SmartTally(ctx, q.ID)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		st := queueStats{QueueID: q.ID, Name: q.Name, State: q.State, SmartCounters: q.Counters, Tally: t, CreatedAt: q.CreatedAt, DoneAt: q.CompletedAt}
		if t.Items > 0 {
			var finished int64
			for _, k := range []string{"done", "failed", "cancelled", "gone"} {
				finished += t.ItemStates[k]
			}
			st.Completion = math.Round(float64(finished)/float64(t.Items)*1000) / 1000
		}
		items, _, err := s.st.SmartItems(ctx, u.ID, q.ID, store.SmartItemsQuery{States: []string{"done"}})
		if err != nil {
			writeErr(w, r, err)
			return
		}
		for i := range items {
			c, err := s.cleanupEligibility(ctx, &items[i], false)
			if err != nil {
				writeErr(w, r, err)
				return
			}
			if c.Status == "eligible" {
				st.EstimatedBytes += c.Size
				st.EstimatedCount++
			}
		}
		st.NetBytes = st.FreedBytes - st.GeneratedBytes
		out = append(out, st)
		c := &total.SmartCounters
		c.GeneratedCount += st.GeneratedCount
		c.GeneratedBytes += st.GeneratedBytes
		c.DeletedCount += st.DeletedCount
		c.DeletedBytes += st.DeletedBytes
		c.FreedBytes += st.FreedBytes
		c.AnalysisMS += st.AnalysisMS
		c.RenderMS += st.RenderMS
		c.Failures += st.Failures
		total.EstimatedBytes += st.EstimatedBytes
		total.EstimatedCount += st.EstimatedCount
	}
	total.NetBytes = total.FreedBytes - total.GeneratedBytes
	writeJSON(w, 200, map[string]any{"queues": out, "total": total})
}
