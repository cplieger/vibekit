// ---------------------------------------------------------------------------
// Notifications: browser Notification API (foreground tab) + Web Push
// (background/closed). Preferences are global (server-side settings).
// Each device auto-prompts for browser permission when enabled globally.
// ---------------------------------------------------------------------------

import { isIOS, isStandalone } from "./platform.js";
import { registerPush, unsubscribePush } from "./actions/notify.js";
import { registerCleanup } from "./actions/index.js";
import { createNotifyAsk, type NotifyAsk } from "./notify-ask.js";
import { LS_NOTIFY_ASK_KEY } from "./ls-keys.js";
import { patchSettings } from "./persist.js";
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
  Record<string, "notify_agent_finished" | "notify_pr_status" | "notify_run_outcome">
> = {
  agent_finished: "notify_agent_finished",
  pr_status: "notify_pr_status",
  run_outcome: "notify_run_outcome",
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

// ---------------------------------------------------------------------------
// The permission ask: two doors onto one prompt.
//
// `requestPermission` below is the SETTINGS door — the user asked for
// notifications, so the prompt is raised inside their own click. The arm/gesture
// pair is the AUTOMATIC door: a cue that wanted to fire and could not arms the ask,
// and the reader's next click spends it. Both live here so they cannot disagree
// about what a grant leads to, and the model itself is DOM-free in `notify-ask.ts`.
// ---------------------------------------------------------------------------

let ask: NotifyAsk | null = null;

/** The Notification constructor, or undefined where it is not one.
 *
 *  Read through `unknown` and tested for a FUNCTION rather than with the
 *  `"Notification" in window` idiom the rest of this module uses, which is the shape
 *  `@cplieger/web-terminal-ui`'s own binding takes: `in` answers true for a global
 *  that exists and is not a constructor, and reading `.permission` off that throws
 *  out of the arm — which would take a whole cue down for a capability check. The
 *  fleet's own rule (`typescript.md`, "Read the capability off the object, never test
 *  for the object") says the same thing from the other side. */
function notificationCtor(): { permission?: unknown; requestPermission?: unknown } | undefined {
  const value: unknown = (globalThis as { Notification?: unknown }).Notification;
  return typeof value === "function"
    ? (value as { permission?: unknown; requestPermission?: unknown })
    : undefined;
}

/** Built on first use, never at module load: every member below reads a global, and
 *  this module is imported by handlers long before any of them is asked a question. */
function notifyAsk(): NotifyAsk {
  ask ??= createNotifyAsk({
    supported: (): boolean => notificationCtor() !== undefined,
    permission: (): string => {
      const value = notificationCtor()?.permission;
      // Anything unrecognised degrades to the value that asks for nothing.
      return typeof value === "string" ? value : "denied";
    },
    request: async (): Promise<string> => {
      const api = notificationCtor();
      const fn = api?.requestPermission;
      if (typeof fn !== "function") {
        return "denied";
      }
      // Both shapes tolerated: modern browsers return a promise, older Safari takes
      // a callback and returns undefined, in which case the answer is already on
      // `permission` by the time the await resolves.
      const answer: unknown = await (fn as () => unknown).call(api);
      if (typeof answer === "string") {
        return answer;
      }
      const settled = notificationCtor()?.permission;
      return typeof settled === "string" ? settled : "denied";
    },
    // A browser blocking site data throws on ACCESS, and both directions of that
    // failure are chosen deliberately: an unreadable marker reads as NOT spent, so
    // the feature still works, degrading to the reference's own once-per-page nag
    // because the module-level flag is then the only bound.
    spent: (): boolean => {
      try {
        return localStorage.getItem(LS_NOTIFY_ASK_KEY) === "1";
      } catch {
        return false;
      }
    },
    markSpent: (): void => {
      try {
        localStorage.setItem(LS_NOTIFY_ASK_KEY, "1");
      } catch {
        /* nothing to remember it with */
      }
    },
    granted: (): void => {
      void adoptGrant();
    },
  });
  return ask;
}

/** Drop the ask's per-page flags. Exported for test isolation only — the browser
 *  module registry is URL-keyed, so a suite cannot re-evaluate this module. */
export function _resetNotifyAskForTest(): void {
  ask = null;
}

/** Note that the app wanted to notify and could not, so the next gesture may ask.
 *
 *  Module-private on purpose: `notifyIfHidden` is the one funnel every cue passes
 *  through, so there is no second site that should be able to arm the ask. */
function armNotifyAsk(): void {
  notifyAsk().arm();
}

/** Note a user gesture. Raises the prompt if one is armed and still worth raising.
 *  Private for the same reason — the listener below is the only caller. */
function noteNotifyGesture(): void {
  notifyAsk().gesture();
}

/** Record that this device's ask is answered without raising anything — the door for
 *  the Settings toggle, in BOTH directions. */
export function spendNotifyAsk(): void {
  notifyAsk().spend();
}

/** Wire the gesture. ONE delegated `click` listener rather than a handler per
 *  control: a click is what carries user activation to a keyboard user too (Enter on
 *  a button synthesizes one), and it is a discrete deliberate act — `keydown` would
 *  raise the prompt over someone mid-sentence in the composer. Capture phase, so a
 *  `stopPropagation` in app code cannot hide the gesture. */
export function installNotifyAskGesture(): () => void {
  const ac = new AbortController();
  document.addEventListener(
    "click",
    () => {
      noteNotifyGesture();
    },
    { capture: true, passive: true, signal: ac.signal },
  );
  return () => {
    ac.abort();
  };
}

/** The browser granted permission through the automatic door, so turn the switch on:
 *  a prompt the reader answered has to leave Settings agreeing with the answer, or the
 *  next cue is refused by a switch they never chose and the app looks broken.
 *
 *  Only the MASTER key is patched. The per-kind switches are their own choices and
 *  default on, so a cue that armed the ask has its own kind on by construction; a
 *  grant must not silently re-enable a channel the reader turned off. (The Settings
 *  toggle enables all of them, because there the user is answering about the whole
 *  feature rather than about the one cue that fired.)
 *
 *  Push is subscribed only after the server confirms: a subscription under a master
 *  switch the server never accepted would deliver notifications the settings page
 *  shows as off. `notifyUICallback` is what makes the toggle follow — the same seam
 *  `restoreNotifications` uses, so this needs no reach into the settings DOM. */
async function adoptGrant(): Promise<void> {
  if (!enabled) {
    const saved = await patchSettings({ notifications_enabled: true });
    if (saved === null) {
      return;
    }
    enabled = true;
    notifyUICallback?.();
  }
  await registerPushViaAction();
}

export function requestPermission(): string | null {
  // The Settings door raises the prompt itself, so the automatic one has nothing
  // left to do on this device whichever way the answer goes.
  spendNotifyAsk();
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
  // The arm LEADS every gate below, because each of them is a way this call can want
  // to notify and not be able to — the switch off, the page in front of the reader,
  // the permission unanswered. This is the one funnel every cue passes through, which
  // is what keeps a new notify site from having to remember to arm.
  armNotifyAsk();
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
