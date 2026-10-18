package store

import (
	"context"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// Summary returns the dashboard counters of GET /summary (api-v1 §3) for
// one user. Counters of features without data yet are simply zero.
// Readiness is left to the caller (it comes from the readiness feature).
func (s *Store) Summary(ctx context.Context, userID int64) (*wire.Summary, error) {
	t := now()
	out := &wire.Summary{}
	queries := []struct {
		dst  *int64
		q    string
		args []any
	}{
		{&out.PendingDevices, "SELECT COUNT(*) FROM devices WHERE user_id = ? AND status = 'pending' AND expires_at > ?", []any{userID, t}},
		{&out.PendingApprovals, "SELECT COUNT(*) FROM approvals WHERE user_id = ? AND status = 'pending' AND (expires_at IS NULL OR expires_at > ?)", []any{userID, t}},
		{&out.RunningJobs, "SELECT COUNT(DISTINCT job_id) FROM job_runs WHERE user_id = ? AND finished_at IS NULL AND status IN (?,?,?,?)",
			[]any{userID, wire.RunStatusQueued, wire.RunStatusScanning, wire.RunStatusUploading, wire.RunStatusCommitting}},
		{&out.FailedRuns24h, "SELECT COUNT(*) FROM job_runs WHERE user_id = ? AND status = ? AND COALESCE(finished_at, updated_at) > ?",
			[]any{userID, wire.RunStatusFailed, t - 24*60*60*1000}},
		{&out.UnreadNotifications, "SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL", []any{userID}},
		{&out.OpenConflicts, "SELECT COUNT(*) FROM sync_conflicts WHERE user_id = ? AND state = ?", []any{userID, wire.ConflictStateOpen}},
		{&out.BlacklistedProfiles, "SELECT COUNT(*) FROM device_profiles p JOIN devices d ON d.id = p.device_id WHERE d.user_id = ? AND p.state = ?",
			[]any{userID, wire.ProfileStateBlacklisted}},
	}
	for _, q := range queries {
		if err := s.DB.QueryRowContext(ctx, q.q, q.args...).Scan(q.dst); err != nil {
			return nil, err
		}
	}
	return out, nil
}
