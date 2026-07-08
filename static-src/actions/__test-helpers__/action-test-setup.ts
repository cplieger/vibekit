/**
 * Shared action-test setup. Provides resetActionFramework() and mock factories.
 * Backed by @cplieger/actions internal modules.
 *
 * NOTE: vi.mock() calls must remain at the top of each test file (Vitest hoisting).
 * Use the exported factory functions as the mock implementation argument.
 */
import { configure, configureTransport } from "@cplieger/actions";
import { resetActionFramework as resetFramework } from "@cplieger/actions/testing";
import type { TransportSendResult } from "@cplieger/actions";
import { error as toastError, success as toastSuccess } from "../../toast.js";
import { send as transportSend } from "../../transport.js";

/** Resets define, registry, cleanup, API config, and transport.
 *  Also wires the notifier and transport through to their respective
 *  modules (which tests mock via vi.mock). */
export function resetActionFramework(): void {
  resetFramework();
  // Wire the library's notifier to toast.js (mocked by tests).
  configure({
    success: (msg) => {
      toastSuccess(msg);
    },
    error: (msg, retry) => {
      toastError(msg, retry);
    },
  });
  // Wire the library's transport to transport.js send (mocked by tests).
  configureTransport(async (cmd, { signal }) => {
    const r = await transportSend(cmd as Parameters<typeof transportSend>[0], {
      signal,
      reportSendState: false,
    });
    return r as TransportSendResult;
  });
}

/**
 * Read a header value from a mocked `fetch` call's RequestInit, regardless of
 * how the headers were supplied. Since actions 2.0.7 routes `apiAction` through
 * `@cplieger/fetch`, the request core always hands the underlying `fetch` a
 * `Headers` instance (not the plain lowercase-keyed object the pre-2.0.7 core
 * used), so a bracket lookup like `init.headers["idempotency-key"]` reads
 * `undefined`. This accessor handles a `Headers` instance, a plain record, or
 * an entries array, and is case-insensitive. Returns undefined when the header
 * (or the RequestInit) is absent.
 */
export function headerValue(init: RequestInit | undefined, name: string): string | undefined {
  const h = init?.headers;
  if (h === undefined) {
    return undefined;
  }
  if (h instanceof Headers) {
    return h.get(name) ?? undefined;
  }
  const lower = name.toLowerCase();
  const entries = Array.isArray(h) ? h : Object.entries(h);
  for (const [k, v] of entries) {
    if (k.toLowerCase() === lower) {
      return v;
    }
  }
  return undefined;
}
