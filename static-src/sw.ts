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
    sw.registration.pushManager
      .subscribe(old !== null ? old.options : { userVisibleOnly: true })
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
