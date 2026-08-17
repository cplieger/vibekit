// ---------------------------------------------------------------------------
// Keyboard shortcuts: single keydown handler on document.
//
// Two keys are handled BEFORE the Ctrl/Cmd gate, because they carry no modifier:
// Escape (close the topmost modal) and `?` (open the shortcut reference sheet).
// Everything in the `shortcuts` table below requires Ctrl or Cmd, so a bare key
// can never be a table row — the gate returns before the loop is reached.
// ---------------------------------------------------------------------------

import { emitBus, BUS_KEYS_ESCAPE } from "./bus.js";
import { closeTopModal } from "./modals.js";

interface ShortcutDef {
  key: string;
  shift?: boolean;
  action: () => void;
  /** What the binding does, in the reference sheet's words. */
  description: string;
  /** Which heading the reference sheet files it under. Presentation, like
   *  `description`, and it lives here for the same reason: the sheet is
   *  generated from this table, so a binding declared here needs no second edit
   *  anywhere to appear on it correctly. */
  group: string;
}

/** One registered chord, as the reference sheet reads it. The action is
 *  deliberately absent: the sheet describes bindings, it does not invoke them. */
export interface ShortcutBinding {
  readonly key: string;
  readonly shift: boolean;
  readonly description: string;
  readonly group: string;
}

const shortcuts: ShortcutDef[] = [];
let initialized = false;

function register(def: ShortcutDef): void {
  shortcuts.push(def);
}

/** The Ctrl/Cmd chords this module currently answers to.
 *
 *  Exported so the reference sheet is GENERATED from the registry rather than
 *  transcribed beside it: a binding added below then appears on the sheet with no
 *  second edit, which is the only way a hand-written list stays true. Empty until
 *  initKeyboardShortcuts has run. */
export function registeredShortcuts(): readonly ShortcutBinding[] {
  return shortcuts.map((s) => ({
    key: s.key,
    shift: s.shift === true,
    description: s.description,
    group: s.group,
  }));
}

/** True when the event's target is a text-entry surface, so an unmodified
 *  printable key belongs to it rather than to a global binding. */
function isTextEntry(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  return (
    target instanceof HTMLInputElement ||
    target instanceof HTMLTextAreaElement ||
    target instanceof HTMLSelectElement ||
    target.isContentEditable
  );
}

export function initKeyboardShortcuts(actions: {
  newChat: () => void;
  toggleShell: () => void;
  toggleFiles: () => void;
  toggleGit: () => void;
  toggleSettings: () => void;
  sendMessage: () => void;
  showShortcuts: () => void;
}): void {
  if (initialized) {
    return;
  }
  initialized = true;

  register({ key: "k", action: actions.newChat, description: "New conversation", group: "Chats" });
  register({ key: "n", action: actions.newChat, description: "New conversation", group: "Chats" });
  register({ key: "/", action: actions.toggleShell, description: "Toggle shell", group: "Panels" });
  register({
    key: "f",
    shift: true,
    action: actions.toggleFiles,
    description: "Toggle file browser",
    group: "Panels",
  });
  register({
    key: "g",
    shift: true,
    action: actions.toggleGit,
    description: "Toggle git panel",
    group: "Panels",
  });
  register({
    key: ",",
    action: actions.toggleSettings,
    description: "Toggle settings",
    group: "Panels",
  });
  register({
    key: "Enter",
    action: actions.sendMessage,
    description: "Send message",
    group: "Composer",
  });

  document.addEventListener("keydown", (e: KeyboardEvent) => {
    // Don't intercept when typing in inputs (except for Escape and Ctrl combos)
    const isInput = isTextEntry(e.target);

    if (e.key === "Escape") {
      // Close topmost modal/panel via the shared helper, which
      // ensures confirm-dialog cleanup (clone-replaced buttons)
      // runs correctly even on Escape dismissal.
      if (closeTopModal()) {
        e.preventDefault();
        return;
      }
      // Deselect in file browser
      emitBus(BUS_KEYS_ESCAPE);
      return;
    }

    const mod = e.ctrlKey || e.metaKey;

    // `?` is Shift+/ with no Ctrl or Cmd, so it sits above the gate beside
    // Escape. It must not fire while the user is typing — `?` is an ordinary
    // character in a prompt — and it must not ALSO reach app.ts's
    // focusComposerOnTyping, which redirects any bare printable key into the
    // composer and would type the `?` there while the sheet opened.
    // stopImmediatePropagation is what stops that sibling listener on the same
    // node; preventDefault alone does not, because it inspects the target and the
    // modifiers rather than defaultPrevented.
    if (!mod && !e.altKey && e.key === "?" && !isInput) {
      e.preventDefault();
      e.stopImmediatePropagation();
      actions.showShortcuts();
      return;
    }

    if (!mod) {
      return;
    }

    for (const s of shortcuts) {
      if (s.key.toLowerCase() !== e.key.toLowerCase()) {
        continue;
      }
      if (s.shift === true && !e.shiftKey) {
        continue;
      }
      if (s.shift !== true && e.shiftKey) {
        continue;
      }

      // Allow Ctrl+Enter in textareas for send
      if (s.key === "Enter" && isInput) {
        e.preventDefault();
        s.action();
        return;
      }

      // Skip other shortcuts when focused on input
      if (isInput && s.key !== "Enter") {
        // Allow Ctrl+K/N to work even in inputs (new chat)
        if (s.key === "k" || s.key === "n") {
          e.preventDefault();
          s.action();
          return;
        }
        continue;
      }

      e.preventDefault();
      s.action();
      return;
    }
  });
}
