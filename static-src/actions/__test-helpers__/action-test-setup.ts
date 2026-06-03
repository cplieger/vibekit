/**
 * Shared action-test setup. Provides resetActionFramework() and mock factories.
 * Backed by @cplieger/actions internal modules.
 *
 * NOTE: vi.mock() calls must remain at the top of each test file (Vitest hoisting).
 * Use the exported factory functions as the mock implementation argument.
 */
import { vi } from "vitest";
import { configure, configureTransport } from "@cplieger/actions";
import type { TransportSendResult } from "@cplieger/actions";
import { error as toastError, success as toastSuccess } from "../../toast.js";
import { send as transportSend } from "../../transport.js";

// Deep imports into @cplieger/actions internals for test reset.
// These are not part of the public API surface but are stable test utilities
// that work at runtime (Vitest resolves them). TS rejects them due to the
// package "exports" field — suppress the module-not-found errors.
// @ts-expect-error — deep import for test reset (not public API)
import { _resetForTest as resetDefine } from "@cplieger/actions/dist/src/define.js";
// @ts-expect-error — deep import for test reset (not public API)
import { _resetForTest as resetRegistry } from "@cplieger/actions/dist/src/registry.js";
// @ts-expect-error — deep import for test reset (not public API)
import { _resetForTest as resetCleanup } from "@cplieger/actions/dist/src/cleanup.js";
// @ts-expect-error — deep import for test reset (not public API)
import { _resetApiConfigForTest as resetApiConfig } from "@cplieger/actions/dist/src/api.js";
// @ts-expect-error — deep import for test reset (not public API)
import { _resetTransportForTest as resetTransport } from "@cplieger/actions/dist/src/transport.js";

export { resetDefine, resetRegistry, resetCleanup };

/** Resets define, registry, cleanup, API config, and transport.
 *  Also wires the notifier and transport through to their respective
 *  modules (which tests mock via vi.mock). */
export function resetActionFramework(): void {
  resetDefine();
  resetRegistry();
  resetCleanup();
  resetApiConfig();
  resetTransport();
  // Wire the library's notifier to toast.js (mocked by tests).
  configure({
    success: (msg) => { toastSuccess(msg); },
    error: (msg, retry) => { toastError(msg, retry); },
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

/** Canonical toast mock factory for vi.mock("../toast.js", mockToast) */
export const mockToast = () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
});
