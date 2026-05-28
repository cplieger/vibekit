// ---------------------------------------------------------------------------
// Turn actions: copy / export buttons attached after streaming finalizes.
//
// Extracted from messages.ts — the "Turn actions" section (lines 1363-1468).
// ---------------------------------------------------------------------------

import { ICON_COPY, ICON_COPY_MD, ICON_LINK, ICON_EXPORT } from "./icons.js";
import { getActive, getActiveId } from "./store.js";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import { copyClipboard } from "./actions/messages.js";

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

/** Tracks active "copied" animation timers per button. */
const copyTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts
// ---------------------------------------------------------------------------

let _svgTemplate: (markup: string) => () => Node = () => () => document.createDocumentFragment();

export function initTurnActionCallbacks(cbs: {
  svgTemplate: (markup: string) => () => Node;
}): void {
  _svgTemplate = cbs.svgTemplate;
}

// ---------------------------------------------------------------------------
// Public
// ---------------------------------------------------------------------------

export function attachTurnActions(el: HTMLDivElement): void {
  const wrap = el.closest<HTMLElement>(".msg-wrap");
  const msgID = wrap?.getAttribute(RECONCILE_KEY) ?? "";
  const session = getActive();
  const msg = session?.messages.find((m) => m.id === msgID);
  const raw = msg?.content ?? el.textContent ?? ""; // eslint-disable-line @typescript-eslint/no-unnecessary-condition
  if (raw.trim() === "") return;
  if (el.nextElementSibling?.classList.contains("turn-actions")) return;

  const chatID = getActiveId();
  const row = document.createElement("div");
  row.className = "turn-actions";

  const leftSlot = document.createElement("span");
  leftSlot.className = "turn-actions-summary";
  row.appendChild(leftSlot);

  const rightSlot = document.createElement("span");
  rightSlot.className = "turn-actions-buttons";
  row.appendChild(rightSlot);

  const makeBtn = (
    svgMarkup: string,
    ariaLabel: string,
    onClick: (btn: HTMLButtonElement) => void,
  ): HTMLButtonElement => {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "turn-action-btn";
    btn.appendChild(_svgTemplate(svgMarkup)());
    btn.setAttribute("aria-label", ariaLabel);
    btn.setAttribute("data-tooltip", ariaLabel);
    btn.addEventListener("click", () => { onClick(btn); });
    return btn;
  };

  const copyAndAnimate = (btn: HTMLButtonElement, text: string): void => {
    void copyClipboard.dispatch(text, {
      silent: true,
      onSuccess: () => {
        btn.classList.add("copied");
        const prev = copyTimers.get(btn);
        if (prev !== undefined) clearTimeout(prev);
        copyTimers.set(
          btn,
          setTimeout(() => { btn.classList.remove("copied"); }, 1500),
        );
      },
    });
  };

  rightSlot.appendChild(
    makeBtn(ICON_COPY, "Copy as text", (btn) => {
      copyAndAnimate(btn, el.textContent ?? ""); // eslint-disable-line @typescript-eslint/no-unnecessary-condition
    }),
  );
  rightSlot.appendChild(
    makeBtn(ICON_COPY_MD, "Copy as markdown", (btn) => {
      copyAndAnimate(btn, raw);
    }),
  );
  if (chatID !== "") {
    rightSlot.appendChild(
      makeBtn(ICON_LINK, "Copy chat ID", (btn) => {
        copyAndAnimate(btn, chatID);
      }),
    );
    rightSlot.appendChild(
      makeBtn(ICON_EXPORT, "Export chat as JSON", () => {
        const a = document.createElement("a");
        a.href = `/api/chats/${encodeURIComponent(chatID)}/export`;
        a.download = `${chatID}.json`;
        a.rel = "noopener";
        document.body.appendChild(a);
        a.click();
        a.remove();
      }),
    );
  }

  if (wrap !== null && wrap !== undefined) { // eslint-disable-line @typescript-eslint/no-unnecessary-condition
    wrap.appendChild(row);
  } else {
    el.insertAdjacentElement("afterend", row);
  }
}
