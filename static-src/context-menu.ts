// ---------------------------------------------------------------------------
// Context menu: reusable positioned popup with dismiss-on-click-outside,
// Escape key, focus management, and ARIA roles.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { wireArrowNav } from "./arrow-nav.js";

export interface ContextMenuItem {
  label: string;
  action: () => void;
}

export interface ContextMenuPosition {
  x: number;
  y: number;
}

/** Show a context menu at the given position. Dismisses on click-outside
 *  or Escape. Returns a cleanup function to programmatically dismiss. */
export function showContextMenu(
  items: ContextMenuItem[],
  position: ContextMenuPosition,
): () => void {
  const menu = el("div", { className: "tab-context-menu", role: "menu" });
  menu.style.position = "absolute";
  menu.style.left = `${position.x}px`;
  menu.style.top = `${position.y}px`;

  for (const item of items) {
    const btn = el("button", { className: "tab-context-item", role: "menuitem" }, item.label);
    btn.addEventListener("click", () => {
      dismiss();
      item.action();
    });
    menu.appendChild(btn);
  }

  document.body.appendChild(menu);

  // Roving-tabindex arrow-key navigation (Up/Down/Home/End, Enter/Space to
  // activate) makes the menu fully keyboard-operable per the WAI-ARIA menu
  // pattern; complements the initial focus below.
  wireArrowNav(menu, ".tab-context-item");

  // Focus first item for keyboard accessibility.
  const first = menu.querySelector<HTMLButtonElement>("button");
  if (first) {
    first.focus();
  }

  function dismiss(): void {
    menu.remove();
    document.removeEventListener("pointerdown", onPointerDown);
    document.removeEventListener("keydown", onKeyDown);
  }

  function onPointerDown(): void {
    dismiss();
  }

  function onKeyDown(ev: KeyboardEvent): void {
    if (ev.key === "Escape") {
      dismiss();
    }
  }

  setTimeout(() => {
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
  }, 0);

  return dismiss;
}
