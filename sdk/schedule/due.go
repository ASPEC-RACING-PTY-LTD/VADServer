package schedule

import (
	"encoding/binary"
	"hash/fnv"
	"time"
)

// DefaultGrace is how late a run may start and still count as on time.
const DefaultGrace = 5 * time.Minute

// Options are the per-device inputs of NextRun and Due.
type Options struct {
	// Loc is the zone for wall-clock schedules; nil means time.Local. Use
	// ResolveLocation(s.Timezone, deviceLoc) to honour Schedule.Timezone.
	Loc *time.Location
	// JitterSeed selects this device's offset within jitter_minutes; see
	// JitterSeed. The same seed always gives the same offset.
	JitterSeed uint64
	// Grace is how late a slot may be and still run on time; 0 means
	// DefaultGrace.
	Grace time.Duration
}

// JitterSeed derives a deterministic jitter seed from a stable device
// identifier and a job id, so each device (and each job on it) keeps the
// same offset across restarts while devices are spread apart.
func JitterSeed(device string, jobID int64) uint64 {
	h := fnv.New64a()
	h.Write([]byte(device))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(jobID))
	h.Write(b[:])
	x := h.Sum64()
	// splitmix64 finaliser to spread nearby inputs.
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// Jitter returns the delay added to every slot of s for seed: a whole number
// of seconds in [0, jitter_minutes).
func (s Schedule) Jitter(seed uint64) time.Duration {
	if s.JitterMinutes <= 0 {
		return 0
	}
	return time.Duration(seed%uint64(s.JitterMinutes*60)) * time.Second
}

// NextRun returns the first jittered run time strictly after after, or the
// zero time if there is none (see Next).
func (s Schedule) NextRun(after time.Time, opt Options) time.Time {
	j := s.Jitter(opt.JitterSeed)
	t := s.Next(after.Add(-j), opt.Loc)
	if t.IsZero() {
		return t
	}
	return t.Add(j)
}

// Decision is the result of Due.
type Decision struct {
	// Due reports that the job should start now.
	Due bool
	// Missed reports that at least one slot passed more than the grace
	// period ago without a run. With Due it is a catch-up run; without Due
	// the missed slots were skipped (catch_up "skip").
	Missed bool
	// Next is the first run time strictly after now (zero for manual
	// schedules).
	Next time.Time
}

// Due decides whether a job should start at now. last is the start of the
// job's last run; for a job that never ran, pass its creation time. A zero
// last counts as overdue.
//
// Missed-run policy: any number of missed slots produce at most one catch-up
// run (catch_up "run_once", the default), or none (catch_up "skip"). A slot
// that is less than the grace period old is on time and always runs. Record
// the run's start time as the next call's last, so a slot never runs twice.
func (s Schedule) Due(last, now time.Time, opt Options) Decision {
	if s.Type == TypeManual {
		return Decision{}
	}
	grace := opt.Grace
	if grace <= 0 {
		grace = DefaultGrace
	}
	next := s.NextRun(now, opt)
	var first time.Time
	if !last.IsZero() {
		first = s.NextRun(last, opt)
		if first.IsZero() || first.After(now) {
			return Decision{Next: next}
		}
	}
	// A slot in (now-grace, now] is on time.
	if recent := s.NextRun(now.Add(-grace), opt); !recent.IsZero() && !recent.After(now) {
		// Earlier slots between last and recent were missed but this run
		// covers them.
		return Decision{Due: true, Missed: !last.IsZero() && first.Before(recent), Next: next}
	}
	if s.CatchUp == CatchUpSkip {
		return Decision{Missed: true, Next: next}
	}
	return Decision{Due: true, Missed: true, Next: next}
}
