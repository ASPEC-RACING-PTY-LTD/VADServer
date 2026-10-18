// Owner: SRV-8 / INT (P1-SW). Cross-task wiring: points the seams other
// feature areas left (profiles.go createProfileConsentTask) at the P1-17
// device-task store, and exports the approval and lease sweeps for the app
// maintenance loop.

package api

import (
	"context"
	"log/slog"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func init() {
	// Profile consent requests (api-v1 §6B) go through CreateTask so they are
	// validated, module-gated on "profiles" and wake the device's long-polls.
	createProfileConsentTask = func(ctx context.Context, s *Server, deviceID, createdBy int64, payload wire.TaskProfileConsentRequestPayload) (int64, error) {
		d, err := s.st.Device(ctx, deviceID)
		if err != nil {
			return 0, err
		}
		by := createdBy
		res, err := s.CreateTask(ctx, TaskRequest{UserID: d.UserID, DeviceID: d.ID, Kind: wire.TaskKindProfileConsentRequest,
			Payload: payload, RequiredModules: []string{wire.ModuleProfiles}, CreatedBy: &by})
		if err != nil {
			return 0, err
		}
		return res.TaskID, nil
	}
}

// ExpireApprovals expires overdue approvals and runs their Rejected hooks.
// The app maintenance loop calls it; reads of approvals also sweep.
func (s *Server) ExpireApprovals(ctx context.Context) {
	s.expireApprovals(ctx)
}

// ExpireTaskLeases returns device tasks whose lease ended to pending (or
// fails them after the last attempt, running their result hooks). The app
// maintenance loop calls it; device polls also sweep.
func (s *Server) ExpireTaskLeases(ctx context.Context) {
	requeued, failed, err := s.st.ExpireTaskLeases(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("task lease expiry failed", "err", err)
		}
		return
	}
	woken := map[int64]bool{}
	for _, id := range requeued {
		if t, err := s.st.Task(ctx, id); err == nil && !woken[t.DeviceID] {
			woken[t.DeviceID] = true
			s.taskHub().notify(t.DeviceID)
		}
	}
	for _, id := range failed {
		if t, err := s.st.Task(ctx, id); err == nil {
			s.runTaskHooks(ctx, t)
		}
	}
}
