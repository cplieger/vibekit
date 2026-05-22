// ---------------------------------------------------------------------------
// Chat retention state. Reads cleanup.periodDays from kiro-cli settings
// and exposes it to the tab-close handler (archive vs delete) and the
// history button (hidden when retention is 0).
//
// 0 = off (delete on tab close, no history).
// >0 = archive on tab close, show history, server purges after N days.
// ---------------------------------------------------------------------------

import { fetchKiroSetting, CancellableSlot } from "./api-client.js";

class RetentionController {
  private retentionDays = 1;
  private readonly refreshSlot = new CancellableSlot();
  private readonly listeners = new Set<() => void>();

  isRetentionEnabled(): boolean { return this.retentionDays > 0; }

  onRetentionChange(fn: () => void): void { this.listeners.add(fn); }

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
}

const instance = new RetentionController();

export function isRetentionEnabled(): boolean { return instance.isRetentionEnabled(); }
export function onRetentionChange(fn: () => void): void { instance.onRetentionChange(fn); }
export async function refreshRetention(): Promise<void> { return instance.refreshRetention(); }
