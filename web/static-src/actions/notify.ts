// Actions for push notification lifecycle.
// ---------------------------------------------------------------------------

import { defineAction, ActionError } from "./index.js";
import { apiGet, apiPost } from "../api-client.js";
import { urlBase64ToUint8Array } from "../notify.js";
import { $ } from "../dom.js";

/**
 * notify.register_push — wraps the full push registration flow:
 * SW register → VAPID key fetch → pushManager.subscribe → POST subscribe.
 *
 * Dispatched when the user explicitly toggles notifications on.
 * Rollback: unchecks the toggle so the UI reflects reality on failure.
 */
export const registerPushAction = defineAction<void, ServiceWorkerRegistration>({
  name: "notify.register_push",
  run: async (_args, signal) => {
    if (!("serviceWorker" in navigator)) {
      throw new ActionError("Service workers not supported");
    }
    const reg = await navigator.serviceWorker.register("/sw.js");
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });

    const keyData = await apiGet<{ publicKey: string }>("/api/push/vapid-key", signal);
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    if (keyData === null) throw new ActionError("Could not fetch VAPID key");

    const appServerKey = urlBase64ToUint8Array(keyData.publicKey);
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: appServerKey.buffer as ArrayBuffer,
    });
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });

    const posted = await apiPost("/api/push/subscribe", sub.toJSON(), signal);
    if (signal.aborted) throw new ActionError("cancelled", { code: "cancelled" });
    if (posted === null) throw new ActionError("Server rejected subscription");

    return reg;
  },
  rollback: () => {
    // Uncheck the toggle so the visual state reflects the failed registration.
    $.notifyToggle.checked = false;
  },
  error: "Couldn't enable push notifications",
});
