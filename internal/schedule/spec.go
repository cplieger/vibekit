// Package schedule computes when a recurring workflow run is next due.
//
// The shape is a deliberate subset of RFC 5545's recurrence rule (the
// iCalendar vocabulary every calendar tool speaks) rather than a cron
// expression: it maps one-to-one onto the four choices the UI offers, needs no
// parser and no dependency, and leaves room for "the 2nd Tuesday" later — which
// cron cannot express at all.
//
// Everything here is LOCAL time, by decision. A schedule says "02:00" and the
// user means 02:00 where the container lives; there is no per-schedule timezone
// field and no UTC conversion. The UI shows the resolved next run so the
// meaning is never ambiguous.
package schedule

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Freq is how often a schedule recurs.
type Freq string

// The four recurrence frequencies the UI offers.
const (
	FreqHourly  Freq = "hourly"  // every Interval hours, at Minute past
	FreqDaily   Freq = "daily"   // every day at Hour:Minute
	FreqWeekly  Freq = "weekly"  // on each Weekday at Hour:Minute
	FreqMonthly Freq = "monthly" // on MonthDay at Hour:Minute
)

// LastDay in MonthDay means "the last day of the month", the explicit answer to
// months that have no 29th/30th/31st.
const LastDay = -1

// maxScanDays bounds the forward search. A monthly schedule needs at most one
// month plus a leap-year margin; this is generous and keeps the loop finite
// even for a spec that can never match.
const maxScanDays = 400

// ErrNoOccurrence means the spec matches no day within the scan window. A
// validated spec cannot produce this.
var ErrNoOccurrence = errors.New("schedule has no next occurrence")

// Spec is one recurrence rule. Only the fields its Freq uses are read, so a
// stored spec keeps the others at zero.
type Spec struct {
	Freq Freq `json:"freq"`
	// Weekdays are the days FreqWeekly fires on, as time.Weekday (0=Sunday).
	Weekdays []int `json:"weekdays,omitempty"`
	// Interval is the hour step for FreqHourly (1-24). Anchored to local
	// midnight rather than to when the schedule was saved, so the fire times
	// are stable across restarts and predictable on a clock. A step that does
	// not divide 24 therefore has a short final gap each day: every 5 hours is
	// 00,05,10,15,20 and then midnight.
	Interval int `json:"interval,omitempty"`
	// MonthDay is the day FreqMonthly fires on: 1-31, or LastDay. A value past
	// the end of a short month is CLAMPED to that month's last day rather than
	// skipping the month, so "day 31" still fires in February.
	MonthDay int `json:"month_day,omitempty"`
	// Hour and Minute are the local time of day. Hour is ignored by FreqHourly,
	// which uses Minute as the offset past each stepped hour.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
}

// Validate reports whether the spec is well-formed. Called on every write so a
// stored schedule can be trusted by the runner.
func (s Spec) Validate() error {
	if s.Minute < 0 || s.Minute > 59 {
		return fmt.Errorf("minute %d out of range 0-59", s.Minute)
	}
	switch s.Freq {
	case FreqHourly:
		if s.Interval < 1 || s.Interval > 24 {
			return fmt.Errorf("hourly interval %d out of range 1-24", s.Interval)
		}
		return nil
	case FreqDaily:
		return s.validateHour()
	case FreqWeekly:
		return s.validateWeekly()
	case FreqMonthly:
		return s.validateMonthly()
	default:
		return fmt.Errorf("unknown frequency %q", s.Freq)
	}
}

func (s Spec) validateWeekly() error {
	if len(s.Weekdays) == 0 {
		return errors.New("weekly schedule needs at least one weekday")
	}
	for _, d := range s.Weekdays {
		if d < 0 || d > 6 {
			return fmt.Errorf("weekday %d out of range 0-6", d)
		}
	}
	return s.validateHour()
}

func (s Spec) validateMonthly() error {
	if s.MonthDay != LastDay && (s.MonthDay < 1 || s.MonthDay > 31) {
		return fmt.Errorf("month day %d out of range 1-31 (or %d for last)", s.MonthDay, LastDay)
	}
	return s.validateHour()
}

func (s Spec) validateHour() error {
	if s.Hour < 0 || s.Hour > 23 {
		return fmt.Errorf("hour %d out of range 0-23", s.Hour)
	}
	return nil
}

// NextRun returns the first occurrence strictly after `after`, in after's own
// location.
//
// It works by scanning forward one day at a time and asking two questions per
// day — does this day match, and which times does it produce — so every piece
// of calendar arithmetic (month lengths, leap years, DST) is delegated to
// time.Date rather than reimplemented. A day-granular scan is far easier to
// verify than closed-form date math, and 400 iterations of integer comparison
// costs nothing at the once-a-minute rate the runner calls it.
func NextRun(s Spec, after time.Time) (time.Time, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, err
	}
	day := startOfDay(after)
	for i := range maxScanDays {
		d := day.AddDate(0, 0, i)
		if !s.matchesDay(d) {
			continue
		}
		for _, t := range s.timesOn(d) {
			if t.After(after) {
				return t, nil
			}
		}
	}
	return time.Time{}, ErrNoOccurrence
}

// startOfDay is local midnight on t's date.
func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// matchesDay reports whether the spec fires at all on the given day.
func (s Spec) matchesDay(day time.Time) bool {
	switch s.Freq {
	case FreqHourly, FreqDaily:
		return true
	case FreqWeekly:
		return slices.Contains(s.Weekdays, int(day.Weekday()))
	case FreqMonthly:
		return day.Day() == s.monthDayIn(day)
	default:
		return false
	}
}

// monthDayIn resolves MonthDay against the length of the given day's month:
// LastDay and any overshoot both become that month's final day.
func (s Spec) monthDayIn(day time.Time) int {
	last := daysInMonth(day)
	if s.MonthDay == LastDay || s.MonthDay > last {
		return last
	}
	return s.MonthDay
}

// daysInMonth uses time.Date's normalization: day 0 of the NEXT month is the
// last day of this one, which is correct for February in a leap year without
// knowing anything about leap years.
func daysInMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// timesOn returns the fire times on a day the spec matches, in order.
func (s Spec) timesOn(day time.Time) []time.Time {
	at := func(h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
	}
	if s.Freq != FreqHourly {
		return []time.Time{at(s.Hour, s.Minute)}
	}
	out := make([]time.Time, 0, 24/s.Interval+1)
	for h := 0; h < 24; h += s.Interval {
		out = append(out, at(h, s.Minute))
	}
	return out
}
