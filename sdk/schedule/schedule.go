// Package schedule evaluates backup job schedules (api-v1 §6, rev 1.0 §6
// BackupJob.schedule). It is shared by the server scheduler and the desktop
// scheduler so both compute the same run times.
//
// Wall-clock rules (daily, weekly and cron schedules, evaluated in a local
// time zone):
//   - A wall time skipped by a DST gap (clocks jump forward) runs at the next
//     valid instant, which is the instant the gap ends. Example: 02:30 on the
//     Australia/Sydney spring-forward day runs at 03:00 AEDT.
//   - A wall time that occurs twice (clocks fall back) runs once, at its first
//     occurrence (the earlier, pre-transition offset).
//
// Interval schedules run on a fixed grid of every_minutes since the Unix
// epoch and are unaffected by DST.
package schedule

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Schedule is BackupJob.schedule. It has the same fields and JSON form as
// wire.JobSchedule; convert with Schedule(js) and wire.JobSchedule(s).
//
// Weekdays use ISO numbering: 1 = Monday … 7 = Sunday.
type Schedule wire.JobSchedule

// Schedule types (Schedule.Type).
const (
	TypeCron     = wire.JobScheduleCron
	TypeDaily    = wire.JobScheduleDaily
	TypeWeekly   = wire.JobScheduleWeekly
	TypeInterval = wire.JobScheduleInterval
	TypeManual   = wire.JobScheduleManual
)

// Missed-run policies (Schedule.CatchUp). Empty means CatchUpRunOnce.
const (
	CatchUpRunOnce = wire.JobCatchUpRunOnce
	CatchUpSkip    = wire.JobCatchUpSkip
)

// TimezoneLocal is the Timezone value meaning the device's local zone. An
// empty Timezone means the same.
const TimezoneLocal = "Local"

// Validation limits.
const (
	MinEveryMinutes  = 5
	MaxEveryMinutes  = 31 * 24 * 60
	MaxJitterMinutes = 24 * 60
)

// Validate checks the fields used by s.Type. Fields that the type does not
// use are ignored.
func (s Schedule) Validate() error {
	switch s.Type {
	case TypeManual:
	case TypeInterval:
		if s.EveryMinutes < MinEveryMinutes || s.EveryMinutes > MaxEveryMinutes {
			return fmt.Errorf("schedule: every_minutes %d out of range %d..%d", s.EveryMinutes, MinEveryMinutes, MaxEveryMinutes)
		}
		if s.JitterMinutes >= s.EveryMinutes {
			return fmt.Errorf("schedule: jitter_minutes %d must be less than every_minutes %d", s.JitterMinutes, s.EveryMinutes)
		}
	case TypeDaily:
		if _, _, err := parseAt(s.At); err != nil {
			return err
		}
	case TypeWeekly:
		if _, _, err := parseAt(s.At); err != nil {
			return err
		}
		if _, err := weekdayMask(s.Weekdays); err != nil {
			return err
		}
	case TypeCron:
		if _, err := parseCron(s.Expr); err != nil {
			return err
		}
	default:
		return fmt.Errorf("schedule: unknown type %q", s.Type)
	}
	switch s.CatchUp {
	case "", CatchUpRunOnce, CatchUpSkip:
	default:
		return fmt.Errorf("schedule: unknown catch_up %q", s.CatchUp)
	}
	if s.JitterMinutes < 0 || s.JitterMinutes > MaxJitterMinutes {
		return fmt.Errorf("schedule: jitter_minutes %d out of range 0..%d", s.JitterMinutes, MaxJitterMinutes)
	}
	if _, err := ResolveLocation(s.Timezone, time.UTC); err != nil {
		return err
	}
	return nil
}

// ResolveLocation returns the zone named by a Timezone value: local for ""
// or "Local", otherwise the IANA zone. Loading IANA zones needs tzdata; a
// Windows binary should import _ "time/tzdata".
func ResolveLocation(tz string, local *time.Location) (*time.Location, error) {
	if tz == "" || tz == TimezoneLocal {
		if local == nil {
			local = time.Local
		}
		return local, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("schedule: timezone %q: %w", tz, err)
	}
	return loc, nil
}

// parseAt parses "HH:MM" (24-hour).
func parseAt(at string) (hour, minute int, err error) {
	if len(at) != 5 || at[2] != ':' {
		return 0, 0, fmt.Errorf("schedule: at %q is not HH:MM", at)
	}
	h, err1 := strconv.Atoi(at[:2])
	m, err2 := strconv.Atoi(at[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("schedule: at %q is not HH:MM", at)
	}
	return h, m, nil
}

// weekdayMask converts ISO weekdays (1 = Monday … 7 = Sunday) to a bit mask
// indexed by time.Weekday.
func weekdayMask(days []int) (uint8, error) {
	if len(days) == 0 {
		return 0, fmt.Errorf("schedule: weekdays is empty")
	}
	var mask uint8
	for _, d := range days {
		if d < 1 || d > 7 {
			return 0, fmt.Errorf("schedule: weekday %d out of range 1..7", d)
		}
		bit := uint8(1) << (d % 7)
		if mask&bit != 0 {
			return 0, fmt.Errorf("schedule: duplicate weekday %d", d)
		}
		mask |= bit
	}
	return mask, nil
}
