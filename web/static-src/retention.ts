// ---------------------------------------------------------------------------
// Chat retention state. Reads cleanup.periodDays from kiro-cli settings
// and exposes it to the tab-close handler (archive vs delete) and the
// history button (hidden when retention is 0).
//
// 0 = off (delete on tab close, no history).
// >0 = archive on tab close, show history, server purges after N days.
// ---------------------------------------------------------------------------

import { fetchKiroSetting, CancellableSlot } from "./api-client.js";
import { registerCleanup } from "./actions/index.js";

class RetentionController {
  private retentionDays = 1;
  private readonly refreshSlot = new CancellableSlot();
  private readonly listeners = new Set<() => void>();

  isRetentionEnabled(): boolean { return this.retentionDays > 0; }

  /** Subscribe to retention changes. Returns an unsubscribe function
   *  for symmetry with subscribeToActions / other registry-style APIs.
   *  Currently most callers register at module init and never unsubscribe,
   *  but the API shape is now consistent. */
  onRetentionChange(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => { this.listeners.delete(fn); };
  }

  async refreshRetention(): Promise<void> {
    const signal = this.refreshSlot.start();
    this.retentionDays = await fetchKiroSetting(
      "cleanup.periodDays",
      (v) => { const n = parseInt(v, 10); return (!isNaN(n) && n >= 0) ? n : null; },
      1,
      signal,
    );
    if (signal.aborted) return;
    for (const fn of this.listeners) fn();
  }

  cancelLoad(): void { this.refreshSlot.abort(); }
}

const instance = new RetentionController();
registerCleanup(() => instance.cancelLoad());

export function isRetentionEnabled(): boolean { return instance.isRetentionEnabled(); }
export function onRetentionChange(fn: () => void): () => void { return instance.onRetentionChange(fn); }
export async function refreshRetention(): Promise<void> { return instance.refreshRetention(); }
