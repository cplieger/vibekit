// Ephemeral toast notifications — thin public API over toast-engine.ts.
// See toast-engine.ts for timer/queue state machine internals.
// ---------------------------------------------------------------------------

import {
  type ToastLevel,
  type ToastEntry,
  type ToastRetry,
  type QueuedToast,
  MAX_VISIBLE,
  defaultDuration,
  startTimer,
  pauseTimer,
  resumeTimer,
  promoteFromQueue,
  enqueueWithCap,
} from "./toast-engine.js";
import { el } from "@cplieger/reactive";

export type { ToastLevel, ToastRetry };

const DURATION_DEFAULT_MS = 4000;
const DURATION_ERROR_MS = 0; // sticky

const visible: ToastEntry[] = [];
const queue: QueuedToast[] = [];
let containerEl: HTMLDivElement | null = null;
let escHandlerInstalled = false;

function ensureContainer(): HTMLDivElement {
  if (containerEl !== null) {
    return containerEl;
  }
  const stack = el("div", {
    className: "vk-toast-stack",
    role: "status",
    "aria-live": "polite",
    "aria-atomic": "false",
  }) as HTMLDivElement;
  document.body.appendChild(stack);
  containerEl = stack;
  installEscHandler();
  return stack;
}

function installEscHandler(): void {
  if (escHandlerInstalled) {
    return;
  }
  escHandlerInstalled = true;
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Escape") {
      return;
    }
    const newest = visible[visible.length - 1];
    if (newest === undefined) {
      return;
    }
    dismiss(newest);
  });
}

function dismiss(t: ToastEntry): void {
  if (t.dismissed) {
    return;
  }
  t.dismissed = true;
  if (t.timerId !== null) {
    clearTimeout(t.timerId);
    t.timerId = null;
  }
  t.el.classList.add("vk-toast-leaving");
  let removed = false;
  let fallbackId: ReturnType<typeof setTimeout> | null = null;
  const cleanup = (): void => {
    if (removed) {
      return;
    }
    removed = true;
    if (fallbackId !== null) {
      clearTimeout(fallbackId);
    }
    t.el.removeEventListener("transitionend", onEnd);
    t.el.remove();
    const idx = visible.indexOf(t);
    if (idx !== -1) {
      visible.splice(idx, 1);
    }
    promoteFromQueue(visible, queue, mount);
  };
  const onEnd = (e: Event): void => {
    if (e.target !== t.el) {
      return;
    }
    cleanup();
  };
  t.el.addEventListener("transitionend", onEnd);
  fallbackId = setTimeout(cleanup, 400);
}

function mount(
  message: string,
  level: ToastLevel,
  duration: number,
  retry?: ToastRetry,
): () => void {
  const stack = ensureContainer();

  const node = el("div", {
    className: `vk-toast vk-toast-${level}`,
    tabindex: "0",
    "aria-label": `${level} notification: ${message}. Click to dismiss.`,
  }) as HTMLDivElement;
  if (level === "error") {
    node.setAttribute("role", "alert");
  }

  const msgEl = el("span", { className: "vk-toast-msg" }, message);
  node.appendChild(msgEl);

  let retryBtn: HTMLButtonElement | null;
  if (retry !== undefined) {
    retryBtn = el(
      "button",
      {
        type: "button",
        className: "vk-toast-retry",
        "aria-label": retry.label ?? "Retry action",
      },
      retry.label ?? "Retry",
    ) as HTMLButtonElement;
    retryBtn.addEventListener("click", (e) => {
      e.stopPropagation();
      const handler = retry.onClick;
      dismiss(t);
      try {
        const r = handler();
        if (r instanceof Promise) {
          r.catch((err: unknown) => {
            console.error("[toast] retry handler rejected", err);
          });
        }
      } catch (err) {
        console.error("[toast] retry handler threw", err);
      }
    });
    node.appendChild(retryBtn);
  }

  let progressEl: HTMLSpanElement | null = null;
  if (duration > 0) {
    progressEl = el("span", {
      className: "vk-toast-progress",
      "aria-hidden": "true",
    });
    progressEl.style.width = "100%";
    node.appendChild(progressEl);
  }

  const t: ToastEntry = {
    el: node,
    level,
    duration,
    remaining: duration,
    startedAt: Date.now(),
    timerId: null,
    progressEl,
    dismissed: false,
  };

  node.addEventListener("click", () => {
    dismiss(t);
  });
  node.addEventListener("mouseenter", () => {
    pauseTimer(t);
  });
  node.addEventListener("mouseleave", () => {
    resumeTimer(t, dismiss);
  });
  node.addEventListener("focusin", () => {
    pauseTimer(t);
  });
  node.addEventListener("focusout", () => {
    resumeTimer(t, dismiss);
  });

  stack.appendChild(node);
  visible.push(t);

  requestAnimationFrame(() => {
    node.classList.add("vk-toast-shown");
  });

  startTimer(t, dismiss);

  return () => {
    dismiss(t);
  };
}

function show(
  message: string,
  level: ToastLevel,
  duration: number,
  retry?: ToastRetry,
): () => void {
  if (visible.length >= MAX_VISIBLE) {
    // eslint-disable-next-line prefer-const
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
        if (idx !== -1) {
          queue.splice(idx, 1);
        }
      }
    };
    queueEntry = {
      message,
      level,
      duration,
      ...(retry !== undefined ? { retry } : {}),
      resolve: (fn) => {
        if (dismissedBeforeMount) {
          fn();
          return;
        }
        mountedDismiss = fn;
      },
    };
    enqueueWithCap(queue, queueEntry);
    return ret;
  }
  return mount(message, level, duration, retry);
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
 *  press Escape to dismiss. Optionally accepts a retry config; the
 *  toast renders a button that invokes onClick + dismisses. */
export function error(message: string, retry?: ToastRetry): () => void {
  return show(message, "error", DURATION_ERROR_MS, retry);
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
  for (const t of [...visible]) {
    dismiss(t);
  }
  queue.length = 0;
  if (containerEl !== null) {
    containerEl.remove();
    containerEl = null;
  }
}
