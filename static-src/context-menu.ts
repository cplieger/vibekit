// ---------------------------------------------------------------------------
// Context menu — adopted from @cplieger/ui-primitives (popover).
//
// The hand-rolled positioned popup was replaced by the library's
// `createPopover` anchored at a viewport point via `pointAnchor(x, y)` — the
// canonical way to express a right-click menu. The controller owns
// dismiss-on-outside-click and Escape (Escape is isolated via stopPropagation,
// so a menu opened inside a modal consumes the key rather than also closing the
// modal), plus a real placement engine that flips/clamps the menu on-screen
// (the old copy could overflow the viewport edge). We keep the WAI-ARIA menu
// roles, the roving-tabindex arrow-key nav, and initial focus.
//
// Positioning is preserved: offset 0 + placement bottom/align start puts the
// menu's top-left exactly at the click point (as the old position:absolute
// left/top did), but via position:fixed against clientX/clientY — which is
// also correct under page scroll. Visuals are unchanged: the panel keeps its
// `.tab-context-menu` class (skinned in 12-tabs.css); the base adds only fixed
// positioning + the z-index (mapped to 999 in 04-uip-skin.css).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { createPopover, pointAnchor } from "@cplieger/ui-primitives/popover";
import { wireArrowNav } from "./arrow-nav.js";

export interface ContextMenuItem {
  label: string;
  action: () => void;
}

export interface ContextMenuPosition {
  x: number;
  y: number;
}

/** Show a context menu at the given viewport position. Dismisses on
 *  click-outside or Escape. Returns a function to programmatically dismiss. */
export function showContextMenu(
  items: ContextMenuItem[],
  position: ContextMenuPosition,
): () => void {
  const menu = el("div", { className: "tab-context-menu", role: "menu" });

  // The controller is created before the item handlers so they can close it.
  // It hosts the (disconnected) menu into <body> on show(), positions it, and
  // removes it from the DOM on close via onClose.
  const pop = createPopover(pointAnchor(position.x, position.y), menu, {
    placement: "bottom",
    align: "start",
    offset: 0,
    onClose: () => {
      menu.remove();
    },
  });

  for (const item of items) {
    const btn = el("button", { className: "tab-context-item", role: "menuitem" }, item.label);
    btn.addEventListener("click", () => {
      pop.hide();
      item.action();
    });
    menu.appendChild(btn);
  }

  // Roving-tabindex arrow-key navigation (Up/Down/Home/End, Enter/Space to
  // activate) makes the menu fully keyboard-operable per the WAI-ARIA menu
  // pattern; complements the initial focus below.
  wireArrowNav(menu, ".tab-context-item");

  pop.show();

  // Focus the first item for keyboard accessibility (the popover leaves focus
  // to the caller by default).
  menu.querySelector<HTMLButtonElement>("button")?.focus();

  return () => {
    pop.hide();
  };
}
