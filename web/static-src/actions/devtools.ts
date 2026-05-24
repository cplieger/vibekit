// Devtools overlay: Ctrl+Shift+A toggles a bottom-right panel that
// visualizes the action registry's recent log. Each row shows status
// (pending/success/error/cancelled), action name, duration, and
// truncated args/error. Subscribes to the registry so live actions
// appear in real time.
//
// Off-screen by default. Toggling re-renders from recentLog() and
// keeps live-updating until toggled off. The panel is positioned
// above the toast stack and is keyboard-dismissable (Escape).
//
// Initialize once at app startup via initDevtoolsOverlay(). The
// shortcut is wired globally on document; pressing it toggles the
// panel without dispatching any actions of its own.
// ---------------------------------------------------------------------------

import type { ActionInstance, ActionStatus } from "./types.js";
import { recentLog, subscribe } from "./registry.js";

let panelEl: HTMLDivElement | null = null;
let listEl: HTMLDivElement | null = null;
let unsubscribe: (() => void) | null = null;
let initialized = false;
let shortcutHandler: ((e: KeyboardEvent) => void) | null = null;

const MAX_RENDERED = 100;

const STATUS_COLORS: Record<ActionStatus, string> = {
  pending:   "var(--c-text-tertiary)",
  success:   "var(--c-green)",
  error:     "var(--c-red)",
  cancelled: "var(--c-text-tertiary)",
};

const STATUS_GLYPHS: Record<ActionStatus, string> = {
  pending:   "○",
  success:   "✓",
  error:     "✗",
  cancelled: "⊘",
};

/** Wire the global Ctrl+Shift+A shortcut. Call once at app init. */
export function initDevtoolsOverlay(): void {
  if (initialized) return;
  initialized = true;
  shortcutHandler = (e) => {
    // Ctrl+Shift+A (or Meta+Shift+A on macOS). Skipping inputs so
    // text fields don't intercept.
    if (!(e.ctrlKey || e.metaKey)) return;
    if (!e.shiftKey) return;
    if (e.key.toLowerCase() !== "a") return;
    const target = e.target;
    if (target instanceof HTMLElement) {
      const tag = target.tagName.toLowerCase();
      if (tag === "input" || tag === "textarea" || target.isContentEditable) return;
    }
    e.preventDefault();
    toggle();
  };
  document.addEventListener("keydown", shortcutHandler);
}

/** Show / hide the overlay. Exposed for the keyboard shortcut wiring
 *  and for programmatic control (e.g. a "/debug actions" slash command
 *  in the future). */
export function toggle(): void {
  if (panelEl !== null) {
    teardown();
    return;
  }
  mount();
}

function mount(): void {
  panelEl = document.createElement("div");
  panelEl.className = "vk-devtools-overlay";
  panelEl.setAttribute("role", "dialog");
  panelEl.setAttribute("aria-label", "Action devtools");

  // Header with a close button + clear count.
  const header = document.createElement("div");
  header.className = "vk-devtools-header";

  const title = document.createElement("span");
  title.className = "vk-devtools-title";
  title.textContent = "Actions";
  header.appendChild(title);

  const closeBtn = document.createElement("button");
  closeBtn.type = "button";
  closeBtn.className = "vk-devtools-close";
  closeBtn.textContent = "×";
  closeBtn.setAttribute("aria-label", "Close devtools");
  closeBtn.addEventListener("click", teardown);
  header.appendChild(closeBtn);

  panelEl.appendChild(header);

  listEl = document.createElement("div");
  listEl.className = "vk-devtools-list";
  panelEl.appendChild(listEl);

  document.body.appendChild(panelEl);

  // Initial paint from the existing log.
  rerender();

  // Live updates.
  unsubscribe = subscribe(() => rerender());

  // Escape closes the panel.
  document.addEventListener("keydown", onEscape);
}

function teardown(): void {
  if (panelEl === null) return;
  document.removeEventListener("keydown", onEscape);
  unsubscribe?.();
  unsubscribe = null;
  panelEl.remove();
  panelEl = null;
  listEl = null;
}

function onEscape(e: KeyboardEvent): void {
  if (e.key === "Escape" && panelEl !== null) {
    e.preventDefault();
    teardown();
  }
}

function rerender(): void {
  if (listEl === null) return;
  const log = recentLog().slice(-MAX_RENDERED).reverse();  // newest first
  const frag = document.createDocumentFragment();
  for (const inst of log) {
    frag.appendChild(buildRow(inst));
  }
  listEl.replaceChildren(frag);
}

function buildRow(inst: ActionInstance): HTMLDivElement {
  const row = document.createElement("div");
  row.className = `vk-devtools-row vk-devtools-row-${inst.status}`;

  const glyph = document.createElement("span");
  glyph.className = "vk-devtools-glyph";
  glyph.textContent = STATUS_GLYPHS[inst.status];
  glyph.style.color = STATUS_COLORS[inst.status];
  row.appendChild(glyph);

  const name = document.createElement("span");
  name.className = "vk-devtools-name";
  name.textContent = inst.name;
  row.appendChild(name);

  const duration = document.createElement("span");
  duration.className = "vk-devtools-duration";
  if (inst.completedAt !== undefined) {
    duration.textContent = `${String(inst.completedAt - inst.startedAt)}ms`;
  } else if (inst.status === "pending") {
    duration.textContent = "…";
  }
  row.appendChild(duration);

  // Truncated detail: error message for failures, JSON-stringified
  // args otherwise. Capped at 80 chars to keep rows compact.
  const detail = document.createElement("span");
  detail.className = "vk-devtools-detail";
  if (inst.error !== undefined) {
    detail.textContent = inst.error.message;
    detail.style.color = "var(--c-red)";
  } else {
    detail.textContent = stringifyArgs(inst.args);
  }
  row.appendChild(detail);

  return row;
}

function stringifyArgs(args: unknown): string {
  if (args === undefined || args === null) return "";
  try {
    const s = JSON.stringify(args, replaceCircular(), 0);
    return s.length > 80 ? s.slice(0, 77) + "..." : s;
  } catch {
    return String(args).slice(0, 80);
  }
}

/** JSON.stringify replacer that breaks cycles + omits DOM nodes
 *  (which are common in optimistic-UI args and would either crash
 *  the stringify or produce massive output). */
function replaceCircular(): (key: string, value: unknown) => unknown {
  const seen = new WeakSet<object>();
  return (_key, value) => {
    if (typeof value === "object" && value !== null) {
      if (value instanceof HTMLElement) return `<${value.tagName.toLowerCase()}>`;
      if (seen.has(value)) return "[Circular]";
      seen.add(value);
    }
    return value;
  };
}

/** Test-only: reset internal state so a fresh module load starts clean. */
export function _resetForTest(): void {
  teardown();
  if (shortcutHandler !== null) {
    document.removeEventListener("keydown", shortcutHandler);
    shortcutHandler = null;
  }
  initialized = false;
}
