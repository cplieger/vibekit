// ---------------------------------------------------------------------------
// Chat retention state. Reads cleanup.periodDays from kiro-cli settings
// and exposes it to the tab-close handler (archive vs delete) and the
// history button (hidden when retention is 0).
//
// 0 = off (delete on tab close, no history).
// >0 = archive on tab close, show history, server purges after N days.
//
// Backed by a reactive signal (see settings-tabs.ts for the same pattern):
// onRetentionChange is just subscribe(), so consumers re-run on every
// change. subscribe() also fires immediately with the current value — the
// only consumer (the history-button toggle) is idempotent, so the extra
// initial run is harmless.
// ---------------------------------------------------------------------------

import { signal, subscribe } from "@cplieger/reactive";
import { fetchKiroSetting } from "./api-client.js";
import { defineAction, retryNetwork } from "./actions/index.js";

const retentionDays = signal(1);

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
  return retentionDays.value > 0;
}

/** Subscribe to retention changes. Returns an unsubscribe function.
 *  Fires immediately with the current value on subscribe. */
export function onRetentionChange(fn: () => void): () => void {
  return subscribe(retentionDays, fn);
}

export async function refreshRetention(): Promise<void> {
  const result = await refreshRetentionAction.dispatch(undefined);
  if (result === null) {
    return;
  }
  retentionDays.value = result;
}
