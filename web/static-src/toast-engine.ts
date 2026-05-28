// toast-engine.ts — Timer/queue state machine for toast notifications.
// Pure state logic, no DOM creation. Testable without a browser environment.

export type ToastLevel = "info" | "success" | "error";

export const MAX_VISIBLE = 3;
export const MAX_QUEUE = 20;
const DURATION_DEFAULT_MS = 4000;
const DURATION_ERROR_MS = 0; // sticky

export interface ToastEntry {
  el: HTMLDivElement;
  level: ToastLevel;
  duration: number;
  remaining: number;
  startedAt: number;
  timerId: ReturnType<typeof setTimeout> | null;
  progressEl: HTMLSpanElement | null;
  dismissed: boolean;
}

export interface ToastRetry {
  readonly label?: string;
  readonly onClick: () => void | Promise<void>;
}

export interface QueuedToast {
  message: string;
  level: ToastLevel;
  duration: number;
  retry?: ToastRetry;
  resolve: (dismissFn: () => void) => void;
}

export function defaultDuration(level: ToastLevel): number {
  return level === "error" ? DURATION_ERROR_MS : DURATION_DEFAULT_MS;
}

export function startTimer(t: ToastEntry, onDismiss: (t: ToastEntry) => void): void {
  if (t.duration <= 0 || t.remaining <= 0) {
    return;
  }
  t.startedAt = Date.now();
  t.timerId = setTimeout(() => {
    onDismiss(t);
  }, t.remaining);
  if (t.progressEl !== null) {
    t.progressEl.style.transitionDuration = "0ms";
    void t.progressEl.offsetWidth;
    t.progressEl.style.transitionDuration = `${String(t.remaining)}ms`;
    requestAnimationFrame(() => {
      if (t.progressEl !== null) {
        t.progressEl.style.width = "0%";
      }
    });
  }
}

export function pauseTimer(t: ToastEntry): void {
  if (t.timerId === null || t.duration <= 0) {
    return;
  }
  clearTimeout(t.timerId);
  t.timerId = null;
  const elapsed = Date.now() - t.startedAt;
  t.remaining = Math.max(0, t.remaining - elapsed);
  if (t.progressEl !== null) {
    const cs = window.getComputedStyle(t.progressEl);
    t.progressEl.style.transitionDuration = "0ms";
    t.progressEl.style.width = cs.width;
  }
}

export function resumeTimer(t: ToastEntry, onDismiss: (t: ToastEntry) => void): void {
  if (t.timerId !== null || t.duration <= 0 || t.remaining <= 0) {
    return;
  }
  startTimer(t, onDismiss);
}

export function promoteFromQueue(
  visible: ToastEntry[],
  queue: QueuedToast[],
  mountFn: (message: string, level: ToastLevel, duration: number, retry?: ToastRetry) => () => void,
): void {
  while (visible.length < MAX_VISIBLE) {
    const next = queue.shift();
    if (next === undefined) {
      return;
    }
    const dismissFn = mountFn(next.message, next.level, next.duration, next.retry);
    next.resolve(dismissFn);
  }
}

/** Enforce queue cap. When the queue is at MAX_QUEUE, drop the oldest
 *  entry (resolve its dismiss function as a no-op) before pushing. */
export function enqueueWithCap(queue: QueuedToast[], entry: QueuedToast): void {
  if (queue.length >= MAX_QUEUE) {
    const dropped = queue.shift();
    if (dropped !== undefined) {
      // Resolve with a no-op dismiss so the caller's Promise settles.
      dropped.resolve(() => {});
    }
  }
  queue.push(entry);
}
