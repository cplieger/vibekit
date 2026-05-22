// ---------------------------------------------------------------------------
// Tab drag-to-reorder interaction (Pointer Events, unified mouse + touch).
// Extracted from tabs.ts — self-contained subsystem that calls back into the
// tab store only via the reorderTabs callback at the end of a drag.
// ---------------------------------------------------------------------------

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
  private readonly boundDragMove = (e: PointerEvent): void => { this.onDragMove(e); };
  private readonly boundDragEnd = (): void => { this.onDragEnd(); };

  /** Whether a drag interaction just completed — used to suppress the
   *  pointerup click that would otherwise fire on the tab element. */
  isDragHandled(): boolean { return this.dragHandled; }

  /** Set the callback invoked when a drag completes with the new tab order. */
  setReorderCallback(fn: (order: string[]) => void): void {
    this.reorderCallback = fn;
  }

  /** Attach drag-to-reorder behavior to a tab element. */
  attachDrag(el: HTMLElement): void {
    let downY = 0;
    let downTimer: ReturnType<typeof setTimeout> | null = null;
    let armed = false;

    el.addEventListener("pointerdown", (e) => {
      if ((e.target as HTMLElement).closest(".tab-close") !== null) return;
      if (!e.isPrimary) return;
      downY = e.clientY;
      armed = true;
      if (e.pointerType === "touch") {
        downTimer = setTimeout(() => {
          if (armed) this.startDrag(el, e.clientY, e.pointerId);
        }, DRAG_HOLD_MS);
      }
    });

    el.addEventListener("pointermove", (e) => {
      if (!armed || this.dragEl !== null) return;
      if (e.pointerType !== "touch" && Math.abs(e.clientY - downY) > DRAG_THRESHOLD_PX) {
        this.startDrag(el, e.clientY, e.pointerId);
      }
    });

    el.addEventListener("pointerup", () => {
      armed = false;
      if (downTimer !== null) { clearTimeout(downTimer); downTimer = null; }
    });
    el.addEventListener("pointercancel", () => {
      armed = false;
      if (downTimer !== null) { clearTimeout(downTimer); downTimer = null; }
    });
  }

  private startDrag(el: HTMLElement, clientY: number, pointerId: number): void {
    this.dragEl = el;
    const rect = el.getBoundingClientRect();
    this.dragOffsetY = clientY - rect.top;

    this.dragGhost = el.cloneNode(true) as HTMLElement;
    this.dragGhost.classList.add("tab-drag-ghost");
    this.dragGhost.style.width = `${rect.width}px`;
    this.dragGhost.style.left = `${rect.left}px`;
    this.dragGhost.style.top = `${rect.top}px`;
    document.body.appendChild(this.dragGhost);

    this.dragIndicator = document.createElement("div");
    this.dragIndicator.className = "tab-drag-indicator";
    el.insertAdjacentElement("afterend", this.dragIndicator);
    this.dragTargetIdx = [...(el.parentElement?.children ?? [])].filter(
      (c) => c !== el && c !== this.dragIndicator,
    ).indexOf(this.dragIndicator.nextElementSibling as Element);
    if (this.dragTargetIdx === -1) {
      this.dragTargetIdx = [...(el.parentElement?.children ?? [])].filter(
        (c) => c !== el && c !== this.dragIndicator,
      ).length;
    }

    el.classList.add("tab-drag-placeholder");
    el.parentElement?.classList.add("dragging");

    el.setPointerCapture(pointerId);
    el.addEventListener("pointermove", this.boundDragMove);
    el.addEventListener("pointerup", this.boundDragEnd);
    el.addEventListener("pointercancel", this.boundDragEnd);
    document.body.style.userSelect = "none";
  }

  private onDragMove(e: PointerEvent): void {
    if (this.dragEl === null || this.dragGhost === null) return;
    this.dragGhost.style.top = `${e.clientY - this.dragOffsetY}px`;

    const list = this.dragEl.parentElement;
    if (list === null) return;
    const siblings = [...list.children].filter(
      (c) => c !== this.dragEl && c !== this.dragIndicator,
    ) as HTMLElement[];

    let target = siblings.length;
    for (let i = 0; i < siblings.length; i++) {
      const r = siblings[i]!.getBoundingClientRect();
      if (e.clientY < r.top + r.height / 2) { target = i; break; }
    }

    if (target === this.dragTargetIdx && this.dragIndicator !== null && this.dragIndicator.isConnected) return;

    const firstRects = new Map<HTMLElement, DOMRect>();
    for (const s of siblings) firstRects.set(s, s.getBoundingClientRect());

    this.dragTargetIdx = target;

    if (this.dragIndicator === null) {
      this.dragIndicator = document.createElement("div");
      this.dragIndicator.className = "tab-drag-indicator";
    }
    if (target >= siblings.length) list.appendChild(this.dragIndicator);
    else list.insertBefore(this.dragIndicator, siblings[target]!);

    for (const s of siblings) {
      const first = firstRects.get(s);
      if (first === undefined) continue;
      const last = s.getBoundingClientRect();
      const dy = first.top - last.top;
      if (dy === 0) continue;
      s.style.transform = `translateY(${String(dy)}px)`;
      s.style.transition = "none";
      requestAnimationFrame(() => {
        s.style.transform = "";
        s.style.transition = `transform 200ms var(--ease-standard)`;
      });
    }
  }

  private onDragEnd(): void {
    if (this.dragEl === null) return;
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
      const newOrder = [...list.children]
        .map((c) => (c as HTMLElement).dataset["tabId"] ?? "")
        .filter((v) => v !== "");
      this.reorderCallback?.(newOrder);
    }

    this.dragEl.removeEventListener("pointermove", this.boundDragMove);
    this.dragEl.removeEventListener("pointerup", this.boundDragEnd);
    this.dragEl.removeEventListener("pointercancel", this.boundDragEnd);
    this.dragEl.parentElement?.classList.remove("dragging");
    document.body.style.userSelect = "";
    this.dragEl = null;
    this.dragTargetIdx = -1;
    this.dragHandled = true;
    setTimeout(() => { this.dragHandled = false; }, 80);
  }
}

// ---------------------------------------------------------------------------
// Singleton instance + function exports that form the module's public API.
// ---------------------------------------------------------------------------

const instance = new TabDragController();

/** Whether a drag interaction just completed — used to suppress the
 *  pointerup click that would otherwise fire on the tab element. */
export function isDragHandled(): boolean { return instance.isDragHandled(); }

/** Set the callback invoked when a drag completes with the new tab order. */
export function setReorderCallback(fn: (order: string[]) => void): void {
  instance.setReorderCallback(fn);
}

/** Attach drag-to-reorder behavior to a tab element. */
export function attachDrag(el: HTMLElement): void {
  instance.attachDrag(el);
}
