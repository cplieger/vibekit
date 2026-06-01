// Actions for push notification lifecycle.
// ---------------------------------------------------------------------------

import { defineAction, ActionError, apiAction, retryNetwork } from "./index.js";
import { apiGet, apiPost } from "../api-client.js";
import { urlBase64ToUint8Array } from "../push-util.js";

const API_PUSH_SUBSCRIBE = "/api/push/subscribe";
const API_PUSH_UNSUBSCRIBE = "/api/push/unsubscribe";
const API_PUSH_VAPID_KEY = "/api/push/vapid-key";

/** Fire-and-forget unsubscribe from push notifications. No toast, no
 *  retry — best-effort cleanup when the user disables notifications. */
export const unsubscribePush = apiAction<{ endpoint: string }>({
  name: "notify.unsubscribe_push",
  request: ({ endpoint }) => ({
    method: "POST",
    path: API_PUSH_UNSUBSCRIBE,
    body: { endpoint },
  }),
  error: false,
  success: false,
});

/**
 * notify.register_push — wraps the full push registration flow:
 * SW register → VAPID key fetch → pushManager.subscribe → POST subscribe.
 *
 * Dispatched when the user explicitly toggles notifications on.
 * Rollback: unchecks the toggle so the UI reflects reality on failure.
 */
// eslint-disable-next-line @typescript-eslint/no-invalid-void-type -- void used as generic type argument for action with no args/result
export const registerPush = defineAction<void, ServiceWorkerRegistration>({
  name: "notify.register_push",
  retryable: retryNetwork,
  run: async (_args, signal) => {
    if (!("serviceWorker" in navigator)) {
      throw new ActionError("Service workers not supported", { code: "unsupported" });
    }
    const reg = await navigator.serviceWorker.register("/sw.js");
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }

    const keyData = await apiGet<{ publicKey: string }>(API_PUSH_VAPID_KEY, signal);
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive: signal can abort during await
    if (signal.aborted) {
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    if (keyData === null) {
      throw new ActionError("Could not fetch VAPID key", { code: "network" });
    }

    const appServerKey = urlBase64ToUint8Array(keyData.publicKey);
    const sub = await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: appServerKey as BufferSource,
    });
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive: signal can abort during await
    if (signal.aborted) {
      try {
        await sub.unsubscribe();
      } catch {
        /* best-effort */
      }
      throw new ActionError("cancelled", { code: "cancelled" });
    }

    let posted: unknown;
    try {
      posted = await apiPost(API_PUSH_SUBSCRIBE, sub.toJSON(), signal);
    } catch (e) {
      try {
        await sub.unsubscribe();
      } catch {
        /* best-effort */
      }
      throw e;
    }
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive: signal can abort during await
    if (signal.aborted) {
      try {
        await sub.unsubscribe();
      } catch {
        /* best-effort */
      }
      // Best-effort server-side cleanup after successful POST but cancelled action.
      void apiPost(API_PUSH_UNSUBSCRIBE, {});
      throw new ActionError("cancelled", { code: "cancelled" });
    }
    if (posted === null) {
      try {
        await sub.unsubscribe();
      } catch {
        /* best-effort */
      }
      throw new ActionError("Server rejected subscription", { code: "server_rejected" });
    }

    return reg;
  },
  rollback: () => {
    // Emit a custom event so the UI layer can handle the visual rollback
    // without coupling this action to a specific DOM element ID.
    document.dispatchEvent(new CustomEvent("notify:registration-failed"));
  },
  error: "Couldn't enable push notifications",
});
