// ---------------------------------------------------------------------------
// Keyboard shortcuts: single keydown handler on document.
// ---------------------------------------------------------------------------

import { emitBus, BUS_KEYS_ESCAPE } from "./bus.js";
import { closeTopModal } from "./modals.js";

interface ShortcutDef {
  key: string;
  shift?: boolean;
  action: () => void;
  description: string;
}

const shortcuts: ShortcutDef[] = [];
let initialized = false;

function register(def: ShortcutDef): void {
  shortcuts.push(def);
}

export function initKeyboardShortcuts(actions: {
  newChat: () => void;
  toggleShell: () => void;
  toggleFiles: () => void;
  toggleGit: () => void;
  toggleSettings: () => void;
  sendMessage: () => void;
}): void {
  if (initialized) {
    return;
  }
  initialized = true;

  register({ key: "k", action: actions.newChat, description: "New conversation" });
  register({ key: "n", action: actions.newChat, description: "New conversation" });
  register({ key: "/", action: actions.toggleShell, description: "Toggle shell" });
  register({
    key: "f",
    shift: true,
    action: actions.toggleFiles,
    description: "Toggle file browser",
  });
  register({ key: "g", shift: true, action: actions.toggleGit, description: "Toggle git panel" });
  register({ key: ",", action: actions.toggleSettings, description: "Toggle settings" });
  register({ key: "Enter", action: actions.sendMessage, description: "Send message" });

  document.addEventListener("keydown", (e: KeyboardEvent) => {
    // Don't intercept when typing in inputs (except for Escape and Ctrl combos)
    const target = e.target as HTMLElement;
    const isInput =
      target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.tagName === "SELECT";

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
