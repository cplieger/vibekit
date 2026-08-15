// Minimal ServiceWorkerGlobalScope type declarations.
// Compatible with both DOM and WebWorker lib contexts.

interface ServiceWorkerGlobalScope {
  readonly registration: ServiceWorkerRegistration;
  readonly clients: Clients;
  addEventListener(type: string, listener: EventListenerOrEventListenerObject): void;
}

interface Clients {
  matchAll(options?: { type?: string; includeUncontrolled?: boolean }): Promise<readonly Client[]>;
  openWindow(url: string): Promise<WindowClient | null>;
}

interface Client {
  readonly url: string;
  // The worker's only channel to a live page. It carries a notification's
  // target chat id so the PAGE builds the route (router.ts owns the route
  // vocabulary; this file's script cannot import it).
  postMessage(message: unknown): void;
}

interface WindowClient extends Client {
  focus(): Promise<WindowClient>;
  // Whether this window has user focus. Load-bearing: a focused page gets a
  // posted message and no OS notification, which is the single sanctioned
  // exception to userVisibleOnly.
  readonly focused: boolean;
}

// `renotify` is absent from the bundled DOM lib's NotificationOptions but is
// implemented and required here: a same-tag notification replaces the one on
// screen SILENTLY, so without this a second event supersedes the first with no
// alert at all. Declaration merging rather than a cast, so the option is
// type-checked like any other.
interface NotificationOptions {
  renotify?: boolean;
}

interface PushEvent extends ExtendableEvent {
  readonly data: PushMessageData | null;
}

interface PushMessageData {
  json(): unknown;
  text(): string;
}

interface NotificationEvent extends ExtendableEvent {
  readonly notification: Notification;
}

interface PushSubscriptionChangeEvent extends ExtendableEvent {
  readonly oldSubscription: PushSubscription | null;
}

interface FetchEvent extends ExtendableEvent {
  readonly request: Request;
  respondWith(response: Response | Promise<Response>): void;
}

interface ExtendableEvent extends Event {
  waitUntil(promise: Promise<unknown>): void;
}
