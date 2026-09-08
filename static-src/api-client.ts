// Every REST-style call goes through here: each helper collapses failure to
// `null` (`apiDelete` to `false`) and logs it once. The POST /api/command
// envelope is transport.ts's — a different contract, deliberately not shared.

import {
  createFetch,
  type ApiErr,
  type ApiResult as FetchResult,
  type RequestOptions,
} from "@cplieger/fetch";

export { API_TIMEOUT_MS, withTimeout } from "@cplieger/fetch";

export type { Decoder } from "./validators.js";
import type { Decoder } from "./validators.js";

// No baseUrl and no prepareHeaders: every path is absolute same-origin and the
// client sends no CSRF token, because the server enforces an Origin check
// instead (internal/server/security.go). An ISOLATED instance, so nothing else
// can mutate vibekit's fetch layer through the module-global default.
const fx = createFetch({ credentials: "same-origin" });

/** Build fetch RequestOptions, attaching `signal` only when defined —
 *  exactOptionalPropertyTypes forbids an explicit `signal: undefined`. */
function reqOpts<T>(base: RequestOptions<T>, signal: AbortSignal | undefined): RequestOptions<T> {
  return signal ? { ...base, signal } : base;
}

/** Central failure logging for the collapsing helpers. A deliberate caller
 *  abort is expected and stays silent; every other failure gets one line. */
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
  console.warn("api: non-ok", method, path, r.status, r.error);
}

/** Collapse an envelope to `data | null`, logging failures centrally. An empty
 *  body (`undefined`) becomes null; a JSON `null` / `0` / `false` / `""` is real
 *  data and passes through. */
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

/** DELETE `path`. The body is never read (`ignoreBody`), so a 2xx carrying
 *  non-JSON counts as success and only 4xx/5xx and transport failures fail. */
export async function apiDelete(path: string, signal?: AbortSignal): Promise<boolean> {
  const r = await fx.apiDeleteRaw<unknown>(path, reqOpts({ ignoreBody: true }, signal));
  if (r.ok) {
    return true;
  }
  logApiError(r, "DELETE", path);
  return false;
}

/** A failure a caller has to distinguish rather than collapse. `error` is the
 *  server's own "error" field, or "" when the body carried none. */
interface ApiResult<T> {
  ok: boolean;
  status: number;
  data: T | null;
  error: string;
  /** Parsed body of a FAILED response whose detail sits outside the "error"
   *  key (/api/health's 503 `reason`). Undefined on success and on a non-JSON
   *  body. Server-controlled, at `error`'s trust level. */
  body?: unknown;
}

/** The ONE mapping onto `ApiResult`, shared by the three OrError helpers below;
 *  each still owns its verb, its decoder and whether it logs. `data` is dropped
 *  on the failure side — a caller handed a status has no business reading a body
 *  the transport rejected. */
function toApiResult<T>(r: FetchResult<T>): ApiResult<T> {
  if (r.ok) {
    return { ok: true, status: r.status, data: r.data ?? null, error: "" };
  }
  return { ok: false, status: r.status, data: null, error: r.error, body: r.body };
}

/** GET `path` and validate it with `decoder`; null on non-2xx, network error or
 *  decoder failure. `timeoutMs` overrides the 30s default, because a server
 *  budget LONGER than the client's is unreachable and a caller's
 *  `AbortSignal.timeout()` cannot substitute — signals compose, shorter wins. */
export async function apiGetTyped<T>(
  path: string,
  decoder: Decoder<T>,
  signal?: AbortSignal,
  timeoutMs?: number,
): Promise<T | null> {
  const base: RequestOptions<T> = timeoutMs === undefined ? { decoder } : { decoder, timeoutMs };
  return collapse(await fx.apiGetRaw<T>(path, reqOpts(base, signal)), "GET", path);
}

/** `apiGetTyped`'s OrError twin, for a caller that has to tell an ANSWER from the
 *  absence of one: a 404 licenses a terminal claim where a 5xx, a dead network and
 *  an abort license nothing, and the collapsing form answers null for all four.
 *  A rejected decoder lands on the failure side carrying the real 2xx status, so
 *  a caller keying on 404 cannot mistake an undecodable 200 for one. Logs
 *  nothing: an expected status is not a fault. */
export async function apiGetTypedOrError<T>(
  path: string,
  decoder: Decoder<T>,
  signal?: AbortSignal,
): Promise<ApiResult<T>> {
  return toApiResult(await fx.apiGetRaw<T>(path, reqOpts({ decoder }, signal)));
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
 *  server's validation message; otherwise prefer apiAction. `error` falls back
 *  to "HTTP <status>" when the body carried none. The one of the three that
 *  LOGS: a PUT is a mutation, so a failure is a fault, where the other two exist
 *  because their non-2xx statuses are expected. */
export async function apiPutOrError<T>(
  path: string,
  body: unknown,
  signal?: AbortSignal,
): Promise<ApiResult<T>> {
  const r = await fx.apiPutRaw<T>(path, body, reqOpts({}, signal));
  if (!r.ok) {
    logApiError(r, "PUT", path);
  }
  return toApiResult(r);
}

/** GET variant for a caller to whom a non-2xx body is itself meaningful —
 *  /api/health's 503 `reason` is the canonical consumer. */
export async function apiGetOrError<T>(path: string, signal?: AbortSignal): Promise<ApiResult<T>> {
  // Unlogged: that consumer polls health, where a 503 is an expected state.
  return toApiResult(await fx.apiGetRaw<T>(path, reqOpts({}, signal)));
}

/** One AbortController slot: `start()` aborts the previous in-flight request
 *  and returns a fresh signal. */
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
