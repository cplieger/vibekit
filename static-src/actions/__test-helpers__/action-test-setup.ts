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
