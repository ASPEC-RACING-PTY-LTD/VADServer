package schedule

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
	_ "time/tzdata" // IANA zones on hosts without system tzdata (Windows)

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("tzdata for %s unavailable: %v", name, err)
	}
	return loc
}

func ts(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNext(t *testing.T) {
	daily := func(at string) Schedule { return Schedule{Type: TypeDaily, At: at} }
	weekly := func(at string, d ...int) Schedule { return Schedule{Type: TypeWeekly, At: at, Weekdays: d} }
	cron := func(e string) Schedule { return Schedule{Type: TypeCron, Expr: e} }
	every := func(m int) Schedule { return Schedule{Type: TypeInterval, EveryMinutes: m} }

	tests := []struct {
		name  string
		s     Schedule
		loc   string
		after string
		want  string // RFC3339; "" means zero
	}{
		// Australia/Sydney: DST starts 2026-10-04 02:00 AEST -> 03:00 AEDT,
		// ends 2026-04-05 03:00 AEDT -> 02:00 AEST.
		{"syd skipped 02:30 runs at gap end", daily("02:30"), "Australia/Sydney", "2026-10-03T12:00:00+10:00", "2026-10-04T03:00:00+11:00"},
		{"syd day after gap", daily("02:30"), "Australia/Sydney", "2026-10-04T03:00:00+11:00", "2026-10-05T02:30:00+11:00"},
		{"syd 03:00 on gap day", daily("03:00"), "Australia/Sydney", "2026-10-03T12:00:00+10:00", "2026-10-04T03:00:00+11:00"},
		{"syd 01:00 before gap", daily("01:00"), "Australia/Sydney", "2026-10-03T01:00:00+10:00", "2026-10-04T01:00:00+10:00"},
		{"syd ambiguous 02:30 first occurrence", daily("02:30"), "Australia/Sydney", "2026-04-04T12:00:00+11:00", "2026-04-05T02:30:00+11:00"},
		{"syd ambiguous second occurrence skipped", daily("02:30"), "Australia/Sydney", "2026-04-05T02:30:00+11:00", "2026-04-06T02:30:00+10:00"},
		{"syd hourly cron over gap", cron("0 * * * *"), "Australia/Sydney", "2026-10-04T01:30:00+10:00", "2026-10-04T03:00:00+11:00"},
		{"syd hourly cron after gap", cron("0 * * * *"), "Australia/Sydney", "2026-10-04T03:00:00+11:00", "2026-10-04T04:00:00+11:00"},
		{"syd interval ignores DST", every(60), "Australia/Sydney", "2026-10-03T15:30:00Z", "2026-10-03T16:00:00Z"},
		{"syd weekly sunday on gap day", weekly("02:00", 7), "Australia/Sydney", "2026-10-01T00:00:00+10:00", "2026-10-04T03:00:00+11:00"},

		// America/New_York: DST starts 2026-03-08 02:00 EST -> 03:00 EDT,
		// ends 2026-11-01 02:00 EDT -> 01:00 EST.
		{"ny skipped 02:15", daily("02:15"), "America/New_York", "2026-03-07T12:00:00-05:00", "2026-03-08T03:00:00-04:00"},
		{"ny ambiguous 01:30 first", daily("01:30"), "America/New_York", "2026-10-31T12:00:00-04:00", "2026-11-01T01:30:00-04:00"},
		{"ny ambiguous 01:30 next day", daily("01:30"), "America/New_York", "2026-11-01T01:30:00-04:00", "2026-11-02T01:30:00-05:00"},
		{"ny cron first 01:00", cron("*/30 1-2 * * *"), "America/New_York", "2026-11-01T00:00:00-04:00", "2026-11-01T01:00:00-04:00"},
		{"ny cron 01:30 EDT", cron("*/30 1-2 * * *"), "America/New_York", "2026-11-01T01:00:00-04:00", "2026-11-01T01:30:00-04:00"},
		{"ny cron skips repeated hour", cron("*/30 1-2 * * *"), "America/New_York", "2026-11-01T01:30:00-04:00", "2026-11-01T02:00:00-05:00"},
		{"ny cron gap collapses", cron("*/15 2 * * *"), "America/New_York", "2026-03-08T01:59:00-05:00", "2026-03-08T03:00:00-04:00"},
		{"ny cron gap runs once", cron("*/15 2 * * *"), "America/New_York", "2026-03-08T03:00:00-04:00", "2026-03-09T02:00:00-04:00"},

		// Leap days.
		{"leap cron feb 29", cron("0 0 29 2 *"), "UTC", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"leap daily onto feb 29", daily("23:59"), "UTC", "2028-02-28T23:59:00Z", "2028-02-29T23:59:00Z"},
		{"leap daily off feb 29", daily("23:59"), "UTC", "2028-02-29T23:59:00Z", "2028-03-01T23:59:00Z"},
		{"leap 2100 is not leap", cron("0 12 29 2 *"), "UTC", "2096-03-01T00:00:00Z", "2104-02-29T12:00:00Z"},
		{"cron feb 30 never", cron("0 0 30 2 *"), "UTC", "2026-01-01T00:00:00Z", ""},

		// Week boundaries (ISO weekdays: 1 = Monday, 7 = Sunday).
		{"weekly sat to sun", weekly("10:00", 7), "UTC", "2026-10-17T12:00:00Z", "2026-10-18T10:00:00Z"},
		{"weekly sun to mon midnight", weekly("00:00", 1), "UTC", "2026-10-18T23:59:00Z", "2026-10-19T00:00:00Z"},
		{"weekly fri to mon", weekly("09:00", 5, 1), "UTC", "2026-10-23T09:00:00Z", "2026-10-26T09:00:00Z"},
		{"weekly across year end", weekly("08:00", 4), "UTC", "2026-12-31T08:00:00Z", "2027-01-07T08:00:00Z"},
		{"weekly same day later", weekly("18:00", 7), "UTC", "2026-10-18T10:00:00Z", "2026-10-18T18:00:00Z"},
		{"cron dow 7 is sunday", cron("0 0 * * 7"), "UTC", "2026-10-14T00:00:00Z", "2026-10-18T00:00:00Z"},
		{"cron dom or dow", cron("0 0 13 * fri"), "UTC", "2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z"},
		{"cron dom only", cron("0 0 13 * *"), "UTC", "2026-10-01T00:00:00Z", "2026-10-13T00:00:00Z"},

		// Interval grid and manual.
		{"interval aligns to grid", every(60), "UTC", "2026-10-18T10:15:00Z", "2026-10-18T11:00:00Z"},
		{"interval strictly after", every(60), "UTC", "2026-10-18T11:00:00Z", "2026-10-18T12:00:00Z"},
		{"manual never", Schedule{Type: TypeManual}, "UTC", "2026-10-18T11:00:00Z", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc := mustLoc(t, tc.loc)
			got := tc.s.Next(ts(t, tc.after), loc)
			if tc.want == "" {
				if !got.IsZero() {
					t.Fatalf("got %v, want zero", got)
				}
				return
			}
			if want := ts(t, tc.want); !got.Equal(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			if got.Location() != loc {
				t.Fatalf("location %v, want %v", got.Location(), loc)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		s    Schedule
		ok   bool
	}{
		{"manual", Schedule{Type: TypeManual}, true},
		{"daily", Schedule{Type: TypeDaily, At: "02:00", Timezone: "Local", CatchUp: CatchUpRunOnce}, true},
		{"daily bad at", Schedule{Type: TypeDaily, At: "24:00"}, false},
		{"daily short at", Schedule{Type: TypeDaily, At: "2:00"}, false},
		{"weekly", Schedule{Type: TypeWeekly, At: "09:30", Weekdays: []int{1, 2, 3, 4, 5}}, true},
		{"weekly empty", Schedule{Type: TypeWeekly, At: "09:30"}, false},
		{"weekly zero day", Schedule{Type: TypeWeekly, At: "09:30", Weekdays: []int{0}}, false},
		{"weekly dup", Schedule{Type: TypeWeekly, At: "09:30", Weekdays: []int{1, 1}}, false},
		{"interval", Schedule{Type: TypeInterval, EveryMinutes: 15, JitterMinutes: 5}, true},
		{"interval too small", Schedule{Type: TypeInterval, EveryMinutes: 4}, false},
		{"interval jitter too big", Schedule{Type: TypeInterval, EveryMinutes: 15, JitterMinutes: 15}, false},
		{"cron", Schedule{Type: TypeCron, Expr: "*/5 1-3,22 * jan-mar mon-fri"}, true},
		{"cron fields", Schedule{Type: TypeCron, Expr: "* * * *"}, false},
		{"cron range", Schedule{Type: TypeCron, Expr: "61 * * * *"}, false},
		{"cron step", Schedule{Type: TypeCron, Expr: "*/0 * * * *"}, false},
		{"unknown type", Schedule{Type: "hourly"}, false},
		{"bad catch_up", Schedule{Type: TypeManual, CatchUp: "always"}, false},
		{"negative jitter", Schedule{Type: TypeDaily, At: "02:00", JitterMinutes: -1}, false},
		{"iana tz", Schedule{Type: TypeDaily, At: "02:00", Timezone: "Australia/Sydney"}, true},
		{"bad tz", Schedule{Type: TypeDaily, At: "02:00", Timezone: "Mars/Olympus"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.s.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestJSONMatchesWire(t *testing.T) {
	const in = `{"type":"weekly","expr":"","at":"02:00","weekdays":[1,2,3,4,5],"every_minutes":0,"timezone":"Local","catch_up":"run_once","jitter_minutes":10}`
	var s Schedule
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Fatalf("round trip\n got %s\nwant %s", out, in)
	}
	var w wire.JobSchedule
	if err := json.Unmarshal([]byte(in), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(Schedule(w), s) {
		t.Fatalf("wire %+v != schedule %+v", w, s)
	}
}

func TestJitter(t *testing.T) {
	s := Schedule{Type: TypeDaily, At: "02:00", JitterMinutes: 10}
	a, b := JitterSeed("device-a", 7), JitterSeed("device-a", 7)
	if a != b {
		t.Fatal("seed not deterministic")
	}
	if JitterSeed("device-b", 7) == a || JitterSeed("device-a", 8) == a {
		t.Fatal("seed ignores input")
	}
	for i := 0; i < 1000; i++ {
		j := s.Jitter(JitterSeed("dev", int64(i)))
		if j < 0 || j >= 10*time.Minute || j%time.Second != 0 {
			t.Fatalf("jitter %v out of range", j)
		}
	}
	if (Schedule{Type: TypeDaily, At: "02:00"}).Jitter(a) != 0 {
		t.Fatal("jitter without jitter_minutes")
	}

	opt := Options{Loc: time.UTC, JitterSeed: 125} // 125 s
	tests := []struct{ after, want string }{
		{"2026-10-18T01:00:00Z", "2026-10-18T02:02:05Z"},
		{"2026-10-18T02:01:00Z", "2026-10-18T02:02:05Z"},
		{"2026-10-18T02:02:05Z", "2026-10-19T02:02:05Z"},
	}
	for _, tc := range tests {
		if got := s.NextRun(ts(t, tc.after), opt); !got.Equal(ts(t, tc.want)) {
			t.Errorf("NextRun(%s) = %v, want %s", tc.after, got, tc.want)
		}
	}
}

func TestDue(t *testing.T) {
	daily := Schedule{Type: TypeDaily, At: "02:00"}
	skip := daily
	skip.CatchUp = CatchUpSkip
	opt := Options{Loc: time.UTC}
	var zero time.Time
	tests := []struct {
		name      string
		s         Schedule
		last, now string
		want      Decision
	}{
		{"not yet", daily, "2026-10-01T02:00:10Z", "2026-10-02T01:00:00Z", Decision{Next: ts(t, "2026-10-02T02:00:00Z")}},
		{"on time", daily, "2026-10-01T02:00:10Z", "2026-10-02T02:01:00Z", Decision{Due: true, Next: ts(t, "2026-10-03T02:00:00Z")}},
		{"on time skip policy", skip, "2026-10-01T02:00:10Z", "2026-10-02T02:04:59Z", Decision{Due: true, Next: ts(t, "2026-10-03T02:00:00Z")}},
		{"already ran", daily, "2026-10-02T02:00:30Z", "2026-10-02T02:01:00Z", Decision{Next: ts(t, "2026-10-03T02:00:00Z")}},
		{"missed many catch up once", daily, "2026-10-01T02:00:10Z", "2026-10-04T10:00:00Z", Decision{Due: true, Missed: true, Next: ts(t, "2026-10-05T02:00:00Z")}},
		{"missed many skip", skip, "2026-10-01T02:00:10Z", "2026-10-04T10:00:00Z", Decision{Missed: true, Next: ts(t, "2026-10-05T02:00:00Z")}},
		{"missed then on time", skip, "2026-10-01T02:00:10Z", "2026-10-03T02:02:00Z", Decision{Due: true, Missed: true, Next: ts(t, "2026-10-04T02:00:00Z")}},
		{"just past grace", daily, "2026-10-01T02:00:10Z", "2026-10-02T02:05:01Z", Decision{Due: true, Missed: true, Next: ts(t, "2026-10-03T02:00:00Z")}},
		{"manual", Schedule{Type: TypeManual}, "2026-10-01T02:00:10Z", "2026-10-04T10:00:00Z", Decision{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.s.Due(ts(t, tc.last), ts(t, tc.now), opt)
			if got.Due != tc.want.Due || got.Missed != tc.want.Missed || !got.Next.Equal(tc.want.Next) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	now := ts(t, "2026-10-04T10:00:00Z")
	if d := daily.Due(zero, now, opt); !d.Due || !d.Missed {
		t.Errorf("never run, run_once: %+v", d)
	}
	if d := skip.Due(zero, now, opt); d.Due || !d.Missed {
		t.Errorf("never run, skip: %+v", d)
	}
	// After a catch-up run recorded at now, nothing is due until the next slot.
	if d := daily.Due(now, now.Add(time.Hour), opt); d.Due {
		t.Errorf("after catch-up: %+v", d)
	}
}
