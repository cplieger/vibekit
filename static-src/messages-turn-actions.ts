// ---------------------------------------------------------------------------
// Turn actions: the copy / source / export buttons in the turn FOOTER.
//
// One row per turn, right-aligned beside the ledger summary, identical whether
// the turn is open or folded — the footer is the one region that survives the
// fold, so actions that operate on the whole turn live there rather than on an
// assistant bubble inside the (foldable) body.
// ---------------------------------------------------------------------------

// Defensive null/undefined checks on DOM lookups that the type system
// claims are guaranteed non-null but can race with reconcile passes.
/* eslint-disable @typescript-eslint/no-unnecessary-condition */

import type { Turn } from "./turns.js";
import { ICON_COPY, ICON_COPY_MD, ICON_SOURCE, ICON_LINK, ICON_EXPORT } from "./icons.js";
import { getActive, getActiveId } from "./store.js";
import { copyClipboard } from "./actions/messages.js";
import { downloadChatExport } from "./chat-export.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

/** Tracks active "copied" animation timers per button. */
const copyTimers = new WeakMap<HTMLElement, ReturnType<typeof setTimeout>>();

/** The turn each footer's actions operate on, refreshed every paint so click
 *  handlers read current data rather than a mount-time snapshot. */
const footerTurns = new WeakMap<HTMLElement, Turn>();

// ---------------------------------------------------------------------------
// Callbacks injected by messages.ts
// ---------------------------------------------------------------------------

let _svgTemplate: (markup: string) => () => Node = () => () => document.createDocumentFragment();

export function initTurnActionCallbacks(cbs: {
  svgTemplate: (markup: string) => () => Node;
}): void {
  _svgTemplate = cbs.svgTemplate;
}

/** Whether `card`'s mounted body is a COMPLETE answer for "copy as text": the
 *  renderer owns which ordinals a body holds, injected because messages.ts imports
 *  this module. True until wired, which is what a tree that windows nothing answers. */
let bodyHoldsWholeTurn: (card: HTMLElement, t: Turn) => boolean = () => true;

export function initTurnActionsBodyProbe(holdsWholeTurn: typeof bodyHoldsWholeTurn): void {
  bodyHoldsWholeTurn = holdsWholeTurn;
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

/** Mount the action buttons into the turn's footer, once per footer element,
 *  and refresh the turn snapshot the handlers read. Buttons appear only once
 *  the turn has settled with something to copy — a running turn's footer (rare:
 *  ledger data lands at turn end) stays actions-free, matching the old
 *  finalize-time attachment. */
export function mountTurnFooterActions(footer: HTMLElement, card: HTMLElement, t: Turn): void {
  footerTurns.set(footer, t);
  if (footer.querySelector(":scope > .turn-actions-buttons") !== null) {
    return;
  }
  if (t.outcome === "running" || turnMarkdown(t).trim() === "") {
    return;
  }

  const slot = el("span", { className: "turn-actions-buttons" });
  const current = (): Turn => footerTurns.get(footer) ?? t;

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
      el("span", { className: "turn-action-label" }, ariaLabel),
    ) as HTMLButtonElement;
    btn.addEventListener("click", () => {
      onClick(btn);
      // On phone the secondary buttons sit inside this native disclosure. An
      // action commits the choice, so close the menu in the same gesture. On
      // desktop the details content is forced inline and this changes nothing.
      btn.closest<HTMLDetailsElement>(".turn-actions-more")?.removeAttribute("open");
    });
    return btn;
  };

  // Copy as text stays direct at every width. It is the common action and the
  // one whose immediate feedback a reader expects beside the turn.
  slot.appendChild(
    makeBtn(ICON_COPY, "Copy as text", (btn) => {
      copyAndAnimate(btn, turnPlainText(card, current()));
    }),
  );

  // The other four actions stay inline on desktop and collapse behind one
  // native <details> summary on phone. One set of real buttons serves both
  // layouts, so resizing cannot leave a duplicate source toggle out of sync.
  const secondary = el("span", { className: "turn-actions-secondary" });
  secondary.appendChild(
    makeBtn(ICON_COPY_MD, "Copy as markdown", (btn) => {
      copyAndAnimate(btn, turnMarkdown(current()));
    }),
  );
  const srcBtn = makeBtn(ICON_SOURCE, "View markdown source", (btn) => {
    // Re-resolved at click time rather than captured at mount: a later
    // message_appended carrying the server's sanitized content would otherwise
    // leave a stale source behind an unchanging button.
    toggleTurnSource(card, turnMarkdown(current()), btn);
  });
  srcBtn.classList.add("turn-action-src");
  srcBtn.setAttribute("aria-pressed", "false");
  secondary.appendChild(srcBtn);
  secondary.appendChild(
    makeBtn(ICON_LINK, "Copy chat ID", (btn) => {
      const chatID = getActiveId();
      if (chatID !== "") {
        copyAndAnimate(btn, chatID);
      }
    }),
  );
  secondary.appendChild(
    makeBtn(ICON_EXPORT, "Export chat as JSON", () => {
      const chatID = getActiveId();
      if (chatID !== "") {
        downloadChatExport(chatID, getActive()?.name ?? "", "json");
      }
    }),
  );

  const more = el("details", {
    className: "turn-actions-more",
    name: "turn-actions-overflow",
  }) as HTMLDetailsElement;
  more.appendChild(
    el(
      "summary",
      {
        className: "turn-action-btn turn-action-more",
        "aria-label": "More turn actions",
        "data-tooltip": "More turn actions",
      },
      "\u2026",
    ),
  );
  more.appendChild(secondary);
  slot.appendChild(more);

  // Before the Rewind button when one exists, so the destructive action keeps
  // the far edge to itself; grid placement pins the columns either way.
  const rewind = footer.querySelector<HTMLElement>(":scope > .turn-rewind");
  if (rewind !== null) {
    rewind.before(slot);
  } else {
    footer.appendChild(slot);
  }
}

/** Class of the raw-source view. One per turn surface. */
const RAW_CLASS = "turn-raw";

/** Drop any raw-source view and restore the rendered regions. Called when the
 *  fold state changes: the raw view belongs to the surface it was opened on
 *  (body or face), and the OTHER surface renders fresh — leaving the button
 *  latched against a surface that no longer shows raw would make it lie. */
export function resetTurnSourceView(card: HTMLElement): void {
  const raws = card.querySelectorAll(`.${RAW_CLASS}`);
  if (raws.length === 0) {
    return;
  }
  for (const raw of raws) {
    raw.remove();
  }
  for (const region of renderedRegions(card)) {
    region.classList.remove("hidden");
  }
  const btn = card.querySelector<HTMLButtonElement>(
    ":scope > .turn-footer > .turn-actions-buttons .turn-action-src",
  );
  if (btn !== null) {
    setSrcButtonState(btn, false);
  }
}

/** Hide the block regions of a body that is showing raw source, after something
 *  mounted into it: `toggleTurnSource` hides the regions it FINDS, and a new ROW brings
 *  an unhidden one with it. A window move makes that reachable on any scroll. */
export function syncSourceView(body: HTMLElement): void {
  const raw = body.querySelector<HTMLElement>(`:scope > .${RAW_CLASS}`);
  if (raw === null || raw.classList.contains("hidden")) {
    return;
  }
  for (const region of body.querySelectorAll<HTMLElement>(".assistant-blocks")) {
    region.classList.add("hidden");
  }
}

/**
 * Show the turn's markdown SOURCE in place of its rendering, and back.
 *
 * The WHOLE rendered output swaps — on the OPEN body that is every
 * `.assistant-blocks` region (tool cards, reasoning traces and subagent boxes
 * included), on the folded FACE it is the prose bubble. The source is one
 * document, so every word the model wrote arrives at the top of it; a
 * half-swap would show the same turn in two different orders at once.
 *
 * Mechanics carried over from the per-message version:
 *
 *   - The raw view is a SIBLING that gets ADDED, never a replacement of the
 *     rendered children: `updateAssistantBody` runs on every repaint and would
 *     silently undo a replacement.
 *   - Exactly one of the two carries `.hidden` (`display: none !important`),
 *     because find-in-chat's walker prunes `.hidden` subtrees; `opacity` or
 *     `visibility` would leave both in the tree and double-count matches.
 *   - CONTAINERS hide, never children one by one, so a block arriving after
 *     the toggle lands inside an already-hidden region.
 */
function toggleTurnSource(card: HTMLElement, source: string, btn: HTMLButtonElement): void {
  const host = activeSurface(card);
  if (host === null) {
    return;
  }
  const rendered = renderedRegions(card);
  let raw = host.querySelector<HTMLElement>(`:scope > .${RAW_CLASS}`);
  if (raw === null) {
    raw = el("pre", { className: `${RAW_CLASS} hidden` });
    const first = rendered.find((r) => r.parentElement === host);
    if (first !== undefined) {
      first.insertAdjacentElement("beforebegin", raw);
    } else {
      host.prepend(raw);
    }
  }
  const showRaw = raw.classList.contains("hidden");
  if (showRaw) {
    raw.textContent = source;
  }
  raw.classList.toggle("hidden", !showRaw);
  for (const region of rendered) {
    region.classList.toggle("hidden", showRaw);
  }
  setSrcButtonState(btn, showRaw);
}

function setSrcButtonState(btn: HTMLButtonElement, raw: boolean): void {
  const label = raw ? "View rendered reply" : "View markdown source";
  btn.setAttribute("aria-pressed", raw ? "true" : "false");
  btn.setAttribute("aria-label", label);
  btn.setAttribute("data-tooltip", label);
}

/** The surface the source stands in for right now: the face when folded, the
 *  body when open. A folded stub has no body, so the face answer covers it. */
function activeSurface(card: HTMLElement): HTMLElement | null {
  if (card.hasAttribute("data-folded")) {
    return card.querySelector<HTMLElement>(":scope > .turn-face");
  }
  return card.querySelector<HTMLElement>(":scope > .turn-body");
}

/** The rendered regions the source hides: every block region in the body plus
 *  the face's prose bubble. Queried plural because `updateAssistantBody`'s
 *  self-healing path can leave two block regions behind; hiding only the first
 *  would leave a stray body on screen. */
function renderedRegions(card: HTMLElement): HTMLElement[] {
  return [
    ...card.querySelectorAll<HTMLElement>(
      ":scope > .turn-body .assistant-blocks, :scope > .turn-face > .turn-face-prose",
    ),
  ];
}

/** The turn's markdown, for "copy as markdown" and the source view: every
 *  assistant message's stored content joined in order, falling back to its
 *  parent-authored text blocks when a block-mode message carries an empty
 *  top-level `content`. */
export function turnMarkdown(t: Turn): string {
  const parts: string[] = [];
  for (const m of t.body) {
    if (m.role !== "assistant") {
      continue;
    }
    let text = m.content ?? "";
    if (text.trim() === "") {
      text = (m.blocks ?? [])
        .map((b) => (b.type === "text" && (b.agent_subtask_id ?? "") === "" ? (b.text ?? "") : ""))
        .filter((s) => s !== "")
        .join("\n\n");
    }
    if (text.trim() !== "") {
      parts.push(text);
    }
  }
  return parts.join("\n\n");
}

/** The turn's rendered plain text, for "copy as text": the assistant bubbles
 *  of whichever surface is mounted (body open, face folded), falling back to
 *  the markdown when neither holds one (a folded stub with no prose face).
 *
 *  The BODY answers only while it holds the whole turn MOUNTED — a windowed or
 *  still-building one would copy a hole. The face and the store are the two complete
 *  answers, in that order, exactly as before. */
function turnPlainText(card: HTMLElement, t: Turn): string {
  const bubbles = [
    ...card.querySelectorAll(
      bodyHoldsWholeTurn(card, t)
        ? ":scope > .turn-body .message.assistant, :scope > .turn-face > .message.assistant"
        : ":scope > .turn-face > .message.assistant",
    ),
  ];
  if (bubbles.length > 0) {
    return bubbles.map((b) => b.textContent ?? "").join("\n\n");
  }
  return turnMarkdown(t);
}
