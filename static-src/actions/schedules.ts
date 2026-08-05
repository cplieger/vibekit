// Workflow-schedule actions: the Schedule button beside Run on /docs/workflows.
//
// The server owns the recurrence math and resolves next_run_at, so these are
// plain transport — nothing here recomputes a fire time.

import { apiAction, retryNetwork, RETRY_STANDARD } from "./index.js";
import type { ScheduleSpec, SchedulesResponse, ScheduleView } from "../schedule-types.js";

export const loadSchedules = apiAction<
  // eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args
  void,
  SchedulesResponse
>({
  name: "schedules.list",
  dedupe: true,
  retryable: retryNetwork,
  retry: RETRY_STANDARD,
  request: () => ({ method: "GET", path: "/api/schedules" }),
  error: "Couldn't load schedules",
});

/** Insert or replace one recipe's schedule. */
export const saveSchedule = apiAction<
  { source: string; spec: ScheduleSpec; enabled: boolean },
  ScheduleView
>({
  name: "schedules.save",
  request: (a) => ({
    method: "POST",
    path: "/api/schedules",
    body: { source: a.source, spec: a.spec, enabled: a.enabled },
  }),
  error: "Couldn't save the schedule",
});

/** Remove a recipe's schedule. */
export const deleteSchedule = apiAction<string, { ok: boolean }>({
  name: "schedules.delete",
  request: (id) => ({ method: "DELETE", path: `/api/schedules/${encodeURIComponent(id)}` }),
  error: "Couldn't remove the schedule",
});
