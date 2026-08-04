// ---------------------------------------------------------------------------
// Tab drag-to-reorder interaction (Pointer Events, unified mouse + touch).
// Extracted from tabs.ts — self-contained subsystem that calls back into the
// tab store only via the reorderTabs callback at the end of a drag.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";

const DRAG_THRESHOLD_PX = 5;
const DRAG_HOLD_MS = 300;

class TabDragController {
  private dragEl: HTMLElement | null = null;
  private dragGhost: HTMLElement | null = null;
  private dragIndicator: HTMLElement | null = null;
  private dragOffsetY = 0;
  private dragTargetIdx = -1;
  private dragHandled = false;
  private reorderCallback: ((order: string[]) => void) | null = null;

  // Bound handlers for add/removeEventListener identity.
  private readonly boundDragMove = (e: PointerEvent): void => {
    this.onDragMove(e);
  };
  private readonly boundDragEnd = (): void => {
    this.onDragEnd();
  };

  /** Whether a drag interaction just completed — used to suppress the
   *  pointerup click that would otherwise fire on the tab element. */
  isDragHandled(): boolean {
    return this.dragHandled;
  }

  /** Set the callback invoked when a drag completes with the new tab order. */
  setReorderCallback(fn: (order: string[]) => void): void {
    this.reorderCallback = fn;
  }

  /** Attach drag-to-reorder behavior to a tab element. */
  attachDrag(tabEl: HTMLElement): void {
    let downY = 0;
    let downTimer: ReturnType<typeof setTimeout> | null = null;
    let armed = false;

    tabEl.addEventListener("pointerdown", (e) => {
      if ((e.target as HTMLElement).closest(".tab-close") !== null) {
        return;
      }
      if (!e.isPrimary) {
        return;
      }
      downY = e.clientY;
      armed = true;
      if (e.pointerType === "touch") {
        downTimer = setTimeout(() => {
          if (armed) {
            this.startDrag(tabEl, e.clientY, e.pointerId);
          }
        }, DRAG_HOLD_MS);
      }
    });

    tabEl.addEventListener("pointermove", (e) => {
      if (!armed || this.dragEl !== null) {
        return;
      }
      if (e.pointerType !== "touch" && Math.abs(e.clientY - downY) > DRAG_THRESHOLD_PX) {
        this.startDrag(tabEl, e.clientY, e.pointerId);
      }
    });

    tabEl.addEventListener("pointerup", () => {
      armed = false;
      if (downTimer !== null) {
        clearTimeout(downTimer);
        downTimer = null;
      }
    });
    tabEl.addEventListener("pointercancel", () => {
      armed = false;
      if (downTimer !== null) {
        clearTimeout(downTimer);
        downTimer = null;
      }
    });
  }

  /** The rows a drag reorders: everything except the dragged row, the drop
   *  indicator, and any sub-tab collapsed for the duration of the drag.
   *
   *  Excluding collapsed children is not cosmetic. The indicator's target index
   *  is a position in THIS list and the dropped order is read back from it, so a
   *  hidden child left in the list would contribute a zero-height rect that can
   *  never be hit and an id at a position nothing can be dropped into. */
  private draggableSiblings(list: HTMLElement): HTMLElement[] {
    return [...list.children].filter(
      (c) =>
        c !== this.dragEl &&
        c !== this.dragIndicator &&
        !(c as HTMLElement).hasAttribute("data-drag-collapsed"),
    ) as HTMLElement[];
  }

  /** Fold sub-tabs into their parent for the duration of the drag, and unfold on
   *  drop. Without this, dragging a parent past its own children reads as
   *  chaos — the children do not move with it, because their position is derived
   *  rather than dragged. */
  private setChildrenCollapsed(list: HTMLElement | null, on: boolean): void {
    for (const c of list?.querySelectorAll<HTMLElement>(".tab-child") ?? []) {
      if (on) {
        c.dataset["dragCollapsed"] = "";
      } else {
        delete c.dataset["dragCollapsed"];
      }
    }
  }

  private startDrag(tabEl: HTMLElement, clientY: number, pointerId: number): void {
    this.dragEl = tabEl;
    this.setChildrenCollapsed(tabEl.parentElement, true);
    const rect = tabEl.getBoundingClientRect();
    this.dragOffsetY = clientY - rect.top;

    this.dragGhost = tabEl.cloneNode(true) as HTMLElement;
    this.dragGhost.classList.add("tab-drag-ghost");
    this.dragGhost.style.width = `${rect.width}px`;
    this.dragGhost.style.left = `${rect.left}px`;
    this.dragGhost.style.top = `${rect.top}px`;
    document.body.appendChild(this.dragGhost);

    this.dragIndicator = el("div", { className: "tab-drag-indicator" });
    tabEl.insertAdjacentElement("afterend", this.dragIndicator);
    const list0 = tabEl.parentElement;
    const siblings0 = list0 === null ? [] : this.draggableSiblings(list0);
    this.dragTargetIdx = siblings0.indexOf(this.dragIndicator.nextElementSibling as HTMLElement);
    if (this.dragTargetIdx === -1) {
      this.dragTargetIdx = siblings0.length;
    }

    tabEl.classList.add("tab-drag-placeholder");
    tabEl.parentElement?.classList.add("dragging");

    tabEl.setPointerCapture(pointerId);
    tabEl.addEventListener("pointermove", this.boundDragMove);
    tabEl.addEventListener("pointerup", this.boundDragEnd);
    tabEl.addEventListener("pointercancel", this.boundDragEnd);
    document.body.style.userSelect = "none";
  }

  private onDragMove(e: PointerEvent): void {
    if (this.dragEl === null || this.dragGhost === null) {
      return;
    }
    this.dragGhost.style.top = `${e.clientY - this.dragOffsetY}px`;

    const list = this.dragEl.parentElement;
    if (list === null) {
      return;
    }
    const siblings = this.draggableSiblings(list);

    let target = siblings.length;
    for (let i = 0; i < siblings.length; i++) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const r = siblings[i]!.getBoundingClientRect();
      if (e.clientY < r.top + r.height / 2) {
        target = i;
        break;
      }
    }

    if (target === this.dragTargetIdx && this.dragIndicator?.isConnected) {
      return;
    }

    const firstRects = new Map<HTMLElement, DOMRect>();
    for (const s of siblings) {
      firstRects.set(s, s.getBoundingClientRect());
    }

    this.dragTargetIdx = target;

    this.dragIndicator ??= el("div", { className: "tab-drag-indicator" });
    if (target >= siblings.length) {
      list.appendChild(this.dragIndicator);
    } else {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      list.insertBefore(this.dragIndicator, siblings[target]!);
    }

    for (const s of siblings) {
      const first = firstRects.get(s);
      if (first === undefined) {
        continue;
      }
      const last = s.getBoundingClientRect();
      const dy = first.top - last.top;
      if (dy === 0) {
        continue;
      }
      s.style.transform = `translateY(${String(dy)}px)`;
      s.style.transition = "none";
      requestAnimationFrame(() => {
        s.style.transform = "";
        s.style.transition = `transform 200ms var(--ease-standard)`;
      });
    }
  }

  private onDragEnd(): void {
    if (this.dragEl === null) {
      return;
    }
    const list = this.dragEl.parentElement;

    if (this.dragIndicator !== null && list !== null) {
      list.insertBefore(this.dragEl, this.dragIndicator);
      this.dragIndicator.remove();
      this.dragIndicator = null;
    }
    this.dragEl.classList.remove("tab-drag-placeholder");
    this.dragGhost?.remove();
    this.dragGhost = null;

    if (list !== null) {
      for (const child of list.children) {
        const c = child as HTMLElement;
        c.style.transform = "";
        c.style.transition = "";
      }
      // Read the order back BEFORE unfolding, so it is top-level ids only —
      // which is exactly what the tab store persists and what reorderTabs
      // re-anchors children against.
      const newOrder = [...list.children]
        .filter((c) => !(c as HTMLElement).hasAttribute("data-drag-collapsed"))
        .map((c) => (c as HTMLElement).dataset["tabId"] ?? "")
        .filter((v) => v !== "");
      this.setChildrenCollapsed(list, false);
      this.reorderCallback?.(newOrder);
    } else {
      this.setChildrenCollapsed(list, false);
    }

    this.dragEl.removeEventListener("pointermove", this.boundDragMove);
    this.dragEl.removeEventListener("pointerup", this.boundDragEnd);
    this.dragEl.removeEventListener("pointercancel", this.boundDragEnd);
    this.dragEl.parentElement?.classList.remove("dragging");
    document.body.style.userSelect = "";
    this.dragEl = null;
    this.dragTargetIdx = -1;
    this.dragHandled = true;
    setTimeout(() => {
      this.dragHandled = false;
    }, 80);
  }
}

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const instance = new TabDragController();

/** Whether a drag interaction just completed — used to suppress the
 *  pointerup click that would otherwise fire on the tab element. */
export function isDragHandled(): boolean {
  return instance.isDragHandled();
}

/** Set the callback invoked when a drag completes with the new tab order. */
export function setReorderCallback(fn: (order: string[]) => void): void {
  instance.setReorderCallback(fn);
}

/** Attach drag-to-reorder behavior to a tab element. */
export function attachDrag(tabEl: HTMLElement): void {
  instance.attachDrag(tabEl);
}
