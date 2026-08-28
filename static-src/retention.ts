// ---------------------------------------------------------------------------
// Chat retention state. Reads the vibekit-owned chat_retention_days setting
// (/api/settings) and exposes it to the tab-close handler (keep vs delete) and
// the history button (hidden when retention is off).
//
// vibekit owns retention end to end — kiro-cli's own cleanup.periodDays is
// pinned to 0/never, so there are not two systems fighting over one value.
// There is no archive: a closed chat stays exactly where it was, and
// "archived" is computed from its age against the window. Encoding:
//
//   -1 = forever → close keeps the chat, show History, never purged ("backups").
//    0 = off     → NO retention: closing a tab deletes the chat (ephemeral,
//                  lost on close) and the History button is hidden.
//    N = keep N days → close keeps the chat, show History, server purges after N.
//
// So retention is "enabled" (close keeps, History shown) whenever the value is
// not 0 — both a positive day count and forever.
//
// Backed by a reactive signal: onRetentionChange is just subscribe(), so
// consumers re-run on every change. subscribe() also fires immediately with
// the current value — the only consumer (the history-button toggle) is
// idempotent, so the extra initial run is harmless.
// ---------------------------------------------------------------------------

import { signal, subscribe } from "@cplieger/reactive";
import { apiGetTyped } from "./api-client.js";
import { defineAction, retryNetwork } from "./actions/index.js";
import { decodeEffectiveSettings } from "./wire/decoders.gen.js";

// There is no DEFAULT_RETENTION_DAYS any more, and its absence is the point.
// It mirrored settings.DefaultChatRetentionDays with nothing holding the two
// together, applying to a config.json that exists WITHOUT the key — which is
// every install that never touched the setting. The server now resolves defaults
// into the GET, so the payload always carries a real value and there is nothing
// here to keep in step.
//
// The signal's initial value is a placeholder for the window before the first
// fetch resolves, NOT a default: isRetentionEnabled() reads `!== 0`, and starting
// at 0 would report History hidden and chats ephemeral for that window. Any
// nonzero placeholder does; 7 is chosen only to match the common case so the
// pre-fetch and post-fetch answers usually agree.
const retentionDays = signal(7);

// eslint-disable-next-line @typescript-eslint/no-invalid-void-type
const refreshRetentionAction = defineAction<void, number>({
  name: "settings.refresh_retention",
  dedupe: true,
  retryable: retryNetwork,
  retry: { count: 2, delay: 300 },
  run: async (_args, signal) => {
    // Decoded through the generated decoder, so the required field this reads is
    // checked at the boundary rather than asserted by a cast.
    const s = await apiGetTyped("/api/settings", decodeEffectiveSettings, signal);
    if (s === null) {
      // Network, non-2xx or a payload the decoder rejected: throw so dispatch
      // resolves null and refreshRetention leaves the current value in place
      // rather than clobbering it.
      throw new Error("retention: /api/settings unavailable");
    }
    // No coalesce: the field is required on the payload, and the server has
    // already resolved its default and type-checked the stored value.
    return s.chat_retention_days;
  },
  error: false,
});

/** Whether closing a tab KEEPS the chat (and History is shown). True for a
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
