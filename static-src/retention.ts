// ---------------------------------------------------------------------------
// Chat retention state. Reads the vibekit-owned chat_retention_days setting
// (/api/settings) and exposes it to the tab-close handler (archive vs delete)
// and the history button (hidden when retention is off).
//
// vibekit owns retention end to end — kiro-cli's own cleanup.periodDays is
// pinned to 0/never, so there are not two systems fighting over one value.
// Encoding:
//
//   -1 = forever → archive on tab close, show History, never purged ("backups").
//    0 = off     → NO retention: closing a tab deletes the chat (ephemeral,
//                  lost on close) and the History button is hidden.
//    N = keep N days → archive on tab close, show History, server purges after N.
//
// So retention is "enabled" (close archives, History shown) whenever the value
// is not 0 — both a positive day count and forever archive.
//
// Backed by a reactive signal: onRetentionChange is just subscribe(), so
// consumers re-run on every change. subscribe() also fires immediately with
// the current value — the only consumer (the history-button toggle) is
// idempotent, so the extra initial run is harmless.
// ---------------------------------------------------------------------------

import { signal, subscribe } from "@cplieger/reactive";
import { apiGet } from "./api-client.js";
import { defineAction, retryNetwork } from "./actions/index.js";

// Default 1 day, matching settings.DefaultChatRetentionDays server-side.
const retentionDays = signal(1);

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
const refreshRetentionAction = defineAction<void, number>({
  name: "settings.refresh_retention",
  dedupe: true,
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, signal) => {
    const s = await apiGet<{ chat_retention_days?: number }>("/api/settings", signal);
    if (s === null) {
      // Network/non-2xx: throw so dispatch resolves null and refreshRetention
      // leaves the current value in place rather than clobbering it.
      throw new Error("retention: /api/settings unavailable");
    }
    return typeof s.chat_retention_days === "number" ? s.chat_retention_days : 1;
  },
  error: false,
});

/** Whether closing a tab archives the chat (and History is shown). True for a
 *  positive day count AND forever (-1); false only when retention is off (0). */
export function isRetentionEnabled(): boolean {
  return retentionDays.value !== 0;
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
