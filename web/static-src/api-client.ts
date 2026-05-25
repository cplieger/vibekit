// ---------------------------------------------------------------------------
// Thin API client: one shared shape for every REST-style fetch call.
// Each helper returns `null` on failure; callers narrow and decide.
// Errors are logged centrally via console.warn so devtools has a
// consistent audit trail.
//
// NOT used for the POST /api/command envelope — that's a different
// contract (request_id dedup, typed SendResult with status codes)
// served by transport.ts's `send()` function. Keep the two separate.
// ---------------------------------------------------------------------------

const JSON_HEADERS = { "Content-Type": "application/json" };

export const API_TIMEOUT_MS = 30_000;

/** Compose an optional caller signal with a fresh timeout signal. Uses
 *  native AbortSignal.any() + AbortSignal.timeout() — both are baseline
 *  in every browser we ship to (Safari 17.4+, Chrome 124+, Firefox 124+).
 *  https://developer.mozilla.org/en-US/docs/Web/API/AbortSignal/any_static
 *  https://developer.mozilla.org/en-US/docs/Web/API/AbortSignal/timeout_static */
export function withTimeout(signal: AbortSignal | undefined, ms: number): AbortSignal {
  return signal !== undefined
    ? AbortSignal.any([signal, AbortSignal.timeout(ms)])
    : AbortSignal.timeout(ms);
}

/** Internal fetch wrapper shared by apiGet/apiPost/apiPut/apiPatch/apiDelete.
 *  Applies a timeout signal (via withTimeout), logs failures centrally,
 *  and returns null on any non-2xx or network error — callers never see
 *  exceptions. DELETE and 204 responses return an empty object cast to T
 *  (truthy marker) since there's no body to parse. */
async function request<T>(
  method: string, path: string, body?: unknown, signal?: AbortSignal,
): Promise<T | null> {
  try {
    const init: RequestInit = { method };
    if (body !== undefined) {
      init.headers = JSON_HEADERS;
      init.body = JSON.stringify(body);
    }
    init.signal = withTimeout(signal, API_TIMEOUT_MS);
    const r = await fetch(path, init);
    if (!r.ok) {
      console.warn("api: non-ok", method, path, r.status);
      return null;
    }
    // No body (DELETE or empty 204): return a truthy marker cast to T.
    if (method === "DELETE" || r.status === 204) {
      return {} as T;
    }
    return (await r.json()) as T;
  } catch (e) {
    console.warn("api: fetch failed", method, path, e);
    return null;
  }
}

/** GET `path` and return parsed JSON, or null on failure. */
export function apiGet<T>(path: string, signal?: AbortSignal): Promise<T | null> {
  return request<T>("GET", path, undefined, signal);
}

/** POST `body` as JSON to `path`, return parsed JSON response or null. */
export function apiPost<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T | null> {
  return request<T>("POST", path, body, signal);
}

/** PUT `body` as JSON to `path`, return parsed JSON response or null. */
export function apiPut<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T | null> {
  return request<T>("PUT", path, body, signal);
}

/** PATCH `body` as JSON to `path`, return parsed JSON response or null. */
export function apiPatch<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T | null> {
  return request<T>("PATCH", path, body, signal);
}

/** DELETE `path`. Returns true on success, false on failure. */
export async function apiDelete(path: string, signal?: AbortSignal): Promise<boolean> {
  const result = await request<object>("DELETE", path, undefined, signal);
  return result !== null;
}

/** Result shape for apiPostOrError / apiPutOrError: on 2xx `ok` is true
 *  and `data` is the parsed body; on 4xx/5xx `ok` is false, `status`
 *  is the HTTP status, and `error` is the parsed "error" field from
 *  the server's JSON body (empty string if the body didn't include one).
 *  Used by forms that need to surface specific failure reasons (400
 *  validation errors, 409 conflicts) inline instead of silently failing. */
export interface ApiResult<T> {
  ok: boolean;
  status: number;
  data: T | null;
  error: string;
}

// --- Typed decoder hook ---
//
// Re-export the Decoder<T> type from validators.ts so callers don't
// need a second import. The typed helpers below run an optional
// decoder on the parsed JSON before returning; on decoder throw, the
// error is logged with the structured path and the helper returns
// null (`apiGetTyped`/`apiPostTyped`) or an error envelope
// (`apiGetTypedRaw`).
//
// Pattern ported from apps/subflux/internal/server/static-src/api-client.ts.

export type { Decoder } from "./validators.js";
import type { Decoder } from "./validators.js";

/** Fetch + decode variant: runs the response through a Decoder<T>
 *  after parsing JSON. Returns a full ApiResult envelope so callers
 *  can distinguish network errors (status 0), HTTP errors (4xx/5xx),
 *  and decoder failures (shape mismatch) without try/catch. Used by
 *  apiGetTyped / apiPostTyped for type-safe API consumption. */
async function requestTyped<T>(
  method: string, path: string, decoder: Decoder<T>, body?: unknown, signal?: AbortSignal,
): Promise<ApiResult<T>> {
  try {
    const init: RequestInit = { method };
    if (body !== undefined) {
      init.headers = JSON_HEADERS;
      init.body = JSON.stringify(body);
    }
    init.signal = withTimeout(signal, API_TIMEOUT_MS);
    const r = await fetch(path, init);
    if (!r.ok) {
      console.warn("api: non-ok", method, path, r.status);
      return { ok: false, status: r.status, data: null, error: `HTTP ${String(r.status)}` };
    }
    if (method === "DELETE" || r.status === 204) {
      return { ok: true, status: r.status, data: null, error: "" };
    }
    const parsed: unknown = await r.json();
    try {
      return { ok: true, status: r.status, data: decoder(parsed), error: "" };
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      console.error("api: decode failed:", method, path, msg);
      return { ok: false, status: r.status, data: null, error: `response shape mismatch: ${msg}` };
    }
  } catch (e) {
    console.warn("api: fetch failed", method, path, e);
    return {
      ok: false, status: 0, data: null,
      error: e instanceof Error ? e.message : "network error",
    };
  }
}

/** GET `path`, validate the response with `decoder`, return the typed
 *  value or null on non-2xx, network error, or decoder failure.
 *  Failures are logged centrally; callers that need the error message
 *  (e.g. to show the user "response shape mismatch") should use
 *  `apiGetTypedRaw`. */
export async function apiGetTyped<T>(path: string, decoder: Decoder<T>, signal?: AbortSignal): Promise<T | null> {
  const r = await requestTyped<T>("GET", path, decoder, undefined, signal);
  return r.data;
}

/** POST `body` as JSON, validate the response with `decoder`. See
 *  `apiGetTyped` for null/error semantics. */
export async function apiPostTyped<T>(path: string, body: unknown, decoder: Decoder<T>, signal?: AbortSignal): Promise<T | null> {
  const r = await requestTyped<T>("POST", path, decoder, body, signal);
  return r.data;
}

/** GET variant that surfaces the full ApiResult (status + error). Use
 *  when the caller must distinguish "decoder threw" from "server
 *  returned 4xx" or needs the status code. */
export function apiGetTypedRaw<T>(path: string, decoder: Decoder<T>, signal?: AbortSignal): Promise<ApiResult<T>> {
  return requestTyped<T>("GET", path, decoder, undefined, signal);
}

/** Fetch variant that extracts the server's `error` field from non-2xx
 *  JSON responses. Used by apiPostOrError / apiPutOrError for forms
 *  that need to surface specific server validation messages (400, 409)
 *  inline rather than silently returning null. */
async function requestWithError<T>(
  method: string, path: string, body: unknown, signal?: AbortSignal,
): Promise<ApiResult<T>> {
  try {
    const r = await fetch(path, {
      method,
      headers: JSON_HEADERS,
      body: JSON.stringify(body),
      signal: withTimeout(signal, API_TIMEOUT_MS),
    });
    const raw = await r.text();
    let parsed: unknown = null;
    if (raw !== "") {
      try { parsed = JSON.parse(raw); } catch { /* non-JSON body */ }
    }
    if (!r.ok) {
      const err = (typeof parsed === "object" && parsed !== null
        && "error" in parsed && typeof (parsed as { error: unknown }).error === "string")
        ? (parsed as { error: string }).error
        : `HTTP ${String(r.status)}`;
      console.warn("api: non-ok", method, path, r.status, err);
      return { ok: false, status: r.status, data: null, error: err };
    }
    return { ok: true, status: r.status, data: parsed as T, error: "" };
  } catch (e) {
    console.warn("api: fetch failed", method, path, e);
    return {
      ok: false, status: 0, data: null,
      error: e instanceof Error ? e.message : "network error",
    };
  }
}

/** POST variant that surfaces error details. Use when the UI must show
 *  the server's validation message; otherwise prefer apiPost. */
export function apiPostOrError<T>(path: string, body: unknown, signal?: AbortSignal): Promise<ApiResult<T>> {
  return requestWithError<T>("POST", path, body, signal);
}

/** PUT variant that surfaces error details. */
export function apiPutOrError<T>(path: string, body: unknown, signal?: AbortSignal): Promise<ApiResult<T>> {
  return requestWithError<T>("PUT", path, body, signal);
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
  abort(): void { this.ctrl?.abort(); this.ctrl = null; }
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
  if (raw === "") return fallback;
  const parsed = parse(raw);
  return parsed !== null ? parsed : fallback;
}