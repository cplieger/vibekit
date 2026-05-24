// Persisted error tail: captures the last N action errors to
// localStorage so a future "report a bug" / feedback flow can
// attach them to the report. Captures only metadata (action name,
// timestamp, error message + code + status). Args and stack traces
// are intentionally excluded — they may contain sensitive data
// (file paths, prompts, secrets in env files).
//
// Off by default — opt-in by calling initErrorTail() at startup.
// ---------------------------------------------------------------------------

import type { ActionInstance } from "./types.js";
import { subscribe } from "./registry.js";

const STORAGE_KEY = "vk.errors";
const MAX_ENTRIES = 20;

export interface PersistedError {
  readonly name: string;
  readonly at: number;        // Date.now() of failure
  readonly message: string;
  readonly code?: string;
  readonly status?: number;
}

let unsubscribe: (() => void) | null = null;

/** Subscribe to the registry and persist error instances to
 *  localStorage. Idempotent. Returns a teardown fn. */
export function initErrorTail(): () => void {
  unsubscribe?.();
  unsubscribe = subscribe((inst) => {
    if (inst.status !== "error") return;
    if (inst.error === undefined) return;
    const entry = toEntry(inst);
    persist(entry);
  });
  return teardown;
}

function teardown(): void {
  unsubscribe?.();
  unsubscribe = null;
}

/** Read the persisted error list (most-recent first). Safe to call
 *  even before initErrorTail. */
export function getRecentErrors(): readonly PersistedError[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null || raw === "") return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    // Defensive: filter out entries that don't have the required fields.
    return parsed.filter(isValidEntry);
  } catch {
    // Corrupt / unavailable storage. Treat as empty.
    return [];
  }
}

/** Wipe the persisted error list. */
export function clearRecentErrors(): void {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    /* ignore */
  }
}

function persist(entry: PersistedError): void {
  try {
    const current = [...getRecentErrors()];
    current.unshift(entry);
    if (current.length > MAX_ENTRIES) current.length = MAX_ENTRIES;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(current));
  } catch {
    // localStorage full or unavailable. Drop the entry silently;
    // we don't want a quota error to surface to the user as an
    // action toast.
  }
}

function toEntry(inst: ActionInstance): PersistedError {
  const err = inst.error;
  const out: {
    name: string;
    at: number;
    message: string;
    code?: string;
    status?: number;
  } = {
    name: inst.name,
    at: inst.completedAt ?? inst.startedAt,
    message: err?.message ?? "(no message)",
  };
  if (err?.code !== undefined) out.code = err.code;
  if (err?.status !== undefined) out.status = err.status;
  return out;
}

function isValidEntry(value: unknown): value is PersistedError {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v["name"] === "string" &&
    typeof v["at"] === "number" &&
    typeof v["message"] === "string"
  );
}

/** Test-only: reset internal state. */
export function _resetForTest(): void {
  teardown();
}
