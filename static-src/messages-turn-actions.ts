// ---------------------------------------------------------------------------
// Turn actions: copy / export buttons attached after streaming finalizes.
//
// Extracted from messages.ts — the "Turn actions" section (lines 1363-1468).
// ---------------------------------------------------------------------------

// Defensive null/undefined checks on DOM lookups that the type system
// claims are guaranteed non-null but can race with reconcile passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import type { Message } from "./types.js";
import { ICON_COPY, ICON_COPY_MD, ICON_LINK, ICON_EXPORT } from "./icons.js";
import { getActive, getActiveId } from "./store.js";
import { KEY_ATTR as RECONCILE_KEY } from "./reconcile.js";
import { copyClipboard } from "./actions/messages.js";
import { downloadChatExport } from "./chat-export.js";
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
  // Idempotent: exactly one turn-actions row per assistant turn, whether it
  // was attached on live-stream finalize or on a historical/reloaded mount.
  // Guard at the wrap level (the row is appended to the wrap, not right after
  // contentEl) so block-mode turns with trailing tool/thinking blocks aren't
  // double-decorated.
  if (wrap !== null && wrap.querySelector(":scope > .turn-actions") !== null) {
    return;
  }
  if (contentEl.nextElementSibling?.classList.contains("turn-actions")) {
    return;
  }
  const msgID = wrap?.getAttribute(RECONCILE_KEY) ?? "";
  const session = getActive();
  const msg = session?.messages.find((m) => m.id === msgID);
  const markdown = turnMarkdown(msg, wrap, contentEl);
  if (markdown.trim() === "") {
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
      copyAndAnimate(btn, turnPlainText(wrap, contentEl));
    }),
  );
  rightSlot.appendChild(
    makeBtn(ICON_COPY_MD, "Copy as markdown", (btn) => {
      copyAndAnimate(btn, markdown);
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
        downloadChatExport(chatID, session?.name ?? "", "json");
      }),
    );
  }

  if (wrap !== null && wrap !== undefined) {
    wrap.appendChild(row);
  } else {
    contentEl.insertAdjacentElement("afterend", row);
  }
}

/** The turn's markdown, for "copy as markdown": the stored message content,
 *  or the concatenation of its text blocks, or the rendered text as a last
 *  resort. Block-mode turns can carry an empty top-level `content`, so the
 *  fallbacks keep the row attaching (and the copy correct) on those turns. */
function turnMarkdown(
  msg: Message | undefined,
  wrap: HTMLElement | null,
  contentEl: HTMLDivElement,
): string {
  if (msg !== undefined) {
    if (msg.content !== undefined && msg.content !== "") {
      return msg.content;
    }
    const text = (msg.blocks ?? [])
      .map((b) => (b.type === "text" ? (b.text ?? "") : ""))
      .filter((t) => t !== "")
      .join("\n\n")
      .trim();
    if (text !== "") {
      return text;
    }
  }
  return turnPlainText(wrap, contentEl);
}

/** The turn's rendered plain text, for "copy as text": every assistant bubble
 *  in the turn joined, so a block-mode turn with several text bubbles copies
 *  whole rather than just the bubble the finalize path happened to pass. */
function turnPlainText(wrap: HTMLElement | null, contentEl: HTMLDivElement): string {
  if (wrap !== null) {
    const bubbles = [...wrap.querySelectorAll(".message.assistant")];
    if (bubbles.length > 0) {
      return bubbles.map((b) => b.textContent ?? "").join("\n\n");
    }
  }
  return contentEl.textContent ?? "";
}
