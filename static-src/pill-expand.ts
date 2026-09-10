// ---------------------------------------------------------------------------
// Expandable pills: click/keyboard to expand a pill into a detail card
// anchored to the pill's position. Only one pill can be expanded at a time.
// Click outside, click the pill, or press Escape to collapse.
//
// The popup lifecycle — outside-click dismissal, Escape, single-open
// coordination, trigger ARIA (aria-expanded / aria-haspopup), and the
// enter/leave state classes with transition-end settling — is
// @cplieger/ui-primitives' createPopup: the non-positioning popup primitive,
// which is exactly this pattern's shape. The card is a SIBLING of the pill
// inside .pill-slot, which positions it (vibekit.md mandates the
// expandable-pill pattern over floating popups for pill-row controls, so
// popover's placement engine is deliberately not involved). Sibling rather
// than child for two reasons: the pill's press scale would otherwise shrink
// its own open card, and a card nested in the trigger puts interactive
// content inside a <button>. This module keeps only the pill-specific glue:
// the toggle wiring, the .pill-expanded skin class, and the legacy
// hidden-class normalization. Enter/exit motion stays in 15-input.css, keyed
// off the library's is-open class on .pill-expand-content.
// ---------------------------------------------------------------------------

import { closePopupGroup, createPopup } from "@cplieger/ui-primitives/popup";

/** Single-open coordination group shared by every expandable pill. */
const PILL_GROUP = "pill-expand";

export function makeExpandable(
  pill: HTMLElement,
  contentEl: HTMLElement,
  opts?: {
    onExpand?: () => void;
    onCollapse?: () => void;
    signal?: AbortSignal;
    haspopup?: "menu" | "listbox" | "tree" | "grid" | "dialog" | true;
  },
): void {
  const listenerOpts = opts?.signal !== undefined ? { signal: opts.signal } : undefined;

  // Normalize the legacy display state: consumers author the card with the
  // `hidden` utility CLASS; the popup primitive drives the `[hidden]`
  // ATTRIBUTE plus the is-open / is-leaving state classes.
  contentEl.classList.remove("hidden");
  contentEl.hidden = true;

  // Collapsed ARIA present before the first toggle (createPopup writes the
  // same attributes on show/hide).
  pill.setAttribute("aria-expanded", "false");
  pill.setAttribute("aria-haspopup", String(opts?.haspopup ?? "true"));

  const popup = createPopup(contentEl, {
    trigger: pill,
    group: PILL_GROUP,
    // The old document-level Escape handler let the key keep propagating to
    // the app's global key handling; keep that contract.
    isolateEscape: false,
    ...(opts?.haspopup !== undefined ? { haspopup: opts.haspopup } : {}),
    onOpen: () => {
      pill.classList.add("pill-expanded");
      opts?.onExpand?.();
      // Consumers such as the model and mode pickers build their rows on open;
      // position from that final synchronous width, not the empty card's width.
      clampToViewport(pill, contentEl);
    },
    onClose: () => {
      pill.classList.remove("pill-expanded");
      opts?.onCollapse?.();
    },
  });

  pill.addEventListener(
    "click",
    (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      // A card OUTSIDE the pill (every consumer today, see the header) sends
      // no click here at all. The guard stays for a consumer that nests its
      // card: clicks on the card's CONTENT must not toggle, because the
      // buttons and inputs inside an expanded card have to work.
      if (contentEl.contains(target) && target !== contentEl) {
        return;
      }
      // Shield other document-level click handlers from pill toggles, exactly
      // like the old delegated implementation did.
      e.stopPropagation();
      popup.toggle();
    },
    listenerOpts,
  );

  // Keyboard: Enter/Space to toggle, Escape (while focus is on the pill) to
  // collapse — the popup's own document-level Escape covers the general case.
  pill.addEventListener(
    "keydown",
    (e: KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        popup.toggle();
      } else if (e.key === "Escape" && popup.isOpen) {
        e.preventDefault();
        popup.hide();
      }
    },
    listenerOpts,
  );

  // A consumer tearing down via its AbortSignal also drops the popup wiring.
  opts?.signal?.addEventListener("abort", () => {
    popup.dispose();
  });
}

/** The smallest block size worth clamping to. Below this a card is unusable
 *  whatever it does, so the floor keeps one row plus its scroll affordance on
 *  screen and lets the overflow do the rest, rather than resolving to a height
 *  that renders nothing. Only reachable on a viewport short enough that the
 *  composer is nearly at the top edge. */
const MIN_CARD_BLOCK_PX = 96;

/** Keep an expanded card inside the visual viewport, on BOTH axes.
 *
 *  Inline: the card remains a sibling positioned by `.pill-slot`; only its
 *  inline offset moves. The transform origin follows the trigger, so a clamped
 *  card still grows from the pill that opened it rather than from the screen edge.
 *
 *  Block: the card is anchored `bottom: calc(100% + var(--sp-1))` and grows
 *  UPWARD, so the thing that bounds it is the room between the viewport's top
 *  edge and the card's own bottom. That room is measured here and published as
 *  `--pill-max-block` for 15-input.css to cap against, which replaced two
 *  authored caps that could not know it: 16rem on desktop and min(26rem, 55dvh)
 *  under `width <= 48rem`. Both were guesses, and the desktop one was the
 *  stingier of the two despite desktop having the most room — measured on the
 *  chat-actions menu at a 900px viewport, five 60px rows wanted 332px, the cap
 *  allowed 254px, and the card scrolled with 595px of free space above it.
 *
 *  The viewport bound also subsumes what the caps were FOR. The context card's
 *  metering section renders one row per unit kiro-cli reports, which is upstream
 *  data with no bound, so a card does need a ceiling — but the room above the
 *  pill IS that ceiling, and it is the honest one: the card takes the space that
 *  exists and scrolls only once it has run out. On a phone it additionally reads
 *  `visualViewport`, so a raised keyboard shrinks the cap for real where `55dvh`
 *  could only approximate it. */
function clampToViewport(pill: HTMLElement, card: HTMLElement): void {
  const slot = pill.parentElement;
  const width = card.offsetWidth;
  if (slot === null || width <= 0) {
    return;
  }
  const viewport = window.visualViewport;
  const viewportLeft = viewport?.offsetLeft ?? 0;
  const viewportRight = viewportLeft + (viewport?.width ?? window.innerWidth);
  const margin = popupViewportMargin(card);
  const minLeft = viewportLeft + margin;
  const maxLeft = Math.max(minLeft, viewportRight - margin - width);
  const naturalLeft = slot.getBoundingClientRect().left;
  const cardLeft = Math.min(Math.max(naturalLeft, minLeft), maxLeft);
  const pillRect = pill.getBoundingClientRect();

  card.style.setProperty("--pill-inline-shift", `${String(cardLeft - naturalLeft)}px`);
  card.style.setProperty(
    "--pill-origin-x",
    `${String(pillRect.left + pillRect.width / 2 - cardLeft)}px`,
  );

  // The card's own bottom rather than the pill's top, so the `--sp-1` gap between
  // them needs no second reader here. It is stable under the enter animation:
  // `transform-origin` is `bottom`, so the scale leaves that edge where it is.
  const viewportTop = viewport?.offsetTop ?? 0;
  const room = card.getBoundingClientRect().bottom - viewportTop - margin;
  card.style.setProperty(
    "--pill-max-block",
    `${String(Math.max(Math.round(room), MIN_CARD_BLOCK_PX))}px`,
  );
}

function popupViewportMargin(card: HTMLElement): number {
  const raw = getComputedStyle(card).getPropertyValue("--pill-viewport-margin").trim();
  const n = Number.parseFloat(raw);
  if (Number.isFinite(n)) {
    if (raw.endsWith("rem")) {
      const rootSize = Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
      return n * (Number.isFinite(rootSize) ? rootSize : 16);
    }
    if (raw.endsWith("px")) {
      return n;
    }
  }
  return 12;
}

export function collapseAll(): void {
  closePopupGroup(PILL_GROUP);
}
