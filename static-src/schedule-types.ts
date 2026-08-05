// Workflow-schedule wire types. Hand-declared beside the feature (the
// forge-types.ts precedent) rather than generated: the schedule surface is
// three endpoints and one record.

/** Recurrence frequency; mirrors internal/schedule.Freq. */
export type ScheduleFreq = "hourly" | "daily" | "weekly" | "monthly";

/** MonthDay sentinel meaning "the last day of the month". */
export const LAST_DAY = -1;

/** One recurrence rule; mirrors internal/schedule.Spec. */
export interface ScheduleSpec {
  freq: ScheduleFreq;
  /** Hour step for `hourly` (1-24). */
  interval?: number;
  /** Days for `weekly`, as JS/Go weekday numbers (0 = Sunday). */
  weekdays?: number[];
  /** Day for `monthly` (1-31, or LAST_DAY). */
  month_day?: number;
  hour: number;
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
