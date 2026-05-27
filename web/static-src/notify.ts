// ---------------------------------------------------------------------------
// Notifications: browser Notification API (foreground tab) + Web Push
// (background/closed). Preferences are global (server-side settings).
// Each device auto-prompts for browser permission when enabled globally.
// ---------------------------------------------------------------------------

import { isIOS, isStandalone } from "./platform.js";
import { registerPush, unsubscribePush } from "./actions/notify.js";
import { registerCleanup } from "./actions/index.js";

// ---------------------------------------------------------------------------
// NotifyController: owns all notification/push state as instance fields.
// ---------------------------------------------------------------------------

type PushState =
  | { kind: "idle" }
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

  /** Public hook for global cleanup. */
  cancelPush(): void {
    registerPush.cancel();
  }

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

  areNotificationsEnabled(): boolean {
    return this.enabled;
  }
  isAgentFinishedEnabled(): boolean {
    return this.agentFinished;
  }
  isPermissionNeededEnabled(): boolean {
    return this.permissionNeeded;
  }

  setNotificationsEnabled(v: boolean): void {
    this.enabled = v;
  }
  setAgentFinishedEnabled(v: boolean): void {
    this.agentFinished = v;
  }
  setPermissionNeededEnabled(v: boolean): void {
    this.permissionNeeded = v;
  }

  setNotifyUICallback(fn: () => void): void {
    this.notifyUICallback = fn;
  }

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
      void this.registerPushViaAction();
      return null;
    }
    if (Notification.permission === "denied") {
      return "Notifications were blocked. Allow them in your browser settings.";
    }
    Notification.requestPermission()
      .then((result) => {
        if (result === "granted") {
          void this.registerPushViaAction();
        }
      })
      .catch(() => {
        /* noop */
      });
    return null;
  }

  unregisterPush(): void {
    registerPush.cancel();
    this.pushState = { kind: "idle" };
    if (this.swRegistration === null) {
      return;
    }
    const reg = this.swRegistration;
    this.swRegistration = null;
    reg.pushManager
      .getSubscription()
      .then((sub) => {
        if (sub === null) {
          return;
        }
        const endpoint = sub.endpoint;
        sub.unsubscribe().catch(() => {
          /* noop */
        });
        void unsubscribePush.dispatch({ endpoint });
      })
      .catch(() => {
        /* noop */
      });
    reg.unregister().catch(() => {
      /* noop */
    });
  }

  private autoSubscribe(): void {
    if (
      this.pushState.kind === "registered" ||
      this.pushState.kind === "failed" ||
      this.pushState.kind === "registering"
    ) {
      return;
    }
    if (!("Notification" in window)) {
      return;
    }
    if (Notification.permission === "granted") {
      void this.registerPushViaAction(true);
      return;
    }
    // 'denied' or 'default': do nothing. We only auto-register push
    // when permission is already granted. The explicit toggle click
    // path in requestPermission() handles the user-gesture prompt.
  }

  private async registerPushViaAction(silent = false): Promise<void> {
    if (this.pushState.kind === "registered" || this.pushState.kind === "registering") {
      return;
    }
    this.pushState = { kind: "registering" };
    const reg = await registerPush.dispatch(undefined, silent ? { silent: true } : undefined);
    if (reg !== null) {
      this.swRegistration = reg;
      this.pushState = { kind: "registered", registration: reg };
    } else {
      this.pushState = { kind: "failed", error: "action failed" };
    }
  }

  // --- Local notifications ---

  notifyIfHidden(title: string, body: string): boolean {
    if (!this.enabled) {
      return false;
    }
    if (document.visibilityState !== "hidden") {
      return false;
    }
    if (!("Notification" in window) || Notification.permission !== "granted") {
      return false;
    }
    try {
      const n = new Notification(title, {
        body,
        icon: "/favicon.svg",
        tag: "vibekit",
      });
      n.addEventListener("click", () => {
        window.focus();
        n.close();
      });
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
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const instance = new NotifyController();
registerCleanup(() => {
  instance.cancelPush();
});

export function areNotificationsEnabled(): boolean {
  return instance.areNotificationsEnabled();
}
export function isAgentFinishedEnabled(): boolean {
  return instance.isAgentFinishedEnabled();
}
export function isPermissionNeededEnabled(): boolean {
  return instance.isPermissionNeededEnabled();
}

export function setNotificationsEnabled(v: boolean): void {
  instance.setNotificationsEnabled(v);
}
export function setAgentFinishedEnabled(v: boolean): void {
  instance.setAgentFinishedEnabled(v);
}
export function setPermissionNeededEnabled(v: boolean): void {
  instance.setPermissionNeededEnabled(v);
}

export function setNotifyUICallback(fn: () => void): void {
  instance.setNotifyUICallback(fn);
}

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
