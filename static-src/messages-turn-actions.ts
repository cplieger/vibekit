// ---------------------------------------------------------------------------
// Turn actions: copy / export buttons attached after streaming finalizes.
//
// Extracted from messages.ts — the "Turn actions" section (lines 1363-1468).
// ---------------------------------------------------------------------------

// Defensive null/undefined checks on DOM lookups that the type system
// claims are guaranteed non-null but can race with reconcile passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import { ICON_COPY, ICON_COPY_MD, ICON_LINK, ICON_EXPORT } from "./icons.js";
import { getActive, getActiveId } from "./store.js";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import { copyClipboard } from "./actions/messages.js";
import { el } from "@cplieger/reactive";

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

export function attachTurnActions(contentEl: HTMLDivElement): void {
  const wrap = contentEl.closest<HTMLElement>(".msg-wrap");
  const msgID = wrap?.getAttribute(RECONCILE_KEY) ?? "";
  const session = getActive();
  const msg = session?.messages.find((m) => m.id === msgID);
  const raw = msg?.content ?? contentEl.textContent ?? "";
  if (raw.trim() === "") {
    return;
  }
  if (contentEl.nextElementSibling?.classList.contains("turn-actions")) {
    return;
  }

  const chatID = getActiveId();

  const rightSlot = el("span", { className: "turn-actions-buttons" });
  const row = el(
    "div",
    { className: "turn-actions" },
    el("span", { className: "turn-actions-summary" }),
    rightSlot,
  );

  const makeBtn = (
    svgMarkup: string,
    ariaLabel: string,
    onClick: (btn: HTMLButtonElement) => void,
  ): HTMLButtonElement => {
    const btn = el(
      "button",
      {
        type: "button",
        className: "turn-action-btn",
        "aria-label": ariaLabel,
        "data-tooltip": ariaLabel,
      },
      _svgTemplate(svgMarkup)(),
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      onClick(btn);
    });
    return btn;
  };

  const copyAndAnimate = (btn: HTMLButtonElement, text: string): void => {
    void copyClipboard.dispatch(text, {
      silent: true,
      onSuccess: () => {
        btn.classList.add("copied");
        const prev = copyTimers.get(btn);
        if (prev !== undefined) {
          clearTimeout(prev);
        }
        copyTimers.set(
          btn,
          setTimeout(() => {
            btn.classList.remove("copied");
          }, 1500),
        );
      },
    });
  };

  rightSlot.appendChild(
    makeBtn(ICON_COPY, "Copy as text", (btn) => {
      copyAndAnimate(btn, contentEl.textContent ?? "");
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
        const a = el("a", {
          href: `/api/chats/${encodeURIComponent(chatID)}/export`,
          download: `${chatID}.json`,
          rel: "noopener",
        });
        document.body.appendChild(a);
        a.click();
        a.remove();
      }),
    );
  }

  if (wrap !== null && wrap !== undefined) {
    wrap.appendChild(row);
  } else {
    contentEl.insertAdjacentElement("afterend", row);
  }
}
