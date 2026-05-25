// Live console logger: subscribes to the action registry and emits a
// console.error for every action that fails. Complements the user-
// facing toast (which is suppressed for actions with `error: false`)
// by giving developers a structured trail in DevTools regardless of
// toast policy.
//
// Errors only — successes are intentionally silent so the console
// isn't spammed with every keystroke-driven action. Cancelled
// instances are also skipped (cancellation is usually intentional).
//
// Wired once at app startup via initActionConsoleLog().
// ---------------------------------------------------------------------------

import { subscribe } from "./registry.js";

let unsubscribe: (() => void) | null = null;

/** Subscribe to the registry and live-log action errors to the
 *  browser console. Idempotent — calling twice replaces the previous
 *  subscription. Returns a teardown fn. */
export function initActionConsoleLog(): () => void {
  unsubscribe?.();
  const unsub = subscribe((inst) => {
    if (inst.status !== "error") return;
    if (inst.error === undefined) return;
    const duration = inst.completedAt !== undefined
      ? `${String(inst.completedAt - inst.startedAt)}ms`
      : "?ms";
    // Format: one line, then the full error object expanded by the
    // browser console. Status / code are surfaced inline so a quick
    // scan in DevTools reveals classification without expanding.
    const meta: string[] = [duration];
    if (inst.attempts !== undefined && inst.attempts > 1) meta.push(`${String(inst.attempts)} attempts`);
    if (inst.error.status !== undefined) meta.push(`HTTP ${String(inst.error.status)}`);
    if (inst.error.code !== undefined) meta.push(inst.error.code);
    console.error(
      `[action] ${inst.name} failed (${meta.join(", ")}): ${inst.error.message}`,
      inst.error,
    );
  });
  unsubscribe = unsub;
  return () => {
    if (unsubscribe === unsub) {
      unsub();
      unsubscribe = null;
    }
  };
}

/** Test-only: reset internal state. */
export function _resetForTest(): void {
  unsubscribe?.();
  unsubscribe = null;
}
