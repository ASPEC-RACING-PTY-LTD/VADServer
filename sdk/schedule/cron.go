package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a parsed five-field cron expression:
// minute hour day-of-month month day-of-week.
//
// Each field accepts "*", a number, a range "a-b", a step "*/n" or "a-b/n",
// and comma-separated lists of these. Months accept jan..dec and weekdays
// accept sun..sat; weekday 0 and 7 are both Sunday. As in classic cron, when
// both day-of-month and day-of-week are restricted (not "*"), a day matches
// if either matches.
type cronSpec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
}

var (
	monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

func parseCron(expr string) (cronSpec, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return cronSpec{}, fmt.Errorf("schedule: cron %q: want 5 fields, got %d", expr, len(f))
	}
	var c cronSpec
	var err error
	if c.minute, err = parseCronField(f[0], 0, 59, nil); err != nil {
		return cronSpec{}, fmt.Errorf("schedule: cron %q minute: %w", expr, err)
	}
	if c.hour, err = parseCronField(f[1], 0, 23, nil); err != nil {
		return cronSpec{}, fmt.Errorf("schedule: cron %q hour: %w", expr, err)
	}
	if c.dom, err = parseCronField(f[2], 1, 31, nil); err != nil {
		return cronSpec{}, fmt.Errorf("schedule: cron %q day-of-month: %w", expr, err)
	}
	if c.month, err = parseCronField(f[3], 1, 12, monthNames); err != nil {
		return cronSpec{}, fmt.Errorf("schedule: cron %q month: %w", expr, err)
	}
	if c.dow, err = parseCronField(f[4], 0, 7, dowNames); err != nil {
		return cronSpec{}, fmt.Errorf("schedule: cron %q day-of-week: %w", expr, err)
	}
	if c.dow&(1<<7) != 0 {
		c.dow = c.dow&^(1<<7) | 1
	}
	c.domStar = f[2] == "*" || strings.HasPrefix(f[2], "*/")
	c.dowStar = f[4] == "*" || strings.HasPrefix(f[4], "*/")
	return c, nil
}

func parseCronField(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(s, ",") {
		rng, stepStr, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("bad step %q", part)
			}
			step = n
		}
		a, b := lo, hi
		if rng != "*" {
			from, to, isRange := strings.Cut(rng, "-")
			var err error
			if a, err = cronValue(from, lo, hi, names); err != nil {
				return 0, err
			}
			b = a
			if isRange {
				if b, err = cronValue(to, lo, hi, names); err != nil {
					return 0, err
				}
				if b < a {
					return 0, fmt.Errorf("bad range %q", part)
				}
			} else if hasStep {
				b = hi
			}
		}
		for v := a; v <= b; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, nil
}

func cronValue(s string, lo, hi int, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < lo || v > hi {
		return 0, fmt.Errorf("value %q out of range %d..%d", s, lo, hi)
	}
	return v, nil
}

func (c cronSpec) matchDay(y int, m time.Month, d int, wd time.Weekday) bool {
	if c.month&(1<<uint(m)) == 0 {
		return false
	}
	domOK := c.dom&(1<<uint(d)) != 0
	dowOK := c.dow&(1<<uint(wd)) != 0
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}
