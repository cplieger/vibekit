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
}

interface WindowClient extends Client {
  focus(): Promise<WindowClient>;
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

interface ExtendableEvent extends Event {
  waitUntil(promise: Promise<unknown>): void;
}
