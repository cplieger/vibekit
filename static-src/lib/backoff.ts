// ---------------------------------------------------------------------------
// Exponential backoff computation — used by transport.ts (SSE reconnect).
// ---------------------------------------------------------------------------

export const BACKOFF_CAP_MS = 30_000;

/** Compute the next backoff delay given the previous backoffMs.
 *  Doubles from 500ms up to a 30s cap; delay is randomized within [0, next). */
export function computeBackoff(prevBackoffMs: number): { delay: number; backoffMs: number } {
  const next = Math.min(prevBackoffMs === 0 ? 500 : prevBackoffMs * 2, BACKOFF_CAP_MS);
  return { delay: Math.floor(Math.random() * next), backoffMs: next };
}
