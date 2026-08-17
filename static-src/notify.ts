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

// There is no document-title writer here any more. `setBadge` was named for a
// badge it never set — it wrote document.title only, was called with the literal
// 1 and the literal 0, and asserted its own copy of static/index.html's <title>
// over whatever that file declared. attention.ts owns the title now, with a real
// count folded from the chat tabs and the base captured from the served document.

type PushState =
  | { kind: "idle" }
  | { kind: "registering" }
  | { kind: "registered"; registration: ServiceWorkerRegistration }
  | { kind: "failed"; error: string };

/** The push kinds the user can switch off, keyed by their WIRE value (matching
 *  `api.PushKind`) and paired with the settings key that carries each one.
 *
 *  Derived from the server's registry rather than restated: a kind with a settings
 *  key is configurable, and `permission` deliberately has none — an ask blocks the
 *  turn and has no per-tab marker, so a channel that could go dark on its own
 *  would stall every later turn with nothing on screen to say why. That absence is
 *  why this map exists as a map and not as an exhaustive record over PushKind. */
export const KEYED_PUSH_KINDS: Readonly<Record<string, string>> = {
  agent_finished: "notify_agent_finished",
  pr_status: "notify_pr_status",
};

let swRegistration: ServiceWorkerRegistration | null = null;
let enabled = false;
/** Per-kind enabled state for the KEYED kinds only. Defaults match the server's
 *  registry (both DefaultOn), so a config.json that predates a kind behaves the
 *  same way the server does. */
const kindEnabled = new Map<string, boolean>(
  Object.keys(KEYED_PUSH_KINDS).map((kind) => [kind, true]),
);
let notifyUICallback: (() => void) | null = null;
let pushState: PushState = { kind: "idle" };

// --- Visibility tracking ---

// The `data-tab-hidden` CSS hook, and nothing else. This handler used to clear
// the title count here as well — a wholesale clear on becoming visible, which is
// the shortcut that blanks the cue of a background chat the reader never saw.
// attention.ts replaces it with the rule that only acknowledges the chat on screen
// and the sidebar rows actually in view.
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") {
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
/** Whether a KEYED kind is on. Answers true for any kind not in
 *  KEYED_PUSH_KINDS, because the only such kind is the permission floor and its
 *  answer is always yes — see the note below on why there is no getter for it. */
export function isKindEnabled(kind: string): boolean {
  return kindEnabled.get(kind) ?? true;
}

export function isAgentFinishedEnabled(): boolean {
  return isKindEnabled("agent_finished");
}

// There is no per-kind getter for the permission ask, and adding one back is
// the defect: an ask blocks the turn, so a channel that can go dark on its own
// stalls every later turn with nothing on screen to say so. The master
// `enabled` switch is the only gate, checked inside notifyIfHidden. See the
// "no notify_permission key" note in internal/settings/defaults.go.

export function setNotificationsEnabled(v: boolean): void {
  enabled = v;
}

/** Set one KEYED kind's state. A kind outside KEYED_PUSH_KINDS is ignored rather
 *  than added, so nothing can create an off switch for the permission floor by
 *  passing its name. */
export function setKindEnabled(kind: string, v: boolean): void {
  if (!(kind in KEYED_PUSH_KINDS)) {
    return;
  }
  kindEnabled.set(kind, v);
}

// There is no setAgentFinishedEnabled. It was the per-kind setter when there was
// one keyed kind; setKindEnabled is the same function with the kind as an argument,
// so keeping a named wrapper for one member would be a second door onto one room.
// isAgentFinishedEnabled stays, because handlers/turn.ts asks about exactly that
// kind on the foreground-notification path.

export function setNotifyUICallback(fn: () => void): void {
  notifyUICallback = fn;
}

/** Apply the persisted preferences.
 *
 *  The per-kind values are read through KEYED_PUSH_KINDS, so adding a kind is one
 *  entry there rather than a declared parameter field plus a line in this body. An
 *  absent value keeps the default (on), matching the server's registry for a
 *  config.json that predates the kind.
 *
 *  The lookup is BY KEY — the settings key comes from KEYED_PUSH_KINDS at runtime —
 *  so the body indexes a widened view of the payload. That widening is the one cast
 *  here, and it is safe in the direction that matters: every read compares against
 *  `false`, so a field of some other type cannot be misread as an off switch, and a
 *  key that is absent keeps the default. Narrowing to a declared interface instead
 *  would mean listing every kind's field here as well as in KEYED_PUSH_KINDS. */
export function restoreNotifications(s: object): void {
  const flags = s as Readonly<Record<string, unknown>>;
  const wasEnabled = enabled;
  enabled = flags["notifications_enabled"] === true;
  for (const [kind, settingsKey] of Object.entries(KEYED_PUSH_KINDS)) {
    kindEnabled.set(kind, flags[settingsKey] !== false);
  }

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
