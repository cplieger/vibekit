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

// The five recurrence frequencies the UI offers.
const (
	FreqMinutely Freq = "minutely" // every Interval minutes, phased by Minute
	FreqHourly   Freq = "hourly"   // every Interval hours, at Minute past
	FreqDaily    Freq = "daily"    // every day at Hour:Minute
	FreqWeekly   Freq = "weekly"   // on each Weekday at Hour:Minute
	FreqMonthly  Freq = "monthly"  // on MonthDay at Hour:Minute
)

// LastDay in MonthDay means "the last day of the month", the explicit answer to
// months that have no 29th/30th/31st.
const LastDay = -1

// The bounds on FreqMinutely's Interval. Sub-hour steps only: an hour or more is
// FreqHourly's job, and 60 here would be a second spelling of "hourly, every 1".
//
// The FLOOR is the load-bearing half, and it is derived rather than chosen. Four
// mechanisms misbehave below it, and 5 is the smallest step none of them
// misbehaves at:
//
//   - the runner ticks once a minute (TickInterval), so an interval of 1 leaves
//     every sweep due and the schedule is really "as often as possible"
//   - one live run per recipe is refused as an overlap, so a run that outlives
//     its own interval writes "failed" into the row on every later slot, and the
//     row then reads failed forever while nothing is actually wrong
//   - MissGrace is 3 minutes, and a slot that may still fire is only
//     distinguishable from the NEXT slot while the interval exceeds the grace
//   - a scheduled run's own interval is one input to the run's single deadline
//     (the host's runCeiling / minRunBudget pair in internal/hub/run_bounds.go),
//     and blowing that deadline logs at ERROR for an alert rule, so a 1-minute
//     interval pages the operator for any run that takes 61 seconds. The host
//     FLOORS the derived budget at this same 5 minutes, so the two numbers move
//     together — change minRunBudget with this constant
//
// Mirrored in static-src/schedule-types.ts (INTERVAL_BOUNDS), which is where the
// user meets the rule; change both together.
const (
	minMinuteInterval = 5
	maxMinuteInterval = 59
)

// minutesPerDay bounds the minute walk in timesOn.
const minutesPerDay = 24 * 60

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
	// Interval is the recurrence step, in the unit its Freq names: hours for
	// FreqHourly (1-24), minutes for FreqMinutely (minMinuteInterval to
	// maxMinuteInterval). ONE field for both because a minute-level frequency is
	// the same rule one unit down, not a second scheme.
	//
	// Anchored to local midnight rather than to when the schedule was saved, so
	// the fire times are stable across restarts and predictable on a clock. A
	// step that does not divide its day therefore has a short final gap: every 5
	// hours is 00,05,10,15,20 and then midnight, and every 50 minutes is
	// 00:00,00:50,01:40 and so on, then midnight.
	Interval int `json:"interval,omitempty"`
	// MonthDay is the day FreqMonthly fires on: 1-31, or LastDay. A value past
	// the end of a short month is CLAMPED to that month's last day rather than
	// skipping the month, so "day 31" still fires in February.
	MonthDay int `json:"month_day,omitempty"`
	// Hour and Minute are the local time of day. Hour is ignored by FreqHourly,
	// which uses Minute as the offset past each stepped hour, and by
	// FreqMinutely, which takes Minute % Interval as the step's PHASE.
	//
	// That modulo is what makes the chosen minute mean something rather than
	// nothing: any Minute is a whole number of steps plus that remainder, so the
	// minute the user picked is always itself a fire time. "At :07, every 15"
	// fires at 07, 22, 37, 52 instead of being a rule that never fires at :07.
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
	case FreqMinutely:
		// The one gate the runner trusts: Put validates every write, so a floor
		// enforced here is a floor the scheduler cannot be talked out of. The
		// form mirrors it, but a client is not where this can live.
		if s.Interval < minMinuteInterval || s.Interval > maxMinuteInterval {
			return fmt.Errorf("minute interval %d out of range %d-%d",
				s.Interval, minMinuteInterval, maxMinuteInterval)
		}
		return nil
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
	case FreqMinutely, FreqHourly, FreqDaily:
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
//
// The switch is EXHAUSTIVE rather than the negative test it replaced
// (`if s.Freq != FreqHourly`). That form routed any newly added frequency into
// the one-slot-per-day branch, which is not an error, not a log line and not a
// test failure: the new frequency would degrade to daily and look like it works.
//
// Every fire time is built with time.Date, so month lengths, leap years and DST
// stay delegated rather than reimplemented. Two DST consequences the minute walk
// inherits from the hourly one, both correct and neither needing code here:
//
//   - a spring-forward day's missing local hour normalizes BACKWARD, measured:
//     time.Date(2026, 3, 8, 2, 0, ...) in America/New_York is 01:00 EST. So those
//     slots land on instants the earlier slots already produced, the
//     strictly-after scan in NextRun skips them as past, and the 23-hour day
//     simply holds fewer slots. Nothing repeats and no day is skipped.
//   - a fall-back day's repeated local hour resolves to ONE instant per slot, so
//     those slots fire once rather than twice, at the price of one longer real
//     gap across the repeat.
//
// The list is therefore not monotonic on a spring-forward day, which is safe
// because NextRun returns the first element strictly after its argument and the
// out-of-order block is entirely in the past by the time it is reached.
func (s Spec) timesOn(day time.Time) []time.Time {
	at := func(h, m int) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
	}
	switch s.Freq {
	case FreqMinutely:
		// The capacity is stated because this list is an order of magnitude
		// bigger than the hourly one (288 entries at the floor, against 24 at
		// worst there), and NextRun builds it once per matched day per call.
		out := make([]time.Time, 0, minutesPerDay/s.Interval+1)
		for md := s.Minute % s.Interval; md < minutesPerDay; md += s.Interval {
			out = append(out, at(md/60, md%60))
		}
		return out
	case FreqHourly:
		out := make([]time.Time, 0, 24/s.Interval+1)
		for h := 0; h < 24; h += s.Interval {
			out = append(out, at(h, s.Minute))
		}
		return out
	case FreqDaily, FreqWeekly, FreqMonthly:
		return []time.Time{at(s.Hour, s.Minute)}
	default:
		// Unreachable through the store, since Put validates. Empty rather than a
		// daily fallback on purpose: NextRun then reports ErrNoOccurrence and the
		// runner says so once per tick, which is a signal instead of silence.
		return nil
	}
}
