// ---------------------------------------------------------------------------
// Thin API client: one shared shape for every REST-style fetch call.
// Each helper returns `null` on failure (apiDelete returns `false`); callers
// narrow and decide. Errors are logged centrally via console.warn so devtools
// has a consistent audit trail.
//
// The request/response core is @cplieger/fetch. This module is a thin adapter
// that (1) pins vibekit's fetch config on an isolated createFetch instance,
// (2) maps @cplieger/fetch's non-throwing ApiResult envelope onto vibekit's
// historical null/false-collapsing convention, and (3) centralizes the
// console.warn / console.error logging. The public surface (apiGet / apiPost /
// apiDelete / apiGetTyped / apiPostTyped / apiPutOrError + CancellableSlot +
// fetchKiroSetting) is unchanged, so the call sites don't move.
//
// NOT used for the POST /api/command envelope — that's a different contract
// (request_id dedup, typed SendResult with status codes) served by
// transport.ts's `send()` function. Keep the two separate.
// ---------------------------------------------------------------------------

import {
  createFetch,
  type ApiErr,
  type ApiResult as FetchResult,
  type RequestOptions,
} from "@cplieger/fetch";

// API_TIMEOUT_MS and withTimeout come from @cplieger/fetch — the toolkit's
// single timeout-composition implementation (actions dropped its duplicate
// copies in v3). Re-exported here so existing consumers (e.g.
// editor-openers.ts) keep importing them from "./api-client.js" unchanged.
export { API_TIMEOUT_MS, withTimeout } from "@cplieger/fetch";

// Re-export the local Decoder<T> type so callers don't need a second import.
export type { Decoder } from "./validators.js";
import type { Decoder } from "./validators.js";

// vibekit's own isolated fetch layer. `credentials: "same-origin"` is the
// browser default the hand-rolled core relied on; every call targets an
// absolute same-origin path (e.g. "/api/whoami"), so there's no baseUrl and no
// prepareHeaders hook — the client sends no CSRF token, and the server enforces
// an Origin check instead (internal/server/security.go). An isolated
// createFetch instance keeps this config off the module-global default so
// nothing else can mutate vibekit's fetch layer.
const fx = createFetch({ credentials: "same-origin" });

/** Build fetch RequestOptions, attaching `signal` only when defined —
 *  exactOptionalPropertyTypes forbids an explicit `signal: undefined`. */
function reqOpts<T>(base: RequestOptions<T>, signal: AbortSignal | undefined): RequestOptions<T> {
  return signal ? { ...base, signal } : base;
}

/** Central failure logging for the collapsing helpers, mapping @cplieger/fetch's
 *  error envelope onto the log shapes the hand-rolled core used:
 *   - a deliberate caller abort (code "cancelled", status 0) is expected and
 *     never logged — a caller aborted an in-flight request via
 *     AbortController.abort() (e.g. the git Changes tab supersedes a refresh,
 *     or a CancellableSlot rotates);
 *   - a network error / timeout / client-side build failure (status 0) logs
 *     "api: fetch failed";
 *   - a 2xx body that failed JSON.parse or a decoder shape check (code
 *     "decode") logs "api: decode failed";
 *   - a real non-2xx HTTP response logs "api: non-ok". */
function logApiError(r: ApiErr, method: string, path: string): void {
  if (r.status === 0) {
    if (r.code === "cancelled") {
      return;
    }
    console.warn("api: fetch failed", method, path, r.error);
    return;
  }
  if (r.code === "decode") {
    console.error("api: decode failed:", method, path, r.error);
    return;
  }
  // r.error carries the server's own message (the `{"error": …}` body every
  // api.* helper writes). It used to be dropped here, so a non-2xx logged its
  // status and nothing about the cause, and `collapse` then returned null — so
  // the message existed on the wire and reached neither the console nor the
  // caller.
  console.warn("api: non-ok", method, path, r.status, r.error);
}

/** Collapse a @cplieger/fetch envelope to `data | null`, logging failures
 *  centrally. A 204 / empty-body response (data === undefined) collapses to
 *  null; a JSON `null` / `0` / `false` / `""` body is real data and passes
 *  through unchanged. */
function collapse<T>(r: FetchResult<T>, method: string, path: string): T | null {
  if (r.ok) {
    return r.data ?? null;
  }
  logApiError(r, method, path);
  return null;
}

/** GET `path` and return parsed JSON, or null on failure. */
export async function apiGet<T>(path: string, signal?: AbortSignal): Promise<T | null> {
  return collapse(await fx.apiGetRaw<T>(path, reqOpts({}, signal)), "GET", path);
}

/** POST `body` as JSON to `path`, return parsed JSON response or null. */
export async function apiPost<T>(
  path: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T | null> {
  return collapse(await fx.apiPostRaw<T>(path, body, reqOpts({}, signal)), "POST", path);
}

/** DELETE `path`. Returns true on success, false on failure. The response
 *  body is deliberately never read (`ignoreBody`) — the hand-rolled core
 *  never read DELETE bodies, so a 2xx with a non-JSON body counts as success
 *  and only 4xx/5xx and transport failures are real failures. */
export async function apiDelete(path: string, signal?: AbortSignal): Promise<boolean> {
  const r = await fx.apiDeleteRaw<unknown>(path, reqOpts({ ignoreBody: true }, signal));
  if (r.ok) {
    return true;
  }
  logApiError(r, "DELETE", path);
  return false;
}

/** Result shape for apiPutOrError: on 2xx `ok` is true and `data` is the
 *  parsed body; on 4xx/5xx `ok` is false, `status` is the HTTP status, and
 *  `error` is the parsed "error" field from the server's JSON body (empty
 *  string if the body didn't include one). Used by forms that need to surface
 *  specific failure reasons (400 validation errors, 409 conflicts) inline
 *  instead of silently failing. */
interface ApiResult<T> {
  ok: boolean;
  status: number;
  data: T | null;
  error: string;
  /** Parsed JSON body of a FAILED response, when the server sent one
   *  (e.g. /api/health's 503 `{"status":"unready","reason":...}`,
   *  which carries its detail outside the standard "error" key).
   *  Undefined on success and on non-JSON/empty error bodies.
   *  Server-controlled content — same trust level as `error`. */
  body?: unknown;
}

/** GET `path`, validate the response with `decoder`, return the typed value or
 *  null on non-2xx, network error, or decoder failure. Failures are logged
 *  centrally. */
export async function apiGetTyped<T>(
  path: string,
  decoder: Decoder<T>,
  signal?: AbortSignal,
): Promise<T | null> {
  return collapse(await fx.apiGetRaw<T>(path, reqOpts({ decoder }, signal)), "GET", path);
}

/** POST variant of apiGetTyped: validates the 2xx response body via the
 *  provided decoder, returning null on non-2xx / network / decode failure. */
export async function apiPostTyped<T>(
  path: string,
  body: unknown,
  decoder: Decoder<T>,
  signal?: AbortSignal,
): Promise<T | null> {
  return collapse(await fx.apiPostRaw<T>(path, body, reqOpts({ decoder }, signal)), "POST", path);
}

/** PUT variant that surfaces error details. Use when the UI must show the
 *  server's validation message; otherwise prefer apiAction. On a non-2xx the
 *  `error` field carries the server's JSON "error" message (or "HTTP <status>"
 *  when the body didn't include one), matching the old hand-rolled behavior. */
export async function apiPutOrError<T>(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<ApiResult<T>> {
  const r = await fx.apiPutRaw<T>(path, body, reqOpts({}, signal));
  if (r.ok) {
    return { ok: true, status: r.status, data: r.data ?? null, error: "" };
  }
  logApiError(r, "PUT", path);
  return { ok: false, status: r.status, data: null, error: r.error };
}

/** GET variant that surfaces error details instead of collapsing every
 *  failure to null. Use when a non-2xx body is itself meaningful —
 *  /api/health's 503 envelope is the canonical consumer (the degraded
 *  runtime banner needs the `reason` field). On failure, `body` carries
 *  the parsed error JSON when the server sent one. */
export async function apiGetOrError<T>(path: string, signal?: AbortSignal): Promise<ApiResult<T>> {
  const r = await fx.apiGetRaw<T>(path, reqOpts({}, signal));
  if (r.ok) {
    return { ok: true, status: r.status, data: r.data ?? null, error: "" };
  }
  // No logApiError: the canonical consumer polls health where a 503 is
  // an EXPECTED state, not a fault worth a console audit line.
  return { ok: false, status: r.status, data: null, error: r.error, body: r.body };
}

// --- CancellableSlot: reusable abort-controller lifecycle helper ---

/** Manages a single AbortController slot. Calling start() aborts any
 *  prior in-flight request and returns a fresh signal. Eliminates the
 *  repeated `ctrl?.abort(); ctrl = new AbortController()` boilerplate. */
export class CancellableSlot {
  private ctrl: AbortController | null = null;
  /** Abort any in-flight request and return a fresh signal. */
  start(): AbortSignal {
    this.ctrl?.abort();
    this.ctrl = new AbortController();
    return this.ctrl.signal;
  }
  /** Abort without starting a new request. */
  abort(): void {
    this.ctrl?.abort();
    this.ctrl = null;
  }
}

/** Fetch a kiro-cli setting by key, parse it with the provided function,
 *  and return the fallback if the fetch fails or parsing yields an invalid
 *  value. Consolidates the repeated fetch→parse→validate→default pattern
 *  used by status.ts, retention.ts, and settings.ts consumers. */
export async function fetchKiroSetting<T>(
  key: string,
  parse: (raw: string) => T | null,
  fallback: T,
  signal?: AbortSignal,
): Promise<T> {
  const d = await apiGet<{ value?: string }>(
    `/api/kiro-settings?key=${encodeURIComponent(key)}`,
    signal,
  );
  const raw = d?.value ?? "";
  if (raw === "") {
    return fallback;
  }
  const parsed = parse(raw);
  return parsed ?? fallback;
}
