// ---------------------------------------------------------------------------
// Chat retention state. Reads cleanup.periodDays from kiro-cli settings
// and exposes it to the tab-close handler (archive vs delete) and the
// history button (hidden when retention is 0).
//
// 0 = off (delete on tab close, no history).
// >0 = archive on tab close, show history, server purges after N days.
// ---------------------------------------------------------------------------

import { fetchKiroSetting } from "./api-client.js";
import { defineAction, retryNetwork } from "./actions/index.js";

let retentionDays = 1;
const listeners = new Set<() => void>();

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
const refreshRetentionAction = defineAction<void, number>({
  name: "settings.refresh_retention",
  dedupe: true,
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, signal) => {
    return fetchKiroSetting(
      "cleanup.periodDays",
      (v) => {
        const n = parseInt(v, 10);
        return !isNaN(n) && n >= 0 ? n : null;
      },
      1,
      signal,
    );
  },
  error: false,
});

export function isRetentionEnabled(): boolean {
  return retentionDays > 0;
}

/** Subscribe to retention changes. Returns an unsubscribe function. */
export function onRetentionChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
  };
}

export async function refreshRetention(): Promise<void> {
  const result = await refreshRetentionAction.dispatch(undefined);
  if (result === null) {
    return;
  }
  retentionDays = result;
  for (const fn of listeners) {
    fn();
  }
}
