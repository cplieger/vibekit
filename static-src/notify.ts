// ---------------------------------------------------------------------------
// Notifications: browser Notification API (foreground tab) + Web Push
// (background/closed). Preferences are global (server-side settings).
// Each device auto-prompts for browser permission when enabled globally.
// ---------------------------------------------------------------------------

import { isIOS, isStandalone } from "./platform.js";
import { registerPush, unsubscribePush } from "./actions/notify.js";
import { registerCleanup } from "./actions/index.js";

// ---------------------------------------------------------------------------
// Module-level state (replaces the former NotifyController class).
// ---------------------------------------------------------------------------

/** Application name used in browser Notification titles. */
export const NOTIFY_TITLE = "Vibekit";

/** Application name used in document.title (tab title). */
export const DOC_TITLE_BASE = "Vibekit for Kiro";

type PushState =
  | { kind: "idle" }
  | { kind: "registering" }
  | { kind: "registered"; registration: ServiceWorkerRegistration }
  | { kind: "failed"; error: string };

let swRegistration: ServiceWorkerRegistration | null = null;
let enabled = false;
let agentFinished = true;
let permissionNeeded = true;
let notifyUICallback: (() => void) | null = null;
let pushState: PushState = { kind: "idle" };

// --- Visibility tracking ---

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") {
    setBadge(0);
    document.documentElement.removeAttribute("data-tab-hidden");
  } else {
    document.documentElement.setAttribute("data-tab-hidden", "");
  }
});

if (document.visibilityState !== "visible") {
  document.documentElement.setAttribute("data-tab-hidden", "");
}

// --- Cleanup registration ---

registerCleanup(() => {
  registerPush.cancel();
});

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

export function areNotificationsEnabled(): boolean {
  return enabled;
}
export function isAgentFinishedEnabled(): boolean {
  return agentFinished;
}
export function isPermissionNeededEnabled(): boolean {
  return permissionNeeded;
}

export function setNotificationsEnabled(v: boolean): void {
  enabled = v;
}
export function setAgentFinishedEnabled(v: boolean): void {
  agentFinished = v;
}
export function setPermissionNeededEnabled(v: boolean): void {
  permissionNeeded = v;
}

export function setNotifyUICallback(fn: () => void): void {
  notifyUICallback = fn;
}

export function restoreNotifications(s: {
  notifications_enabled?: boolean;
  notify_agent_finished?: boolean;
  notify_permission?: boolean;
}): void {
  const wasEnabled = enabled;
  enabled = s.notifications_enabled === true;
  agentFinished = s.notify_agent_finished !== false;
  permissionNeeded = s.notify_permission !== false;

  if (enabled) {
    autoSubscribe();
  } else if (wasEnabled) {
    unregisterPush();
  }
  notifyUICallback?.();
}

export function requestPermission(): string | null {
  if (!("Notification" in window)) {
    if (isIOS && !isStandalone) {
      return "Add this app to your Home Screen first, then enable notifications.";
    }
    return "Notifications are not supported in this browser.";
  }
  if (Notification.permission === "granted") {
    void registerPushViaAction();
    return null;
  }
  if (Notification.permission === "denied") {
    return "Notifications were blocked. Allow them in your browser settings.";
  }
  Notification.requestPermission()
    .then((result) => {
      if (result === "granted") {
        void registerPushViaAction();
      }
    })
    .catch(() => {
      /* noop */
    });
  return null;
}

export function unregisterPush(): void {
  registerPush.cancel();
  pushState = { kind: "idle" };
  if (swRegistration === null) {
    return;
  }
  const reg = swRegistration;
  swRegistration = null;
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

export function notifyIfHidden(title: string, body: string): boolean {
  if (!enabled) {
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

export function setBadge(count: number): void {
  document.title = count > 0 ? `(${String(count)}) ${DOC_TITLE_BASE}` : DOC_TITLE_BASE;
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

function autoSubscribe(): void {
  if (
    pushState.kind === "registered" ||
    pushState.kind === "failed" ||
    pushState.kind === "registering"
  ) {
    return;
  }
  if (!("Notification" in window)) {
    return;
  }
  if (Notification.permission === "granted") {
    void registerPushViaAction(true);
    return;
  }
}

async function registerPushViaAction(silent = false): Promise<void> {
  if (pushState.kind === "registered" || pushState.kind === "registering") {
    return;
  }
  pushState = { kind: "registering" };
  const reg = await registerPush.dispatch(undefined, silent ? { silent: true } : undefined);
  if (reg !== null) {
    swRegistration = reg;
    pushState = { kind: "registered", registration: reg };
  } else {
    pushState = { kind: "failed", error: "action failed" };
  }
}
