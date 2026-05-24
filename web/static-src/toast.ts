// Ephemeral toast notifications: bottom-right stacked notifications
// with max-visible queueing, pause-on-hover/focus, progress bar,
// sticky errors, keyboard dismissal, and prefers-reduced-motion
// support.
//
// Usage:
//   info("Saved");
//   success("Cloned 3 repos");
//   error("Couldn't delete: refusing top-level directory");  // sticky
//   showToast("Reloading...", "info", 2000);                 // explicit duration
//
// All variants return a manual-dismiss function.
//
// Design principles (industry consensus + WCAG):
//   - role="status" + aria-live="polite" on the stack: announce
//     without interrupting focus (WCAG SC 4.1.3 Status Messages).
//   - Errors default to duration=0 (sticky) since users typically
//     need time to read and may need to act on them. Toasts can
//     always be dismissed by click or by pressing Escape.
//   - Pause-on-hover and pause-on-focus pause the auto-dismiss
//     timer and freeze the progress bar (WCAG SC 2.2.1 Timing
//     Adjustable).
//   - Max 3 visible at once; further toasts queue and promote on
//     dismiss to prevent notification floods.
//   - Progress bar at the bottom of timed toasts gives a visual
//     countdown of remaining time.
//   - Respects prefers-reduced-motion: skips slide/scale animations
//     and the progress bar, keeps opacity-only transitions.
//   - Toasts must NOT be the only surface for critical errors.
//     Pair with a banner or inline message when the user must act.
//
// Not for: blocking errors, time-sensitive content, batch updates,
// or anything requiring user input. For per-chat persistent state
// use banner-stack.ts; for inline form errors use the form's own
// error surface.
// ---------------------------------------------------------------------------

export type ToastLevel = "info" | "success" | "error";

const MAX_VISIBLE = 3;
const DURATION_DEFAULT_MS = 4000;
const DURATION_ERROR_MS = 0; // sticky

interface ToastEntry {
  el: HTMLDivElement;
  level: ToastLevel;
  duration: number;
  remaining: number;
  startedAt: number;
  timerId: ReturnType<typeof setTimeout> | null;
  progressEl: HTMLSpanElement | null;
  dismissed: boolean;
}

interface QueuedToast {
  message: string;
  level: ToastLevel;
  duration: number;
  resolve: (dismissFn: () => void) => void;
}

const visible: ToastEntry[] = [];
const queue: QueuedToast[] = [];
let containerEl: HTMLDivElement | null = null;
let escHandlerInstalled = false;

function ensureContainer(): HTMLDivElement {
  if (containerEl !== null) return containerEl;
  const stack = document.createElement("div");
  stack.className = "vk-toast-stack";
  // role=status + aria-live=polite: announce without interrupting.
  // aria-atomic=false: only announce the new addition, not the whole
  // stack contents on every change.
  stack.setAttribute("role", "status");
  stack.setAttribute("aria-live", "polite");
  stack.setAttribute("aria-atomic", "false");
  document.body.appendChild(stack);
  containerEl = stack;
  installEscHandler();
  return stack;
}

function installEscHandler(): void {
  if (escHandlerInstalled) return;
  escHandlerInstalled = true;
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") return;
    // Dismiss the most recently added toast (LIFO) so repeated Esc
    // tears down the stack from newest to oldest.
    const newest = visible[visible.length - 1];
    if (newest === undefined) return;
    dismiss(newest);
  });
}

function defaultDuration(level: ToastLevel): number {
  return level === "error" ? DURATION_ERROR_MS : DURATION_DEFAULT_MS;
}

function startTimer(t: ToastEntry): void {
  if (t.duration <= 0 || t.remaining <= 0) return;
  t.startedAt = Date.now();
  t.timerId = setTimeout(() => dismiss(t), t.remaining);
  if (t.progressEl !== null) {
    // Reset transition then animate width to 0 over `remaining`.
    t.progressEl.style.transitionDuration = "0ms";
    // Force layout so the next change actually transitions.
    void t.progressEl.offsetWidth;
    t.progressEl.style.transitionDuration = `${String(t.remaining)}ms`;
    requestAnimationFrame(() => {
      if (t.progressEl !== null) t.progressEl.style.width = "0%";
    });
  }
}

function pauseTimer(t: ToastEntry): void {
  if (t.timerId === null || t.duration <= 0) return;
  clearTimeout(t.timerId);
  t.timerId = null;
  const elapsed = Date.now() - t.startedAt;
  t.remaining = Math.max(0, t.remaining - elapsed);
  if (t.progressEl !== null) {
    // Freeze the progress bar at its current visual width.
    const cs = window.getComputedStyle(t.progressEl);
    t.progressEl.style.transitionDuration = "0ms";
    t.progressEl.style.width = cs.width;
  }
}

function resumeTimer(t: ToastEntry): void {
  if (t.timerId !== null || t.duration <= 0 || t.remaining <= 0) return;
  startTimer(t);
}

function dismiss(t: ToastEntry): void {
  if (t.dismissed) return;
  t.dismissed = true;
  if (t.timerId !== null) {
    clearTimeout(t.timerId);
    t.timerId = null;
  }
  t.el.classList.add("vk-toast-leaving");
  let removed = false;
  let fallbackId: ReturnType<typeof setTimeout> | null = null;
  const cleanup = (): void => {
    if (removed) return;
    removed = true;
    if (fallbackId !== null) clearTimeout(fallbackId);
    t.el.removeEventListener("transitionend", onEnd);
    t.el.remove();
    const idx = visible.indexOf(t);
    if (idx !== -1) visible.splice(idx, 1);
    promoteFromQueue();
  };
  const onEnd = (e: Event): void => {
    // Only fire on the toast element itself, not bubbling children.
    if (e.target !== t.el) return;
    cleanup();
  };
  t.el.addEventListener("transitionend", onEnd);
  // Fallback in case the transitionend never fires (display:none parent,
  // reduced-motion no-op transitions, etc).
  fallbackId = setTimeout(cleanup, 400);
}

function promoteFromQueue(): void {
  while (visible.length < MAX_VISIBLE) {
    const next = queue.shift();
    if (next === undefined) return;
    const dismissFn = mount(next.message, next.level, next.duration);
    next.resolve(dismissFn);
  }
}

function mount(message: string, level: ToastLevel, duration: number): () => void {
  const stack = ensureContainer();

  const el = document.createElement("div");
  el.className = `vk-toast vk-toast-${level}`;
  // tabindex so keyboard users can Tab to the toast and read/dismiss
  // it. Without this, the toast is unreachable via keyboard.
  el.setAttribute("tabindex", "0");
  // Errors are interruptive; everything else is polite. The container's
  // aria-live wins for screen reader announcement, but role=alert on
  // the individual error toast also signals importance to a11y trees
  // that inspect children.
  if (level === "error") {
    el.setAttribute("role", "alert");
  }

  // Message text — textContent (never innerHTML) since toasts must
  // not render rich content (live regions don't handle it well).
  const msgEl = document.createElement("span");
  msgEl.className = "vk-toast-msg";
  msgEl.textContent = message;
  el.appendChild(msgEl);

  let progressEl: HTMLSpanElement | null = null;
  if (duration > 0) {
    progressEl = document.createElement("span");
    progressEl.className = "vk-toast-progress";
    progressEl.setAttribute("aria-hidden", "true");
    progressEl.style.width = "100%";
    el.appendChild(progressEl);
  }

  const t: ToastEntry = {
    el,
    level,
    duration,
    remaining: duration,
    startedAt: Date.now(),
    timerId: null,
    progressEl,
    dismissed: false,
  };

  el.addEventListener("click", () => dismiss(t));
  el.addEventListener("mouseenter", () => pauseTimer(t));
  el.addEventListener("mouseleave", () => resumeTimer(t));
  el.addEventListener("focusin", () => pauseTimer(t));
  el.addEventListener("focusout", () => resumeTimer(t));

  stack.appendChild(el);
  visible.push(t);

  // Slide-in: a frame after insertion so the transition triggers.
  requestAnimationFrame(() => el.classList.add("vk-toast-shown"));

  startTimer(t);

  return () => dismiss(t);
}

function show(message: string, level: ToastLevel, duration: number): () => void {
  if (visible.length >= MAX_VISIBLE) {
    // Queue. The dismiss-fn returned from the queued path proxies to
    // the eventual mounted toast; if dismissed before mount we just
    // remove from the queue.
    let queueEntry: QueuedToast | undefined;
    let mountedDismiss: (() => void) | null = null;
    let dismissedBeforeMount = false;
    const ret = (): void => {
      if (mountedDismiss !== null) {
        mountedDismiss();
        return;
      }
      dismissedBeforeMount = true;
      if (queueEntry !== undefined) {
        const idx = queue.indexOf(queueEntry);
        if (idx !== -1) queue.splice(idx, 1);
      }
    };
    queueEntry = {
      message, level, duration,
      resolve: (fn) => {
        if (dismissedBeforeMount) {
          fn();
          return;
        }
        mountedDismiss = fn;
      },
    };
    queue.push(queueEntry);
    return ret;
  }
  return mount(message, level, duration);
}

/** Show an info-level toast. Auto-dismisses after 4s (paused on hover). */
export function info(message: string): () => void {
  return show(message, "info", DURATION_DEFAULT_MS);
}

/** Show a success-level toast. Auto-dismisses after 4s (paused on hover). */
export function success(message: string): () => void {
  return show(message, "success", DURATION_DEFAULT_MS);
}

/** Show an error-level toast. Sticky by default — users typically
 *  need time to read errors and may need to act on them. Click or
 *  press Escape to dismiss. */
export function error(message: string): () => void {
  return show(message, "error", DURATION_ERROR_MS);
}

/** Show a toast with explicit level + duration. Use durationMs=0
 *  for a sticky toast that requires manual dismissal. Pass undefined
 *  to use the level-default (4s for info/success, sticky for error). */
export function showToast(
  message: string,
  level: ToastLevel = "info",
  durationMs?: number,
): () => void {
  return show(message, level, durationMs ?? defaultDuration(level));
}

/** Test-only: clear all visible + queued toasts. */
export function _resetForTest(): void {
  for (const t of [...visible]) dismiss(t);
  queue.length = 0;
  if (containerEl !== null) {
    containerEl.remove();
    containerEl = null;
  }
}
