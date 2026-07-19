// ---------------------------------------------------------------------------
// Service worker for Web Push notifications + PWA installability.
// Handles push events, notification clicks, subscription recovery, and a
// minimal fetch handler (the presence of an active fetch handler is part of
// the browser's PWA install criteria).
// Compiled to static/sw.js by tsconfig.sw.json.
// ---------------------------------------------------------------------------

// eslint-disable-next-line @typescript-eslint/triple-slash-reference
/// <reference path="sw-env.d.ts" />

const sw = self as unknown as ServiceWorkerGlobalScope;

// Minimal fetch handler. Its presence (an active SW with a fetch handler) is
// what satisfies the PWA install criterion; without it browsers won't offer
// installation. We only pass navigations straight through to the network and
// leave every other request (assets, /api/*, SSE, the shell WebSocket) to the
// browser's default handling. Deliberately NO caching — the app is served
// fresh on every load (HTTP `Cache-Control: no-cache` + revalidation owns
// freshness), so a deploy is never masked by a stale precache.
sw.addEventListener("fetch", ((event: FetchEvent) => {
  if (event.request.mode === "navigate") {
    event.respondWith(fetch(event.request));
  }
}) as EventListener);

sw.addEventListener("push", ((event: PushEvent) => {
  if (event.data === null) {
    return;
  }
  let data: { title?: string; body?: string };
  try {
    data = event.data.json() as { title?: string; body?: string };
  } catch {
    data = { title: "Vibekit", body: event.data.text() };
  }
  const title = data.title ?? "Vibekit";
  event.waitUntil(
    sw.registration
      .showNotification(title, {
        body: data.body ?? "",
        icon: "/favicon.svg",
        badge: "/icon-192.png",
        tag: "vibekit",
      })
      .catch((err: unknown) => {
        console.error("sw: showNotification failed", err);
      }),
  );
}) as EventListener);

sw.addEventListener("notificationclick", ((event: NotificationEvent) => {
  event.notification.close();
  event.waitUntil(
    sw.clients
      .matchAll({ type: "window", includeUncontrolled: true })
      .then((list: readonly Client[]) => {
        for (const client of list) {
          if ("focus" in client) {
            return (client as WindowClient).focus();
          }
        }
        return sw.clients.openWindow("/");
      }),
  );
}) as EventListener);

sw.addEventListener("pushsubscriptionchange", ((event: PushSubscriptionChangeEvent) => {
  const old = event.oldSubscription;
  event.waitUntil(
    resolveSubscribeOptions(old)
      .then((opts) => sw.registration.pushManager.subscribe(opts))
      .then((newSub: PushSubscription) =>
        fetch("/api/push/subscribe", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(newSub.toJSON()),
        }),
      )
      .catch((err: unknown) => {
        console.error("sw: re-subscribe failed", err);
      }),
  );
}) as EventListener);

/** Subscription options for a pushsubscriptionchange recovery. When the
 *  browser supplies the old subscription, reuse its options verbatim.
 *  When it does NOT (the exact case this event exists for — an expired
 *  subscription can arrive with oldSubscription === null), a bare
 *  `{userVisibleOnly:true}` subscribe fails on VAPID-enforcing push
 *  services, so recovery previously broke precisely when it was needed:
 *  fetch the server's VAPID public key and subscribe with it. */
async function resolveSubscribeOptions(
  old: PushSubscription | null,
): Promise<PushSubscriptionOptionsInit> {
  if (old !== null) {
    return old.options;
  }
  const r = await fetch("/api/push/vapid-key");
  if (!r.ok) {
    throw new Error(`vapid-key fetch failed: HTTP ${String(r.status)}`);
  }
  const d = (await r.json()) as { publicKey?: string };
  if (typeof d.publicKey !== "string" || d.publicKey === "") {
    throw new Error("no VAPID public key available for re-subscribe");
  }
  return { userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(d.publicKey) };
}

/** Base64url → Uint8Array for the VAPID applicationServerKey. Local copy
 *  of push-util.ts's helper: the service worker compiles standalone
 *  (tsconfig.sw.json includes only sw.ts) and registers as a classic
 *  script, so it cannot import modules. */
function urlBase64ToUint8Array(base64String: string): Uint8Array<ArrayBuffer> {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  // Explicit ArrayBuffer backing so the result satisfies BufferSource
  // under TS's generic TypedArray types (applicationServerKey rejects
  // Uint8Array<ArrayBufferLike>).
  const arr = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) {
    arr[i] = raw.charCodeAt(i);
  }
  return arr;
}
