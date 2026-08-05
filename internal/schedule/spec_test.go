package schedule

import (
	"testing"
	"time"
)

// at builds a local time, the same location NextRun works in.
func at(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.Local)
}

func TestNextRun(t *testing.T) {
	tests := []struct {
		name  string
		spec  Spec
		after time.Time
		want  time.Time
	}{
		{
			name:  "daily later today",
			spec:  Spec{Freq: FreqDaily, Hour: 2, Minute: 30},
			after: at(2026, time.August, 4, 1, 0),
			want:  at(2026, time.August, 4, 2, 30),
		},
		{
			name:  "daily rolls to tomorrow once passed",
			spec:  Spec{Freq: FreqDaily, Hour: 2, Minute: 30},
			after: at(2026, time.August, 4, 2, 30),
			want:  at(2026, time.August, 5, 2, 30),
		},
		{
			name:  "hourly steps from local midnight",
			spec:  Spec{Freq: FreqHourly, Interval: 6, Minute: 15},
			after: at(2026, time.August, 4, 7, 0),
			want:  at(2026, time.August, 4, 12, 15),
		},
		{
			name:  "hourly wraps to midnight after the last step",
			spec:  Spec{Freq: FreqHourly, Interval: 6, Minute: 15},
			after: at(2026, time.August, 4, 18, 30),
			want:  at(2026, time.August, 5, 0, 15),
		},
		{
			name:  "hourly step that does not divide 24 has a short final gap",
			spec:  Spec{Freq: FreqHourly, Interval: 5, Minute: 0},
			after: at(2026, time.August, 4, 20, 30),
			want:  at(2026, time.August, 5, 0, 0),
		},
		{
			name:  "weekly picks the next listed weekday",
			spec:  Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Monday)}, Hour: 9},
			after: at(2026, time.August, 4, 10, 0), // a Tuesday
			want:  at(2026, time.August, 10, 9, 0), // the following Monday
		},
		{
			name:  "weekly with several days takes the soonest",
			spec:  Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Monday), int(time.Wednesday)}, Hour: 9},
			after: at(2026, time.August, 4, 10, 0), // Tuesday
			want:  at(2026, time.August, 5, 9, 0),  // Wednesday
		},
		{
			name:  "weekly same day but earlier hour still fires today",
			spec:  Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Tuesday)}, Hour: 23},
			after: at(2026, time.August, 4, 10, 0),
			want:  at(2026, time.August, 4, 23, 0),
		},
		{
			name:  "monthly this month",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 15, Hour: 3},
			after: at(2026, time.August, 4, 10, 0),
			want:  at(2026, time.August, 15, 3, 0),
		},
		{
			name:  "monthly rolls to next month once passed",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 1, Hour: 3},
			after: at(2026, time.August, 4, 10, 0),
			want:  at(2026, time.September, 1, 3, 0),
		},
		{
			name:  "day 31 clamps to the end of a 30-day month",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 31, Hour: 3},
			after: at(2026, time.September, 1, 0, 0),
			want:  at(2026, time.September, 30, 3, 0),
		},
		{
			name:  "day 31 clamps to the end of February",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 31, Hour: 3},
			after: at(2026, time.February, 1, 0, 0),
			want:  at(2026, time.February, 28, 3, 0),
		},
		{
			name:  "day 30 clamps to a leap February's 29th",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 30, Hour: 3},
			after: at(2028, time.February, 1, 0, 0),
			want:  at(2028, time.February, 29, 3, 0),
		},
		{
			name:  "last day of a 31-day month",
			spec:  Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 3},
			after: at(2026, time.August, 1, 0, 0),
			want:  at(2026, time.August, 31, 3, 0),
		},
		{
			name:  "last day of a leap February",
			spec:  Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 3},
			after: at(2028, time.February, 10, 0, 0),
			want:  at(2028, time.February, 29, 3, 0),
		},
		{
			name:  "monthly crosses a year boundary",
			spec:  Spec{Freq: FreqMonthly, MonthDay: 5, Hour: 3},
			after: at(2026, time.December, 20, 0, 0),
			want:  at(2027, time.January, 5, 3, 0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextRun(tt.spec, tt.after)
			if err != nil {
				t.Fatalf("NextRun: %v", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("NextRun(%v) = %v, want %v", tt.after, got, tt.want)
			}
		})
	}
}

// TestNextRun_IsStrictlyAfter pins the boundary the runner depends on: a due
// time already fired must not be returned again, or a schedule would re-fire in
// a loop for the whole minute.
func TestNextRun_IsStrictlyAfter(t *testing.T) {
	spec := Spec{Freq: FreqDaily, Hour: 2, Minute: 30}
	exact := at(2026, time.August, 4, 2, 30)
	got, err := NextRun(spec, exact)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !got.After(exact) {
		t.Errorf("NextRun must be strictly after its argument: got %v for %v", got, exact)
	}
}

// TestNextRun_Idempotent guards against drift: feeding a result back in must
// yield the following slot, never the same one.
func TestNextRun_Idempotent(t *testing.T) {
	spec := Spec{Freq: FreqHourly, Interval: 8, Minute: 0}
	cur := at(2026, time.August, 4, 3, 0)
	seen := map[time.Time]bool{}
	for range 10 {
		next, err := NextRun(spec, cur)
		if err != nil {
			t.Fatalf("NextRun: %v", err)
		}
		if seen[next] {
			t.Fatalf("NextRun repeated %v", next)
		}
		seen[next] = true
		if !next.After(cur) {
			t.Fatalf("NextRun went backwards: %v after %v", next, cur)
		}
		cur = next
	}
}

func TestSpecValidate(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		ok   bool
	}{
		{"valid daily", Spec{Freq: FreqDaily, Hour: 2, Minute: 30}, true},
		{"valid hourly", Spec{Freq: FreqHourly, Interval: 6}, true},
		{"valid weekly", Spec{Freq: FreqWeekly, Weekdays: []int{0}, Hour: 9}, true},
		{"valid monthly last day", Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 9}, true},
		{"unknown freq", Spec{Freq: "yearly", Hour: 1}, false},
		{"empty freq", Spec{Hour: 1}, false},
		{"hour too high", Spec{Freq: FreqDaily, Hour: 24}, false},
		{"hour negative", Spec{Freq: FreqDaily, Hour: -1}, false},
		{"minute too high", Spec{Freq: FreqDaily, Minute: 60}, false},
		{"hourly interval zero", Spec{Freq: FreqHourly, Interval: 0}, false},
		{"hourly interval too high", Spec{Freq: FreqHourly, Interval: 25}, false},
		{"weekly with no days", Spec{Freq: FreqWeekly, Hour: 9}, false},
		{"weekday out of range", Spec{Freq: FreqWeekly, Weekdays: []int{7}, Hour: 9}, false},
		{"month day zero", Spec{Freq: FreqMonthly, MonthDay: 0, Hour: 9}, false},
		{"month day too high", Spec{Freq: FreqMonthly, MonthDay: 32, Hour: 9}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if tt.ok && err != nil {
				t.Errorf("expected valid, got %v", err)
			}
			if !tt.ok && err == nil {
				t.Errorf("expected an error, got none")
			}
		})
	}
}

// TestNextRun_RejectsAnInvalidSpec keeps a bad stored spec from silently
// producing a fire time.
func TestNextRun_RejectsAnInvalidSpec(t *testing.T) {
	if _, err := NextRun(Spec{Freq: "nope"}, time.Now()); err == nil {
		t.Errorf("an invalid spec must not yield a next run")
	}
}

// FuzzNextRun asserts the two invariants the runner relies on for ANY spec that
// validates: the result is strictly in the future, and it is stable when fed
// back in. A crash-only target would be worthless here — a wrong time is the
// bug that matters, not a panic.
func FuzzNextRun(f *testing.F) {
	f.Add(0, 6, 2, 30, 15, 1)
	f.Add(3, 1, 23, 59, 31, 64)
	f.Fuzz(func(t *testing.T, freqSel, interval, hour, minute, monthDay, weekdayBits int) {
		freqs := []Freq{FreqHourly, FreqDaily, FreqWeekly, FreqMonthly}
		if freqSel < 0 {
			freqSel = -freqSel
		}
		if weekdayBits < 0 {
			weekdayBits = -weekdayBits
		}
		var wd []int
		for d := range 7 {
			if weekdayBits&(1<<d) != 0 {
				wd = append(wd, d)
			}
		}
		s := Spec{
			Freq:     freqs[freqSel%len(freqs)],
			Interval: interval,
			Hour:     hour,
			Minute:   minute,
			MonthDay: monthDay,
			Weekdays: wd,
		}
		if s.Validate() != nil {
			t.Skip("spec does not validate")
		}
		base := at(2026, time.August, 4, 12, 0)
		first, err := NextRun(s, base)
		if err != nil {
			t.Fatalf("a validated spec must have a next run: %v", err)
		}
		if !first.After(base) {
			t.Errorf("next run %v is not after %v", first, base)
		}
		second, err := NextRun(s, first)
		if err != nil {
			t.Fatalf("second NextRun: %v", err)
		}
		if !second.After(first) {
			t.Errorf("next run did not advance: %v then %v", first, second)
		}
	})
}
