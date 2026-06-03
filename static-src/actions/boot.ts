// Boot wiring for @cplieger/actions: connects the library's injection
// points to vibekit's toast, api-client, and transport layers.
// Import this module once at app startup (app.ts) before any action dispatch.
// ---------------------------------------------------------------------------

import { configure, configureTransport } from "@cplieger/actions";
import { error as toastError, success as toastSuccess } from "../toast.js";
import { send as transportSend } from "../transport.js";

export function initActions(): void {
  configure({
    success: (msg) => {
      toastSuccess(msg);
    },
    error: (msg, retry) => {
      toastError(msg, retry);
    },
  });

  configureTransport(async (cmd, { signal }) => {
    const r = await transportSend(cmd as Parameters<typeof transportSend>[0], {
      signal,
      reportSendState: false,
    });
    return r;
  });
}
