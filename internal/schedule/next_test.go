package schedule

import (
	"testing"
	"time"
)

// The invariant the schedule row exists to keep: whatever the anchor says, a
// floored answer is in the FUTURE. A container that was down for a week leaves
// an anchor a week old, and NextRun measures strictly after its argument, so the
// unfloored derivation resolves to a slot that has already passed. Rendering
// that is the defect this helper removes, so every frequency is checked rather
// than one — the floor must be a property of the helper, not of a spec.
func TestNextRunFromNeverResolvesIntoThePast(t *testing.T) {
	now := at(2026, time.August, 12, 14, 37)
	tests := []struct {
		name string
		spec Spec
	}{
		{"hourly", Spec{Freq: FreqHourly, Interval: 6, Minute: 15}},
		{"daily", Spec{Freq: FreqDaily, Hour: 2, Minute: 30}},
		{"weekly", Spec{Freq: FreqWeekly, Weekdays: []int{int(time.Monday)}, Hour: 9}},
		{"monthly", Spec{Freq: FreqMonthly, MonthDay: 1, Hour: 3}},
		{"monthly on the last day", Spec{Freq: FreqMonthly, MonthDay: LastDay, Hour: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, staleBy := range []time.Duration{
				time.Hour,
				48 * time.Hour,
				7 * 24 * time.Hour,
				90 * 24 * time.Hour,
			} {
				anchor := now.Add(-staleBy)
				got, err := NextRunFrom(tt.spec, anchor, now)
				if err != nil {
					t.Errorf("stale by %s: NextRunFrom: %v", staleBy, err)
					continue
				}
				if !got.After(now) {
					t.Errorf("stale by %s: got %s, which is not after now (%s)", staleBy, got, now)
				}
				// The floored answer is also the first slot after now, so the row
				// names the very next fire and not one further out.
				want, err := NextRun(tt.spec, now)
				if err != nil {
					t.Fatalf("NextRun: %v", err)
				}
				if !got.Equal(want) {
					t.Errorf("stale by %s: got %s, want the first slot after now %s", staleBy, got, want)
				}
			}
		})
	}
}

// The ANCHOR is the origin, not the floor. A schedule that just fired must not
// be dragged back to an earlier slot by a floor that sits before its anchor,
// which is exactly what the REST view used to do by measuring from now alone.
func TestNextRunFromKeepsTheAnchorAsOrigin(t *testing.T) {
	spec := Spec{Freq: FreqDaily, Hour: 2, Minute: 0}
	anchor := at(2026, time.August, 12, 3, 0) // fired late, past today's slot
	floor := at(2026, time.August, 12, 1, 0)  // before the anchor

	got, err := NextRunFrom(spec, anchor, floor)
	if err != nil {
		t.Fatalf("NextRunFrom: %v", err)
	}
	if want := at(2026, time.August, 13, 2, 0); !got.Equal(want) {
		t.Errorf("got %s, want %s (the slot after the anchor, not after the floor)", got, want)
	}
}

// The runner passes a zero floor and must keep seeing a slot that has already
// gone: sweep's two branches (fire inside MissGrace, skip when older) both read
// that value, so a helper that floored unconditionally would stop every schedule
// from ever firing.
func TestNextRunFromZeroFloorIsTheRawSlot(t *testing.T) {
	spec := Spec{Freq: FreqDaily, Hour: 2, Minute: 0}
	anchor := at(2026, time.August, 5, 2, 0)
	wantDue := at(2026, time.August, 6, 2, 0) // long past by any real "now"

	got, err := NextRunFrom(spec, anchor, time.Time{})
	if err != nil {
		t.Fatalf("NextRunFrom: %v", err)
	}
	if !got.Equal(wantDue) {
		t.Errorf("got %s, want the unfloored slot %s", got, wantDue)
	}
	raw, err := NextRun(spec, anchor)
	if err != nil {
		t.Fatalf("NextRun: %v", err)
	}
	if !got.Equal(raw) {
		t.Errorf("zero floor changed the runner's answer: got %s, NextRun says %s", got, raw)
	}
}

// A spec the store could never hold, in case one arrives from an older file:
// the error is NextRun's, passed through rather than swallowed into a zero time
// the caller would render as a real date.
func TestNextRunFromPassesValidationErrorsThrough(t *testing.T) {
	now := at(2026, time.August, 12, 14, 0)
	if _, err := NextRunFrom(Spec{Freq: "yearly"}, now, now); err == nil {
		t.Error("expected an error for an unknown frequency")
	}
	if _, err := NextRunFrom(Spec{Freq: FreqWeekly, Hour: 9}, now, now); err == nil {
		t.Error("expected an error for a weekly spec with no weekdays")
	}
}
