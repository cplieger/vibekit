// ---------------------------------------------------------------------------
// Model-refusal callout (kiro-cli 2.13 refusal contract).
//
// When the model declines to continue a conversation (modelStopReason
// "content_filtered" — how Claude models report they refuse to continue),
// KAS streams the refusal explanation as the turn's final assistant chunk
// tagged with _meta.kiro.refusal {category?, recommendedModel?}, and the turn
// ends with stop_reason "refusal". The explanation text renders as ordinary
// assistant content; this module adds the distinct affordance KAS suggests:
// a callout under the turn with the category chip and two recovery actions —
// Rewind (branch the chat from before the refusal) and, when the service
// recommends one, Switch to the recommended model.
//
// syncRefusal mirrors code-refs.ts's shape: idempotent, called on every
// assistant mount/update, keyed off Message.refusal (persisted server-side,
// so live SSE and reload render identically).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import type { Message } from "./types.js";
import { getActive } from "./store.js";
import { switchModel } from "./actions/chat.js";
import { iconEl } from "./icon-el.js";
import { ICON_REFUSAL } from "./icons.js";

const CLS = "refusal-callout";

/** The rewind flow is owned by messages.ts (confirm dialog + open the branch
 *  tab); injected here to avoid a module cycle (messages.ts imports this
 *  module for syncRefusal). */
type RewindHandler = (m: Message) => void;
let onRewind: RewindHandler | undefined;

export function setRefusalRewindHandler(fn: RewindHandler): void {
  onRewind = fn;
}

/** Ensure `wrap`'s refusal callout matches `m.refusal`. Appends the callout
 *  when the turn is a refusal, removes it when it isn't (rewound stores can
 *  replay a message without the field), and no-ops when already mounted —
 *  the refusal metadata is immutable once stamped. */
export function syncRefusal(wrap: HTMLElement, m: Message): void {
  const existing = wrap.querySelector<HTMLElement>(`:scope > .${CLS}`);
  const refusal = m.refusal;
  if (refusal === undefined) {
    existing?.remove();
    return;
  }
  if (existing !== null) {
    return;
  }
  wrap.appendChild(buildCallout(m));
}

function buildCallout(m: Message): HTMLElement {
  const refusal = m.refusal ?? {};
  const header = el(
    "div",
    { className: "refusal-header" },
    iconEl(ICON_REFUSAL),
    el("span", { className: "refusal-title" }, "The model declined to continue"),
  );
  if (refusal.category !== undefined && refusal.category !== "") {
    header.appendChild(el("span", { className: "refusal-chip" }, refusal.category));
  }

  const actions = el("div", { className: "refusal-actions" });
  const rewindBtn = el(
    "button",
    {
      type: "button",
      className: "refusal-btn",
      "data-tooltip": "Branch a new chat from before this point and try a different approach",
    },
    "Rewind",
  ) as HTMLButtonElement;
  rewindBtn.addEventListener("click", () => {
    onRewind?.(m);
  });
  actions.appendChild(rewindBtn);

  const rec = refusal.recommended_model ?? "";
  if (rec !== "") {
    const switchBtn = el(
      "button",
      {
        type: "button",
        className: "refusal-btn",
        "data-tooltip": `Switch this chat to ${rec} and retry`,
      },
      `Switch to ${rec}`,
    ) as HTMLButtonElement;
    switchBtn.addEventListener("click", () => {
      const session = getActive();
      if (session === undefined) {
        return;
      }
      switchBtn.disabled = true;
      void switchModel.dispatch({ chatID: session.id, model: rec }).finally(() => {
        switchBtn.disabled = false;
      });
    });
    actions.appendChild(switchBtn);
  }

  return el(
    "div",
    { className: CLS, role: "note", "aria-label": "Model refusal" },
    header,
    actions,
  );
}
