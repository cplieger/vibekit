// Workflow-schedule wire types. Hand-declared beside the feature (the
// forge-types.ts precedent) rather than generated: the schedule surface is
// three endpoints and one record.

/** Recurrence frequency; mirrors internal/schedule.Freq. */
export type ScheduleFreq = "minutely" | "hourly" | "daily" | "weekly" | "monthly";

/** MonthDay sentinel meaning "the last day of the month". */
export const LAST_DAY = -1;

/** Bounds on the `interval` step, per frequency, mirroring internal/schedule's
 *  minMinuteInterval / maxMinuteInterval and its hourly range.
 *
 *  The minute FLOOR is not cosmetic, and it is the reason this table exists at
 *  all: "every minute" is what a user types when they mean "often", and the
 *  server refuses it because four mechanisms misbehave there (the once-a-minute
 *  runner tick, the 3-minute miss grace, the one-live-run-per-recipe rule, and
 *  the run's own interval-derived deadline, which logs at ERROR for an alert
 *  rule). The form enforces it so the user meets the rule where they can act on
 *  it; the server enforces it because that is the gate the scheduler trusts.
 *  The ceiling keeps this frequency sub-hour: an hour or more is `hourly`.
 *  Change both sides together. */
export const INTERVAL_BOUNDS = {
  minutely: { min: 5, max: 59 },
  hourly: { min: 1, max: 24 },
} as const;

/** One recurrence rule; mirrors internal/schedule.Spec. */
export interface ScheduleSpec {
  freq: ScheduleFreq;
  /** Recurrence step in the unit its freq names: minutes for `minutely`,
   *  hours for `hourly`. Bounded per frequency by INTERVAL_BOUNDS. */
  interval?: number;
  /** Days for `weekly`, as JS/Go weekday numbers (0 = Sunday). */
  weekdays?: number[];
  /** Day for `monthly` (1-31, or LAST_DAY). */
  month_day?: number;
  hour: number;
  /** Minute of the hour (0-59). The time of day for the calendar frequencies,
   *  the offset past each stepped hour for `hourly`, and the step's PHASE for
   *  `minutely` (taken as minute % interval, so the chosen minute is always
   *  itself a fire time). */
  minute: number;
}

/** One schedule as the server renders it, next run already resolved. */
export interface ScheduleView {
  id: string;
  source: string;
  name?: string;
  spec: ScheduleSpec;
  enabled: boolean;
  /** RFC3339; the server computes this so the client never does. */
  next_run_at?: string;
  last_run_at?: string;
  last_result?: string;
}

export interface SchedulesResponse {
  schedules: ScheduleView[];
}
