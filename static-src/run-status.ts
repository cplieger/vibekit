import type { RunNodeStatus, RunStatus } from "./wire/types.gen.js";

export type ClassifiedRunStatus = RunStatus | "unknown";
export type ClassifiedRunNodeStatus = RunNodeStatus | "unknown";

const RUN_STATUS_MEMBERS: Readonly<Record<RunStatus, true>> = {
  running: true,
  paused: true,
  completed: true,
  failed: true,
  aborted: true,
  cancelled: true,
};

const RUN_NODE_STATUS_MEMBERS: Readonly<Record<RunNodeStatus, true>> = {
  pending: true,
  running: true,
  paused: true,
  completed: true,
  failed: true,
  aborted: true,
  skipped: true,
};

function isMember<T extends string>(members: Readonly<Record<T, true>>, value: string): value is T {
  return Object.hasOwn(members, value);
}

export function classifyRunStatus(status: string | undefined): ClassifiedRunStatus | undefined {
  if (status === undefined || status === "") {
    return undefined;
  }
  return isMember(RUN_STATUS_MEMBERS, status) ? status : "unknown";
}

export function classifyRunNodeStatus(status: string): ClassifiedRunNodeStatus {
  return isMember(RUN_NODE_STATUS_MEMBERS, status) ? status : "unknown";
}

export function runStatusTerminal(status: ClassifiedRunStatus): boolean {
  switch (status) {
    case "completed":
    case "failed":
    case "aborted":
    case "cancelled":
      return true;
    case "running":
    case "paused":
    case "unknown":
      return false;
  }
}

export function runStatusActive(status: ClassifiedRunStatus): boolean {
  switch (status) {
    case "running":
    case "paused":
    case "unknown":
      return true;
    case "completed":
    case "failed":
    case "aborted":
    case "cancelled":
      return false;
  }
}
