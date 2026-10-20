package api

import (
	"errors"
	"net/http"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pathutil"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// A client syncing a lot at once (a first sync, or tens of GB) finishes its
// uploads with "X-VD-Finalize: defer" and moves on to the next file instead
// of waiting while the server verifies and stores each one. Once its sync
// is done it asks here how they ended, and uploads again whatever did not
// make it: a finish that failed, a session missing chunks, or a file the
// server does not have although the client sent it.
func (s *Server) handleUploadResults(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req wire.UploadResultsRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Uploads) > wire.UploadResultsMax {
		writeErr(w, r, errf(400, "too_many", "At most %d uploads per request", wire.UploadResultsMax))
		return
	}
	out := make([]wire.UploadOutcome, 0, len(req.Uploads))
	for _, c := range req.Uploads {
		out = append(out, s.uploadOutcome(r, p, c))
	}
	writeJSON(w, http.StatusOK, wire.UploadResultsResponse{Results: out})
}

func (s *Server) uploadOutcome(r *http.Request, p *Principal, c wire.UploadCheck) wire.UploadOutcome {
	ctx := r.Context()
	o := wire.UploadOutcome{ID: c.ID}
	if job := finalizeJobFor(c.ID); job != nil {
		select {
		case <-job.done:
			if job.err != nil {
				o.Status, o.Error = wire.UploadFailed, errMessage(job.err)
			} else {
				o.Status = wire.UploadDone
				if job.res != nil {
					o.Result = job.res.Result
				}
			}
		default:
			o.Status = wire.UploadPending
		}
		return o
	}
	if up, err := s.st.Upload(ctx, p.User.ID, c.ID); err == nil {
		switch up.Status {
		case "finalizing":
			o.Status = wire.UploadPending
			return o
		case "open":
			if !up.Complete() || !s.uploadDataIntact(up) {
				o.Status = wire.UploadIncomplete
				return o
			}
			// All data is here but the finish did not run (a restart, or a
			// failure worth retrying): queue it again.
			unlock, err := uploadLocks.acquire(ctx, up.ID, true)
			if err != nil {
				o.Status = wire.UploadPending
				return o
			}
			defer unlock()
			if cur, err := s.st.Upload(ctx, p.User.ID, up.ID); err == nil && cur.Status == "open" && finalizeJobFor(up.ID) == nil {
				if _, err := s.startFinalize(r, p, cur); err != nil {
					o.Status, o.Error = wire.UploadIncomplete, errMessage(err)
					return o
				}
			}
			o.Status = wire.UploadPending
			return o
		}
	}
	if t, err := s.st.TransferForUpload(ctx, p.User.ID, c.ID); err == nil {
		switch {
		case t.Status == "failed":
			o.Status, o.Error = wire.UploadFailed, t.Error
			return o
		case t.FinishedAt != nil:
			o.Status, o.Result = wire.UploadDone, t.Status
			return o
		}
	}
	// No session or record left: compare with the stored file.
	if segs, err := pathutil.Split(c.Path); err == nil && len(segs) > 0 {
		if n, err := s.st.Resolve(ctx, p.User, segs); err == nil && !n.IsDir && n.Size == c.Size && (c.SHA256 == "" || n.SHA256 == c.SHA256) {
			o.Status = wire.UploadDone
			return o
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			o.Status = wire.UploadPending // cannot tell now; ask again later
			return o
		}
	}
	o.Status, o.Error = wire.UploadMissing, "the server has no copy of this file"
	return o
}

func init() { implementFeature(wire.FeatureDeferredFinish) }
