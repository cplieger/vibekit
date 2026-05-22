// ---------------------------------------------------------------------------
// Notifications: browser Notification API (foreground tab) + Web Push
// (background/closed). Preferences are global (server-side settings).
// Each device auto-prompts for browser permission when enabled globally.
// ---------------------------------------------------------------------------

import { apiGet, apiPost } from "./api-client.js";
import { isIOS, isStandalone } from "./platform.js";

// ---------------------------------------------------------------------------
// NotifyController: owns all notification/push state as instance fields.
// ---------------------------------------------------------------------------

type PushState =
  | { kind: "idle" }
  | { kind: "permission_granted" }
  | { kind: "registering" }
  | { kind: "registered"; registration: ServiceWorkerRegistration }
  | { kind: "failed"; error: string };

class NotifyController {
  private swRegistration: ServiceWorkerRegistration | null = null;

  private enabled = false;
  private agentFinished = true;
  private permissionNeeded = true;

  private notifyUICallback: (() => void) | null = null;

  private pushState: PushState = { kind: "idle" };
  private pushController: AbortController | null = null;

  constructor() {
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        this.setBadge(0);
        document.documentElement.removeAttribute("data-tab-hidden");
      } else {
        document.documentElement.setAttribute("data-tab-hidden", "");
      }
    });

    if (document.visibilityState !== "visible") {
      document.documentElement.setAttribute("data-tab-hidden", "");
    }
  }

  // --- Preference accessors ---

  areNotificationsEnabled(): boolean { return this.enabled; }
  isAgentFinishedEnabled(): boolean { return this.agentFinished; }
  isPermissionNeededEnabled(): boolean { return this.permissionNeeded; }

  setNotificationsEnabled(v: boolean): void { this.enabled = v; }
  setAgentFinishedEnabled(v: boolean): void { this.agentFinished = v; }
  setPermissionNeededEnabled(v: boolean): void { this.permissionNeeded = v; }

  setNotifyUICallback(fn: () => void): void { this.notifyUICallback = fn; }

  // --- Restore from server settings ---

  restoreNotifications(s: {
    notifications_enabled?: boolean;
    notify_agent_finished?: boolean;
    notify_permission?: boolean;
  }): void {
    const wasEnabled = this.enabled;
    this.enabled = s.notifications_enabled === true;
    this.agentFinished = s.notify_agent_finished !== false;
    this.permissionNeeded = s.notify_permission !== false;

    if (this.enabled) {
      this.autoSubscribe();
    } else if (wasEnabled) {
      this.unregisterPush();
    }
    this.notifyUICallback?.();
  }

  // --- Permission + Push subscription ---

  requestPermission(): string | null {
    if (!("Notification" in window)) {
      if (isIOS && !isStandalone) {
        return "Add this app to your Home Screen first, then enable notifications.";
      }
      return "Notifications are not supported in this browser.";
    }
    if (Notification.permission === "granted") {
      void this.registerPush();
      return null;
    }
    if (Notification.permission === "denied") {
      return "Notifications were blocked. Allow them in your browser settings.";
    }
    Notification.requestPermission().then((result) => {
      if (result === "granted") void this.registerPush();
    }).catch(() => {});
    return null;
  }

  unregisterPush(): void {
    this.pushController?.abort();
    this.pushController = null;
    this.pushState = { kind: "idle" };
    if (this.swRegistration === null) return;
    const reg = this.swRegistration;
    this.swRegistration = null;
    reg.pushManager.getSubscription().then((sub) => {
      if (sub === null) return;
      const endpoint = sub.endpoint;
      sub.unsubscribe().catch(() => {});
      void apiPost("/api/push/unsubscribe", { endpoint });
    }).catch(() => {});
    reg.unregister().catch(() => {});
  }

  private autoSubscribe(): void {
    if (this.pushState.kind === "registered" || this.pushState.kind === "failed" || this.pushState.kind === "registering") return;
    if (!("Notification" in window)) return;
    if (Notification.permission === "granted") {
      void this.registerPush();
      return;
    }
    if (Notification.permission === "denied") return;
    Notification.requestPermission().then((result) => {
      if (result === "granted") void this.registerPush();
    }).catch(() => {});
  }

  private async registerPush(): Promise<void> {
    if (this.pushState.kind === "registered" || this.pushState.kind === "registering" || !("serviceWorker" in navigator)) return;
    this.pushController?.abort();
    const ctrl = new AbortController();
    this.pushController = ctrl;
    this.pushState = { kind: "registering" };
    try {
      this.swRegistration = await navigator.serviceWorker.register("/sw.js");
      if (ctrl.signal.aborted || ctrl !== this.pushController) return;
      const keyData = await apiGet<{ publicKey: string }>("/api/push/vapid-key", ctrl.signal);
      if (ctrl.signal.aborted || ctrl !== this.pushController) return;
      if (keyData === null) { this.pushState = { kind: "failed", error: "no VAPID key" }; return; }
      const appServerKey = urlBase64ToUint8Array(keyData.publicKey);
      const sub = await this.swRegistration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: appServerKey.buffer as ArrayBuffer,
      });
      if (ctrl.signal.aborted || ctrl !== this.pushController) return;
      await apiPost("/api/push/subscribe", sub.toJSON());
      if (ctrl.signal.aborted || ctrl !== this.pushController) return;
      this.pushState = { kind: "registered", registration: this.swRegistration };
    } catch (e: unknown) {
      if (ctrl.signal.aborted || ctrl !== this.pushController) return;
      const msg = e instanceof Error ? e.message : "unknown";
      this.pushState = { kind: "failed", error: msg };
      console.warn("push registration failed:", msg);
    }
  }

  // --- Local notifications ---

  notifyIfHidden(title: string, body: string): boolean {
    if (!this.enabled) return false;
    if (document.visibilityState !== "hidden") return false;
    if (!("Notification" in window) || Notification.permission !== "granted") return false;
    try {
      const n = new Notification(title, {
        body,
        icon: "/favicon.svg",
        tag: "vibekit",
      });
      n.addEventListener("click", () => { window.focus(); n.close(); });
      return true;
    } catch {
      return false;
    }
  }

  setBadge(count: number): void {
    const base = "Vibekit for Kiro";
    document.title = count > 0 ? `(${String(count)}) ${base}` : base;
  }
}

// ---------------------------------------------------------------------------
// Utility (stateless)
// ---------------------------------------------------------------------------

export function urlBase64ToUint8Array(base64String: string): Uint8Array {
  const padding = "=".repeat((4 - (base64String.length % 4)) % 4);
  const base64 = (base64String + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = atob(base64);
  const arr = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) arr[i] = raw.charCodeAt(i);
  return arr;
}

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const instance = new NotifyController();

export function areNotificationsEnabled(): boolean { return instance.areNotificationsEnabled(); }
export function isAgentFinishedEnabled(): boolean { return instance.isAgentFinishedEnabled(); }
export function isPermissionNeededEnabled(): boolean { return instance.isPermissionNeededEnabled(); }

export function setNotificationsEnabled(v: boolean): void { instance.setNotificationsEnabled(v); }
export function setAgentFinishedEnabled(v: boolean): void { instance.setAgentFinishedEnabled(v); }
export function setPermissionNeededEnabled(v: boolean): void { instance.setPermissionNeededEnabled(v); }

export function setNotifyUICallback(fn: () => void): void { instance.setNotifyUICallback(fn); }

export function restoreNotifications(s: {
  notifications_enabled?: boolean;
  notify_agent_finished?: boolean;
  notify_permission?: boolean;
}): void {
  instance.restoreNotifications(s);
}

export function requestPermission(): string | null {
  return instance.requestPermission();
}

export function unregisterPush(): void {
  instance.unregisterPush();
}

export function notifyIfHidden(title: string, body: string): boolean {
  return instance.notifyIfHidden(title, body);
}

export function setBadge(count: number): void {
  instance.setBadge(count);
}
