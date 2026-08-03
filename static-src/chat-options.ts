// ---------------------------------------------------------------------------
// The chat options menu: the composer's home for SET-ONCE, per-chat switches.
//
// One `⋯` pill expanding to a small card (the standard pill-expand pattern —
// inline diagonal growth, one open at a time, no floating popup). Its first
// resident is the Supervised toggle, which has been HOMELESS since the
// supervised pill died with the staged-write store: the pill's expanded list
// was KAS's now, but the per-chat CHOICE still needs a control, and
// `set_supervised_mode` had a wire and no UI.
//
// Deliberately a menu of switches rather than more pills. A pill earns its
// prompt-row slot by changing per message (model, role, attachments); a
// set-once mode does not, and each one as a pill would grow the row without
// bound. The menu is where the next such switch lands too.
// ---------------------------------------------------------------------------

import { el, effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { activeSession } from "./store.js";
import { makeExpandable } from "./pill-expand.js";
import { setSupervised } from "./actions/chat.js";

let bound = false;

/** Wire the chat options pill. Idempotent; called once from app.ts. */
export function initChatOptions(): void {
  if (bound) {
    return;
  }
  bound = true;

  const pill = $.chatOptionsBtn;
  const card = $.chatOptionsCard;

  const supervised = el("input", {
    type: "checkbox",
    id: "chat-opt-supervised",
  }) as HTMLInputElement;
  supervised.addEventListener("change", () => {
    const id = activeSession.peek()?.id ?? "";
    if (id === "") {
      // No chat yet: nothing to persist against. Reset the visual; the
      // supervised DEFAULT for brand-new chats lives in Settings →
      // Permissions, which is where a "before the first prompt" choice
      // belongs.
      supervised.checked = false;
      return;
    }
    void setSupervised.dispatch({ chatID: id, enabled: supervised.checked });
  });

  const row = el(
    "label",
    { className: "chat-opt-row", for: "chat-opt-supervised" },
    supervised,
    el(
      "span",
      { className: "chat-opt-text" },
      el("span", { className: "chat-opt-name" }, "Supervised mode"),
      el(
        "span",
        { className: "chat-opt-hint" },
        "Review this chat's file changes at the end of each turn",
      ),
    ),
  );
  card.appendChild(row);

  // The checkbox mirrors the ACTIVE chat's persisted choice; the menu is a
  // projection, so switching tabs re-reads rather than remembers.
  effect(() => {
    supervised.checked = activeSession.value?.supervised_mode === true;
  });

  makeExpandable(pill, card, { haspopup: "dialog" });
}
