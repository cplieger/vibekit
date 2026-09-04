// ---------------------------------------------------------------------------
// Service worker for Web Push notifications, PWA installability, and the shell
// precache. Handles push events, notification clicks, subscription recovery, and
// a fetch handler that serves the build's lazy chunks cache-first (see "The shell
// precache" below).
// Compiled to static/sw.js by tsconfig.sw.json.
// ---------------------------------------------------------------------------

// eslint-disable-next-line @typescript-eslint/triple-slash-reference
/// <reference path="sw-env.d.ts" />

import { type PrecacheManifest, isShellPath, parseManifest } from "./precache.js";

const sw = self as unknown as ServiceWorkerGlobalScope;

// ---------------------------------------------------------------------------
// The shell precache: the content-hashed lazy chunks, never a stable name and
// never the HTML (precache.ts's `isShellPath` owns that rule and the reason).
//
// A resume used to spend ~50 revalidation round trips before first paint, every one
// a 304 — the bytes were already local and the app waited for the network to say so.
// ---------------------------------------------------------------------------

/** Cache holding the precached chunks. One name, and the manifest is stored INSIDE
 *  it under its own path, so the stamp travels with the assets it describes.
 *
 *  A DEPLOY IS NEVER MASKED: index.html stays `no-store`, so every load fetches
 *  the current HTML, therefore the current app.js and style.css (both left to the
 *  network), therefore the current chunk names. `syncPrecache` runs off every
 *  navigation rather than off `install`, because a deploy that leaves sw.js
 *  byte-identical fires no `install` at all. */
const SHELL_CACHE = "vibekit-shell";

/** Where the build's asset list lives (cmd/bundle writes it). */
const PRECACHE_URL = "/precache.json";

/** Read the manifest a document just served, or null when it is unusable.
 *
 *  `no-store` so this one request is never the thing serving a stale answer;
 *  `parseManifest` owns what counts as usable. */
async function fetchManifest(): Promise<PrecacheManifest | null> {
  try {
    const r = await fetch(PRECACHE_URL, { cache: "no-store" });
    if (!r.ok) {
      return null;
    }
    return parseManifest(await r.json());
  } catch {
    // Offline. The cache already holds whatever the last successful sync put
    // there, which is the state this whole mechanism exists to serve.
    return null;
  }
}

/** The sync in flight, if any. */
let syncing: Promise<boolean> | null = null;

/** Bring the cache in line with the current build, and report whether it moved.
 *
 *  ONE SYNC AT A TIME, because two straddling a deploy corrupt each other: A reads
 *  manifest X, B fills and stamps Y, then A's prune — its `wanted` built from X —
 *  deletes Y's content-hashed chunks. The cache then holds stamp Y with Y's assets
 *  missing, and `fillPrecache`'s stamp-equality early return blocks the repair
 *  until the next deploy moves the stamp again. A second caller joins the first
 *  and takes its answer, which is the answer for the manifest state it arrived in. */
function syncPrecache(): Promise<boolean> {
  if (syncing !== null) {
    return syncing;
  }
  const run = fillPrecache();
  syncing = run.finally(() => {
    syncing = null;
  });
  return syncing;
}

/** One sync pass. Call `syncPrecache`, never this.
 *
 *  ORDER IS LOAD-BEARING: fill first, then record the stamp, then prune. A stamp
 *  written before its assets are in would make a crashed sync look complete
 *  forever, and pruning before the fill would blank the cache for a document
 *  loading right now. */
async function fillPrecache(): Promise<boolean> {
  const next = await fetchManifest();
  if (next === null) {
    return false;
  }
  const cache = await caches.open(SHELL_CACHE);
  const held = await cache.match(PRECACHE_URL);
  if (held !== undefined) {
    const heldDoc = parseManifest(await held.json().catch(() => null));
    if (heldDoc?.stamp === next.stamp) {
      return false;
    }
  }
  await cache.addAll([...next.assets]);
  await cache.put(PRECACHE_URL, new Response(JSON.stringify(next)));
  const wanted = new Set([...next.assets, PRECACHE_URL]);
  for (const req of await cache.keys()) {
    if (!wanted.has(new URL(req.url).pathname)) {
      await cache.delete(req);
    }
  }
  return true;
}

sw.addEventListener("install", ((event: ExtendableEvent) => {
  // NOT gated on success: a manifest the build did not write, or a network that
  // is down at install time, must still leave a working worker. The next
  // navigation syncs.
  event.waitUntil(
    syncPrecache().catch((err: unknown) => {
      console.warn("sw: precache install failed", err);
      return false;
    }),
  );
}) as EventListener);

sw.addEventListener("activate", ((event: ExtendableEvent) => {
  event.waitUntil(
    (async () => {
      // Any cache from an earlier naming scheme. Nothing else in this origin's
      // storage is this worker's.
      for (const name of await caches.keys()) {
        if (name !== SHELL_CACHE && name.startsWith("vibekit-")) {
          await caches.delete(name);
        }
      }
      // No skipWaiting anywhere, so this only runs once the previous worker's
      // clients are gone — claiming here therefore cannot hand a document a
      // graph its HTML did not ask for. What it does buy is the FIRST install:
      // the page that registered the worker becomes controlled without a reload,
      // and it loaded the very deployment this cache was built from.
      await sw.clients.claim();
    })(),
  );
}) as EventListener);

// Three arms, and the handler's mere presence is also what satisfies the PWA install
// criterion. A NAVIGATION goes to the network — the shell must stay fresh — and
// doubles as the deploy check. A CONTENT-HASHED CHUNK is answered from the cache.
// Everything else, `/api/*` reads and the `/api/events` stream included, is never
// handed to `respondWith` at all: `isShellPath` is a synchronous gate for exactly
// that reason, because a handler that asks the cache first has already taken the
// request over.
sw.addEventListener("fetch", ((event: FetchEvent) => {
  if (event.request.mode === "navigate") {
    event.respondWith(fetch(event.request));
    event.waitUntil(
      syncPrecache().catch((err: unknown) => {
        console.warn("sw: precache sync failed", err);
        return false;
      }),
    );
    return;
  }
  if (event.request.method !== "GET") {
    return;
  }
  const url = new URL(event.request.url);
  if (url.origin !== location.origin || !isShellPath(url.pathname)) {
    return;
  }
  event.respondWith(
    (async () => {
      const cache = await caches.open(SHELL_CACHE);
      const hit = await cache.match(event.request);
      return hit ?? (await fetch(event.request));
    })(),
  );
}) as EventListener);

/** The push payload vibekit's server sends (internal/push/send.go pushPayload,
 *  whose subject fields come from vibekit.PushSubject).
 *
 *  EXACTLY ONE of the two subject fields is set, and both may be absent for a
 *  workspace-global notification. `chat_id` names the chat a notification belongs
 *  to; `subject` names one that has no chat behind it — a pull request, whose CI
 *  flip happens with nothing open — and carries a kind prefix rather than a URL,
 *  because the page owns the route vocabulary. */
interface PushData {
  title?: string;
  body?: string;
  chat_id?: string;
  subject?: string;
}

/** Message this worker posts to an open page. The page owns the route
 *  vocabulary (router.ts), so we hand over the SUBJECT and let it navigate;
 *  `reason` says whether the user asked to go there or is merely being told. */
interface PushPageMessage {
  type: "push";
  reason: "clicked" | "arrived";
  chatId: string;
  subject: string;
  title: string;
  body: string;
}

/** Subject-key prefix for a pull request (vibekit.PRSubjectPrefix). The two halves of
 *  this contract are in different languages, so the constant is spelled once on
 *  each side and asserted against the Go one by a test. */
const PR_SUBJECT_PREFIX = "pr:";

/** Where to open when NO page is up at all — the one path that needs a URL.
 *  A focus lands on an existing page, which routes itself from the posted subject.
 *  These two literals are spelled here rather than imported from router.ts because
 *  that module is DOM-bound — it registers a `popstate` listener on `window` at
 *  module scope and drives `history` — and this file builds under
 *  `lib: [ESNext, WebWorker]` (tsconfig.sw.json). */
function subjectPath(data: { chatId: string; subject: string }): string {
  if (data.subject.startsWith(PR_SUBJECT_PREFIX)) {
    return "/git";
  }
  return data.chatId === "" ? "/" : `/chat/${encodeURIComponent(data.chatId)}`;
}

/** OS coalescing tag. One tray slot per SUBJECT, so a permission ask on one
 *  chat can no longer replace the finished note on another: a same-tag
 *  notification CLOSES and replaces the one on screen, and does so without a
 *  sound, so the constant tag this replaces lost the earlier notification
 *  silently. Per chat rather than per kind on purpose — within a turn the ask
 *  comes first and the finished note last, so the later one superseding the
 *  earlier is the tray telling the truth.
 *
 *  A subject key takes the same treatment for the same reason: a PR's key is
 *  unique per pull request, so two PRs flipping inside one window keep their own
 *  slots instead of overwriting each other. */
function subjectTag(data: { chatId: string; subject: string }): string {
  if (data.subject !== "") {
    return `vibekit:${data.subject}`;
  }
  return data.chatId === "" ? "vibekit" : `vibekit:${data.chatId}`;
}

/** Read one string field off a notification's `data` bag. The bag is whatever the
 *  browser round-tripped, so every field is checked rather than cast; an absent or
 *  wrong-typed field reads as "", which every consumer treats as unset. */
function readStringField(raw: unknown, field: string): string {
  if (typeof raw !== "object" || raw === null) {
    return "";
  }
  const v: unknown = (raw as Record<string, unknown>)[field];
  return typeof v === "string" ? v : "";
}

/** Window clients for this origin, newest API shape first. */
async function windowClients(): Promise<WindowClient[]> {
  const list = await sw.clients.matchAll({ type: "window", includeUncontrolled: true });
  return list.filter((c): c is WindowClient => "focus" in c);
}

sw.addEventListener("push", ((event: PushEvent) => {
  if (event.data === null) {
    return;
  }
  let data: PushData;
  try {
    data = event.data.json() as PushData;
  } catch {
    data = { title: "Vibekit", body: event.data.text() };
  }
  const title = data.title ?? "Vibekit";
  const body = data.body ?? "";
  const chatID = data.chat_id ?? "";
  const subject = data.subject ?? "";

  event.waitUntil(
    (async () => {
      // A focused page gets a message instead of a tray banner. This is the
      // one sanctioned exception to "every push must show a notification"
      // (Chrome enforces userVisibleOnly and will otherwise substitute its own
      // generic "site updated in background" notice), and it is what makes an
      // in-app toast the right surface when the user is already looking.
      const clients = await windowClients();
      if (clients.some((c) => c.focused)) {
        for (const c of clients) {
          c.postMessage({
            type: "push",
            reason: "arrived",
            chatId: chatID,
            subject,
            title,
            body,
          } satisfies PushPageMessage);
        }
        return;
      }
      try {
        await sw.registration.showNotification(title, {
          body,
          icon: "/favicon.svg",
          badge: "/icon-192.png",
          tag: subjectTag({ chatId: chatID, subject }),
          // Re-alert on a replacement. A same-tag replacement is silent by
          // default, and here a replacement always means the chat moved to
          // something else worth a glance.
          renotify: true,
          // Read back in notificationclick; the only place the target lives.
          data: { chatId: chatID, subject },
        });
      } catch (err: unknown) {
        console.error("sw: showNotification failed", err);
      }
    })(),
  );
}) as EventListener);

sw.addEventListener("notificationclick", ((event: NotificationEvent) => {
  event.notification.close();
  const raw: unknown = event.notification.data;
  const chatID = readStringField(raw, "chatId");
  const subject = readStringField(raw, "subject");

  event.waitUntil(
    (async () => {
      // Focus an existing page and hand it the target, rather than matching on
      // exact URL equality. vibekit is a single page with a router, so a client
      // sitting on /settings does not equal /chat/<id> and the documented
      // exact-match pattern would open a SECOND window of the same app. Posting
      // the id also beats WindowClient.navigate(), which is only legal for
      // clients this worker controls — precisely the ones includeUncontrolled
      // was set to include.
      const clients = await windowClients();
      if (clients.length > 0) {
        const target = clients.find((c) => c.focused) ?? clients[0];
        if (target !== undefined) {
          target.postMessage({
            type: "push",
            reason: "clicked",
            chatId: chatID,
            subject,
            title: event.notification.title,
            body: event.notification.body,
          } satisfies PushPageMessage);
          await target.focus();
          return;
        }
      }
      // No page open at all: this is the one path that needs a URL.
      await sw.clients.openWindow(subjectPath({ chatId: chatID, subject }));
    })(),
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

/** Base64url → Uint8Array for the VAPID applicationServerKey. Near-copy of
 *  push-util.ts's helper, and the difference is the reason it is one: that
 *  version's return type leaves the buffer backing implicit, which
 *  `applicationServerKey` rejects (see below). */
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
