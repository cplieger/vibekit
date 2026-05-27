// ---------------------------------------------------------------------------
// Expandable pills: click/keyboard to expand a pill into a floating
// detail card anchored to the pill's position. Only one pill can be
// expanded at a time. Click outside, click the pill, or press Escape
// to collapse.
// ---------------------------------------------------------------------------

const activePills = new Set<HTMLElement>();

export function makeExpandable(
  pill: HTMLElement,
  contentEl: HTMLElement,
  opts?: { onExpand?: () => void; onCollapse?: () => void; signal?: AbortSignal },
): void {
  const listenerOpts = opts?.signal !== undefined ? { signal: opts.signal } : undefined;

  pill.addEventListener(
    "click",
    (e: MouseEvent) => {
      const target = e.target as HTMLElement;
      if (contentEl.contains(target) && target !== contentEl) {
        return;
      }
      togglePill(pill, contentEl, opts);
    },
    listenerOpts,
  );

  // Keyboard: Enter/Space to toggle, Escape to collapse.
  pill.addEventListener(
    "keydown",
    (e: KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        togglePill(pill, contentEl, opts);
      } else if (e.key === "Escape" && pill.classList.contains("pill-expanded")) {
        e.preventDefault();
        collapse(pill, contentEl, opts?.onCollapse);
      }
    },
    listenerOpts,
  );

  // Close on outside click.
  document.addEventListener(
    "click",
    (e: MouseEvent) => {
      if (!pill.classList.contains("pill-expanded")) {
        return;
      }
      if (pill.contains(e.target as Node)) {
        return;
      }
      collapse(pill, contentEl, opts?.onCollapse);
    },
    listenerOpts,
  );

  // Close on Escape anywhere.
  document.addEventListener(
    "keydown",
    (e: KeyboardEvent) => {
      if (e.key === "Escape" && pill.classList.contains("pill-expanded")) {
        collapse(pill, contentEl, opts?.onCollapse);
      }
    },
    listenerOpts,
  );
}

function togglePill(
  pill: HTMLElement,
  contentEl: HTMLElement,
  opts?: { onExpand?: () => void; onCollapse?: () => void },
): void {
  if (pill.classList.contains("pill-expanded")) {
    collapse(pill, contentEl, opts?.onCollapse);
  } else {
    for (const other of activePills) {
      const otherContent = other.querySelector(".pill-expand-content");
      if (otherContent !== null) {
        collapse(other, otherContent);
      }
    }
    expand(pill, contentEl, opts?.onExpand);
  }
}

function expand(pill: HTMLElement, contentEl: HTMLElement, onExpand?: () => void): void {
  pill.classList.add("pill-expanded");
  contentEl.classList.remove("hidden");
  activePills.add(pill);
  void contentEl.offsetHeight; // force reflow
  contentEl.classList.add("pill-expand-visible");
  onExpand?.();
}

// Generation counter per pill to guard against rapid toggle races.
const collapseGen = new WeakMap<HTMLElement, number>();

function collapse(pill: HTMLElement, contentEl: HTMLElement, onCollapse?: () => void): void {
  pill.classList.remove("pill-expanded");
  contentEl.classList.remove("pill-expand-visible");
  activePills.delete(pill);

  const gen = (collapseGen.get(pill) ?? 0) + 1;
  collapseGen.set(pill, gen);

  contentEl.addEventListener(
    "transitionend",
    () => {
      // Only hide if this is still the latest collapse (no re-expand happened).
      if (collapseGen.get(pill) === gen && !pill.classList.contains("pill-expanded")) {
        contentEl.classList.add("hidden");
      }
    },
    { once: true },
  );
  onCollapse?.();
}

export function collapseAll(): void {
  for (const pill of activePills) {
    const contentEl = pill.querySelector(".pill-expand-content");
    if (contentEl !== null) {
      collapse(pill, contentEl);
    }
  }
}
