// ---------------------------------------------------------------------------
// Notifications: browser Notification API (foreground tab) + Web Push
// (background/closed). Preferences are global (server-side settings).
// Each device auto-prompts for browser permission when enabled globally.
// ---------------------------------------------------------------------------

import { isIOS, isStandalone } from "./platform.js";
import { registerPush, unsubscribePush } from "./actions/notify.js";
import { registerCleanup } from "./actions/index.js";
import type { EffectiveSettings } from "./wire/types.gen.js";

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
 *  `vibekit.PushKind`) and paired with the settings key that carries each one.
 *
 *  Derived from the server's registry rather than restated: a kind with a settings
 *  key is configurable, and `permission` deliberately has none — an ask blocks the
 *  turn and has no per-tab marker, so a channel that could go dark on its own
 *  would stall every later turn with nothing on screen to say why. That absence is
 *  why this map exists as a map and not as an exhaustive record over PushKind. */
export const KEYED_PUSH_KINDS: Readonly<
  Record<string, "notify_agent_finished" | "notify_pr_status">
> = {
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

// THERE IS NONE HERE. This module used to set a `data-tab-hidden` attribute on
// <html> for one CSS rule that switched off the transcript's entry animations
// while the tab was backgrounded. That rule is deleted (61-mcp-tools.css records
// the measurement: Chromium runs those animations in a hidden tab, so it guarded
// against nothing), and this was its only writer, so the attribute has no reader
// and no producer. attention.ts owns every other response to visibility — the
// title count, the favicon cue and the away summary — and it reads
// `document.visibilityState` itself.

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
 *  entry there rather than a declared parameter field plus a line in this body.
 *
 *  There is no cast and no `!== false` any more. Both existed to cope with a key
 *  that might be absent: the master switch defaults OFF and the two per-kind
 *  switches default ON (matching push.kindRegistry), so this one function had to
 *  carry two opposite polarities and get each right. The payload states all three
 *  now, and typing KEYED_PUSH_KINDS' values as the payload's own keys is what makes
 *  the runtime lookup type-safe without listing every kind's field here as well. */
export function restoreNotifications(s: EffectiveSettings): void {
  const wasEnabled = enabled;
  enabled = s.notifications_enabled;
  for (const [kind, settingsKey] of Object.entries(KEYED_PUSH_KINDS)) {
    kindEnabled.set(kind, s[settingsKey]);
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
