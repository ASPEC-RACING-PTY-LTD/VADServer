package schedule

import "time"

// maxScanDays bounds the day-by-day search for wall-clock schedules. It
// covers the longest gap between 29 February dates (2096 to 2104).
const maxScanDays = 366*8 + 2

// Next returns the first run time strictly after after, without jitter, in
// loc (nil means time.Local). Wall-clock schedules follow the DST rules in
// the package documentation. It returns the zero time for manual or invalid
// schedules and for a cron expression with no occurrence within eight years.
func (s Schedule) Next(after time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.Local
	}
	switch s.Type {
	case TypeInterval:
		if s.EveryMinutes <= 0 {
			return time.Time{}
		}
		step := int64(s.EveryMinutes) * 60
		sec := after.Unix()
		k := sec / step
		if sec < 0 && sec%step != 0 {
			k--
		}
		return time.Unix((k+1)*step, 0).In(loc)
	case TypeDaily, TypeWeekly:
		h, m, err := parseAt(s.At)
		if err != nil {
			return time.Time{}
		}
		mask := uint8(0x7f)
		if s.Type == TypeWeekly {
			if mask, err = weekdayMask(s.Weekdays); err != nil {
				return time.Time{}
			}
		}
		return scanDays(after, loc, func(y int, mo time.Month, d int, wd time.Weekday, try func(h, m int) bool) bool {
			if mask&(1<<uint(wd)) == 0 {
				return false
			}
			return try(h, m)
		})
	case TypeCron:
		c, err := parseCron(s.Expr)
		if err != nil {
			return time.Time{}
		}
		return scanDays(after, loc, func(y int, mo time.Month, d int, wd time.Weekday, try func(h, m int) bool) bool {
			if !c.matchDay(y, mo, d, wd) {
				return false
			}
			for h := 0; h < 24; h++ {
				if c.hour&(1<<uint(h)) == 0 {
					continue
				}
				for m := 0; m < 60; m++ {
					if c.minute&(1<<uint(m)) != 0 && try(h, m) {
						return true
					}
				}
			}
			return false
		})
	}
	return time.Time{}
}

// scanDays walks local calendar days from the day before after. For each
// day, visit calls try with that day's wall times in ascending order; try
// reports true (and stops the scan) for the first resolved instant strictly
// after after. Resolved instants never decrease as wall time increases, so
// the first hit is the earliest.
func scanDays(after time.Time, loc *time.Location,
	visit func(y int, mo time.Month, d int, wd time.Weekday, try func(h, m int) bool) bool) time.Time {
	lt := after.In(loc)
	base := time.Date(lt.Year(), lt.Month(), lt.Day()-1, 12, 0, 0, 0, time.UTC)
	var found time.Time
	for i := 0; i < maxScanDays; i++ {
		day := base.AddDate(0, 0, i)
		y, mo, d := day.Date()
		try := func(h, m int) bool {
			t := resolveWall(y, mo, d, h, m, loc)
			if t.After(after) {
				found = t
				return true
			}
			return false
		}
		if visit(y, mo, d, day.Weekday(), try) {
			return found
		}
	}
	return time.Time{}
}

// resolveWall maps a local wall time to an instant: the first occurrence of
// an ambiguous time, or the end of the gap for a time skipped by DST.
func resolveWall(y int, mo time.Month, d, h, m int, loc *time.Location) time.Time {
	wall := time.Date(y, mo, d, h, m, 0, 0, time.UTC)
	var best time.Time
	// Zone offsets are within ±14h, so probes 36h either side see the
	// offsets in force before and after any transition near this wall time.
	for _, probe := range []time.Duration{-36 * time.Hour, 0, 36 * time.Hour} {
		_, off := wall.Add(probe).In(loc).Zone()
		c := wall.Add(-time.Duration(off) * time.Second)
		lc := c.In(loc)
		if lc.Year() == y && lc.Month() == mo && lc.Day() == d && lc.Hour() == h && lc.Minute() == m {
			if best.IsZero() || c.Before(best) {
				best = c
			}
		}
	}
	if !best.IsZero() {
		return best.In(loc)
	}
	// Skipped: using the pre-gap offset lands inside the new zone period,
	// whose start is the end of the gap.
	_, off := wall.Add(-36 * time.Hour).In(loc).Zone()
	c := wall.Add(-time.Duration(off) * time.Second).In(loc)
	if start, _ := c.ZoneBounds(); !start.IsZero() && !start.After(c) {
		return start.In(loc)
	}
	return c
}
