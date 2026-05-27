// ---------------------------------------------------------------------------
// Service worker for Web Push notifications.
// Handles push events, notification clicks, and subscription recovery.
// Compiled to static/sw.js by tsconfig.sw.json.
// ---------------------------------------------------------------------------

/// <reference path="sw-env.d.ts" />

const sw = self as unknown as ServiceWorkerGlobalScope;

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
