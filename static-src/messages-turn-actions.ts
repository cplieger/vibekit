// ---------------------------------------------------------------------------
// Turn actions: copy / export buttons attached after streaming finalizes.
//
// Extracted from messages.ts — the "Turn actions" section (lines 1363-1468).
// ---------------------------------------------------------------------------

// Defensive null/undefined checks on DOM lookups that the type system
// claims are guaranteed non-null but can race with reconcile passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import type { Message } from "./types.js";
import { ICON_COPY, ICON_COPY_MD, ICON_SOURCE, ICON_LINK, ICON_EXPORT } from "./icons.js";
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

/** Copy `text` and flash the button's confirmation.
 *
 *  Exported because the turn HEADER's Copy needs exactly this — the same action
 *  dispatch, the same `.copied` class, the same per-button timer bookkeeping —
 *  and `fundamentals/turn-header.ts` is a pure view that must not import
 *  `actions/`. messages.ts injects it there. */
export function copyWithFeedback(btn: HTMLButtonElement, text: string): void {
  if (text === "") {
    return;
  }
  copyAndAnimate(btn, text);
}

function copyAndAnimate(btn: HTMLButtonElement, text: string): void {
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
  const srcBtn = makeBtn(ICON_SOURCE, "View markdown source", (btn) => {
    // Re-resolved at click time rather than reusing the `markdown` captured
    // above: the row attaches once and is idempotent, so a later
    // message_appended carrying the server's sanitized content would otherwise
    // leave a stale source behind an unchanging button.
    toggleRawSource(wrap, contentEl, msgID, markdown, btn);
  });
  srcBtn.setAttribute("aria-pressed", "false");
  rightSlot.appendChild(srcBtn);
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

/** Class of the raw-source view. One per assistant turn. */
const RAW_CLASS = "turn-raw";

/**
 * Show the reply's markdown SOURCE in place of its rendering, and back.
 *
 * Two mechanical constraints decide the shape, and both were paid for once
 * already elsewhere in this module:
 *
 *   - The raw view is a SIBLING that gets ADDED, never a replacement of the
 *     rendered children. `messageSpec.update` runs `updateAssistantBody` on
 *     every repaint — including every streamed chunk of a LATER turn — so a
 *     toggle that replaced the body would be silently undone.
 *   - Exactly one of the two carries `.hidden` (`display: none`), because
 *     find-in-chat's walker prunes `.hidden` subtrees. Hiding with `opacity` or
 *     `visibility` would leave both in the tree and double-count every match.
 *
 * Only the rendered TEXT bubbles swap out. Tool cards stay put: they are not in
 * the markdown, so hiding them would answer a question nobody asked.
 */
function toggleRawSource(
  wrap: HTMLElement | null,
  contentEl: HTMLDivElement,
  msgID: string,
  fallback: string,
  btn: HTMLButtonElement,
): void {
  const host = wrap ?? contentEl.parentElement;
  if (host === null) {
    return;
  }
  let raw = host.querySelector<HTMLElement>(`.${RAW_CLASS}`);
  if (raw === null) {
    raw = el("pre", { className: `${RAW_CLASS} hidden` });
    // First child of the block region, which is where the prose it replaces
    // sits. New blocks append after it, so streaming cannot displace it.
    const blocks = host.querySelector<HTMLElement>(":scope > .assistant-blocks");
    if (blocks !== null) {
      blocks.prepend(raw);
    } else {
      host.appendChild(raw);
    }
  }
  const showRaw = raw.classList.contains("hidden");
  if (showRaw) {
    raw.textContent = currentSource(msgID, wrap, contentEl, fallback);
  }
  raw.classList.toggle("hidden", !showRaw);
  for (const bubble of host.querySelectorAll<HTMLElement>(".message.assistant")) {
    bubble.classList.toggle("hidden", showRaw);
  }
  const label = showRaw ? "View rendered reply" : "View markdown source";
  btn.setAttribute("aria-pressed", showRaw ? "true" : "false");
  btn.setAttribute("aria-label", label);
  btn.setAttribute("data-tooltip", label);
}

/** The message's markdown as the store holds it NOW, falling back to whatever
 *  the row captured when it attached. */
function currentSource(
  msgID: string,
  wrap: HTMLElement | null,
  contentEl: HTMLDivElement,
  fallback: string,
): string {
  const msg = getActive()?.messages.find((m) => m.id === msgID);
  const md = turnMarkdown(msg, wrap, contentEl);
  return md.trim() === "" ? fallback : md;
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
