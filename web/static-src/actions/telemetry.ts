// Telemetry adapter: optional subscriber that emits action lifecycle
// events (success/error/cancelled) to a configurable sink. Off by
// default — opt-in via initTelemetry({ sink }) at startup or by
// setting localStorage["vk.telemetry"] = "1".
//
// What gets emitted:
//   { name, status, durationMs, errorCode?, errorStatus? }
//
// Args + result are NOT included to avoid leaking PII / file
// contents / chat messages. Only metadata about the action's
// lifecycle is observable.
//
// The sink can be:
//   - A function: (event) => void | Promise<void>
//   - undefined (default): a console.debug logger that's also a no-op
//     in production builds.
//
// Telemetry runs ONLY on terminal states (success / error / cancelled).
// Pending events are filtered out — they'd double the volume without
// adding signal.
// ---------------------------------------------------------------------------

import type { ActionInstance } from "./types.js";
import { subscribe } from "./registry.js";

export interface TelemetryEvent {
  readonly name: string;
  readonly status: "success" | "error" | "cancelled";
  readonly durationMs: number;
  readonly errorCode?: string;
  readonly errorStatus?: number;
}

export interface TelemetryOptions {
  /** Where lifecycle events go. Defaults to a console.debug logger. */
  sink?: (event: TelemetryEvent) => void | Promise<void>;
  /** Bypass the localStorage opt-in flag and force-enable. */
  force?: boolean;
}

let unsubscribe: (() => void) | null = null;

/** Initialize telemetry. Returns a teardown fn (also stored in module
 *  state for idempotent re-init). Idempotent: calling twice replaces
 *  the previous subscription. */
export function initTelemetry(opts: TelemetryOptions = {}): () => void {
  // Tear down any prior subscription so initTelemetry({...}) twice
  // doesn't double-emit.
  unsubscribe?.();
  unsubscribe = null;

  if (opts.force !== true && !isOptedIn()) {
    return noop;
  }

  const sink = opts.sink ?? defaultSink;

  unsubscribe = subscribe((inst) => {
    if (inst.status === "pending") return;
    if (inst.completedAt === undefined) return;
    const event = toEvent(inst);
    // Run the sink in a microtask so a slow / rejecting sink can't
    // stall dispatch. Any thrown / rejected value is swallowed and
    // logged so a buggy adapter never breaks other registry listeners.
    queueMicrotask(() => {
      void (async () => {
        try {
          await sink(event);
        } catch (err) {
          console.error("[telemetry] sink rejected:", err);
        }
      })();
    });
  });

  return teardown;
}

function teardown(): void {
  unsubscribe?.();
  unsubscribe = null;
}

function isOptedIn(): boolean {
  try {
    return localStorage.getItem("vk.telemetry") === "1";
  } catch {
    // localStorage may throw in private-mode iframes; treat as opt-out.
    return false;
  }
}

function toEvent(inst: ActionInstance): TelemetryEvent {
  const completedAt = inst.completedAt ?? inst.startedAt;
  const out: {
    name: string;
    status: "success" | "error" | "cancelled";
    durationMs: number;
    errorCode?: string;
    errorStatus?: number;
  } = {
    name: inst.name,
    status: inst.status as "success" | "error" | "cancelled",
    durationMs: completedAt - inst.startedAt,
  };
  if (inst.error !== undefined) {
    if (inst.error.code !== undefined) out.errorCode = inst.error.code;
    if (inst.error.status !== undefined) out.errorStatus = inst.error.status;
  }
  return out;
}

function defaultSink(event: TelemetryEvent): void {
  // console.debug so it's filterable in DevTools without spamming
  // the default Console view.
  console.debug("[action]", event);
}

function noop(): void {
  /* noop teardown when telemetry is opted-out */
}

/** Test-only: reset internal state. */
export function _resetForTest(): void {
  teardown();
}
