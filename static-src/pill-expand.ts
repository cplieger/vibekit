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

export function collapseAll(): void {
  closePopupGroup(PILL_GROUP);
}
