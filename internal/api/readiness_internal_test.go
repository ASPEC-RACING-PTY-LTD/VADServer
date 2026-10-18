package api

import (
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestJobFreshness(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	daily := wire.JobSchedule{Type: wire.JobScheduleDaily, At: "02:00", Timezone: "UTC"}
	hourly := wire.JobSchedule{Type: wire.JobScheduleInterval, EveryMinutes: 60}
	manual := wire.JobSchedule{Type: wire.JobScheduleManual}
	day := 24 * time.Hour
	for _, tc := range []struct {
		name    string
		sched   wire.JobSchedule
		created time.Duration
		last    time.Duration // 0 = never
		failed  bool
		want    string
	}{
		{"new job, no backup yet", daily, time.Hour, 0, false, wire.ReadinessStatusWarning},
		{"old job, never backed up", daily, 2 * day, 0, false, wire.ReadinessStatusCritical},
		{"daily, ran this morning", daily, 30 * day, 10 * time.Hour, false, wire.ReadinessStatusOK},
		{"daily, missed one run", daily, 30 * day, 30 * time.Hour, false, wire.ReadinessStatusOK},
		{"daily, missed two runs", daily, 30 * day, 60 * time.Hour, false, wire.ReadinessStatusWarning},
		{"daily, a week overdue", daily, 30 * day, 8 * day, false, wire.ReadinessStatusCritical},
		{"daily, last run failed", daily, 30 * day, 10 * time.Hour, true, wire.ReadinessStatusWarning},
		{"hourly, 3 hours ago", hourly, 30 * day, 3 * time.Hour, false, wire.ReadinessStatusWarning},
		{"manual, 10 days ago", manual, 60 * day, 10 * day, false, wire.ReadinessStatusOK},
		{"manual, 40 days ago", manual, 60 * day, 40 * day, false, wire.ReadinessStatusCritical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := &store.ReadinessJob{Job: store.BackupJob{Name: "j", Schedule: tc.sched, CreatedAt: ms(tc.created)}}
			if tc.last > 0 {
				j.LastSuccessAt = ms(tc.last)
			}
			if tc.failed {
				j.LastRunStatus, j.LastRunAt = wire.RunStatusFailed, ms(time.Minute)
			}
			got, msg := readinessEval{now: now}.jobFreshness(j)
			if got != tc.want || msg == "" {
				t.Fatalf("got %s (%q), want %s", got, msg, tc.want)
			}
		})
	}
}

func TestReadinessScore(t *testing.T) {
	c := func(id, st string) wire.ReadinessCheck { return wire.ReadinessCheck{ID: id, Status: st} }
	for _, tc := range []struct {
		name   string
		checks []wire.ReadinessCheck
		score  int
		status string
	}{
		{"empty", nil, 0, wire.ReadinessStatusUnknown},
		{"only unknown and informational", []wire.ReadinessCheck{c("a", wire.ReadinessStatusUnknown), c(wire.ReadinessCheckInstallMode, wire.ReadinessStatusOK)}, 0, wire.ReadinessStatusUnknown},
		{"all ok", []wire.ReadinessCheck{c("a", wire.ReadinessStatusOK), c("b", wire.ReadinessStatusOK)}, 100, wire.ReadinessStatusOK},
		{"ok and warning", []wire.ReadinessCheck{c("a", wire.ReadinessStatusOK), c("b", wire.ReadinessStatusWarning), c("c", wire.ReadinessStatusUnknown)}, 75, wire.ReadinessStatusWarning},
		{"critical wins", []wire.ReadinessCheck{c("a", wire.ReadinessStatusOK), c("b", wire.ReadinessStatusOK), c("c", wire.ReadinessStatusCritical)}, 67, wire.ReadinessStatusCritical},
	} {
		score, status := readinessScore([]wire.ReadinessDevice{{Checks: tc.checks}})
		if score != tc.score || status != tc.status {
			t.Fatalf("%s: %d %s", tc.name, score, status)
		}
	}
}
