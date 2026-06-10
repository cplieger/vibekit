// ---------------------------------------------------------------------------
// Lightweight popover menu used by the source-control toolbar's
// overflow "⋯" trigger. No new dependencies — pure DOM and a single
// outside-click + Escape handler.
//
// TODO(git-toolbar): Wire into git toolbar — currently only imported by tests.
//
// Items are passed in declaratively so different surfaces can share
// the component (today: git toolbar; future: repo-picker row menu,
// PR-row menu).
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

export interface OverflowMenuItem {
  /** Stable identifier — also used as data-overflow-item attribute. */
  id: string;
  label: string;
  /** When true, render with a danger style. */
  danger?: boolean;
  /** When true, the item is rendered as disabled and clicks are
   *  ignored. */
  disabled?: boolean;
  onSelect(): void;
}

interface OpenedMenu {
  root: HTMLElement;
  trigger: HTMLElement;
  cleanup: () => void;
}

let opened: OpenedMenu | null = null;

/** Open an overflow menu anchored to `trigger`. Closes any previously
 *  open menu and installs an outside-click + Escape close. */
export function openOverflowMenu(trigger: HTMLElement, items: readonly OverflowMenuItem[]): void {
  closeOverflowMenu();

  const root = el("div", {
    className: "overflow-menu",
    role: "menu",
    "aria-label": "Actions",
  });

  for (const item of items) {
    const btn = el(
      "button",
      {
        type: "button",
        className: "overflow-menu-item",
        "data-overflow-item": item.id,
        role: "menuitem",
        disabled: item.disabled,
      },
      item.label,
    );
    if (item.danger === true) {
      btn.classList.add("overflow-menu-item-danger");
    }
    if (item.disabled === true) {
      btn.setAttribute("aria-disabled", "true");
    }
    btn.addEventListener("click", () => {
      if (item.disabled === true) {
        return;
      }
      closeOverflowMenu();
      item.onSelect();
    });
    root.appendChild(btn);
  }

  // Position the menu under-right of the trigger so it sits in the
  // toolbar's typical reading direction. Caller can override by
  // restyling .overflow-menu in their own CSS file if needed.
  const rect = trigger.getBoundingClientRect();
  root.style.position = "fixed";
  root.style.top = `${rect.bottom + 4}px`;
  // Prefer right-aligning the menu to the trigger so wider menus
  // don't escape the right edge of the viewport on smaller screens.
  root.style.right = `${Math.max(0, window.innerWidth - rect.right)}px`;

  document.body.appendChild(root);

  const onDocumentClick = (e: MouseEvent): void => {
    const target = e.target as Node | null;
    if (target === null) {
      return;
    }
    if (root.contains(target)) {
      return;
    }
    if (trigger.contains(target)) {
      return;
    }
    closeOverflowMenu();
  };
  const onKey = (e: KeyboardEvent): void => {
    if (e.key === "Escape") {
      e.stopPropagation();
      closeOverflowMenu();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const items = root.querySelectorAll<HTMLButtonElement>(
        ".overflow-menu-item:not([aria-disabled='true'])",
      );
      if (items.length === 0) {
        return;
      }
      const current = root.querySelector<HTMLButtonElement>(".overflow-menu-item:focus");
      let idx = current !== null ? Array.from(items).indexOf(current) : -1;
      idx =
        e.key === "ArrowDown" ? (idx + 1) % items.length : (idx - 1 + items.length) % items.length;
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      items[idx]!.focus();
    }
  };
  const onScrollOrResize = (): void => {
    closeOverflowMenu();
  };

  // Attach keydown immediately — Escape doesn't need deferral.
  document.addEventListener("keydown", onKey);

  // Defer outside-click attach to next tick so the click that opened
  // the menu doesn't immediately close it (the click still bubbles
  // up to document).
  const deferTimer = setTimeout(() => {
    document.addEventListener("click", onDocumentClick);
  }, 0);

  document.addEventListener("scroll", onScrollOrResize, true);
  window.addEventListener("resize", onScrollOrResize);
  trigger.setAttribute("aria-haspopup", "menu");
  trigger.setAttribute("aria-expanded", "true");

  const cleanup = (): void => {
    clearTimeout(deferTimer);
    document.removeEventListener("click", onDocumentClick);
    document.removeEventListener("keydown", onKey);
    document.removeEventListener("scroll", onScrollOrResize, true);
    window.removeEventListener("resize", onScrollOrResize);
    trigger.setAttribute("aria-expanded", "false");
    root.remove();
  };

  opened = { root, trigger, cleanup };

  // Focus the first non-disabled item for keyboard users.
  const firstEnabled = root.querySelector<HTMLButtonElement>(
    ".overflow-menu-item:not([aria-disabled='true'])",
  );
  firstEnabled?.focus();
}

/** Close any currently-open overflow menu. No-op if none open. */
export function closeOverflowMenu(): void {
  if (opened === null) {
    return;
  }
  const o = opened;
  opened = null;
  o.cleanup();
  o.trigger.focus();
}

/** Test seam: report whether a menu is currently open. */
export function _isOverflowMenuOpen(): boolean {
  return opened !== null;
}
