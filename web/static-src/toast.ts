// Ephemeral toast notifications: bottom-right stack of short-lived
// status messages for transient feedback (action failures, copy
// confirmations, etc).
//
// Usage:
//   showToast("Saved", "success");
//   showToast("Couldn't delete: refusing top-level directory", "error");
//   showToast("Reloading...", "info", 2000);
//
// Toasts auto-dismiss after `durationMs` (default 4s) and click to
// dismiss. The container is created lazily on first use and lives at
// the document body root so toasts overlay every view.
//
// Not persisted, not chat-scoped — for state that the user must keep
// seeing, use banner-stack.ts (per-chat persistent banners).
// ---------------------------------------------------------------------------

export type ToastLevel = "info" | "success" | "error";

let containerEl: HTMLDivElement | null = null;
const TOAST_DEFAULT_MS = 4000;
const TOAST_LEAVE_MS = 280;

function ensureContainer(): HTMLDivElement {
  if (containerEl !== null) return containerEl;
  const stack = document.createElement("div");
  stack.className = "vk-toast-stack";
  // aria-live: polite so screen readers announce new toasts without
  // interrupting the current focus.
  stack.setAttribute("role", "status");
  stack.setAttribute("aria-live", "polite");
  stack.setAttribute("aria-atomic", "false");
  document.body.appendChild(stack);
  containerEl = stack;
  return stack;
}

/** Show an ephemeral toast. Returns a dismiss function the caller can
 *  invoke to hide the toast early (rare; useful for "loading..." that
 *  should be replaced with a result toast). */
export function showToast(
  message: string,
  level: ToastLevel = "info",
  durationMs: number = TOAST_DEFAULT_MS,
): () => void {
  const stack = ensureContainer();
  const t = document.createElement("div");
  t.className = `vk-toast vk-toast-${level}`;
  t.textContent = message;
  stack.appendChild(t);

  // Slide-in: a frame after insertion so the transition triggers.
  requestAnimationFrame(() => t.classList.add("vk-toast-shown"));

  let dismissed = false;
  const dismiss = (): void => {
    if (dismissed) return;
    dismissed = true;
    t.classList.add("vk-toast-leaving");
    setTimeout(() => t.remove(), TOAST_LEAVE_MS);
  };

  t.addEventListener("click", dismiss);
  setTimeout(dismiss, durationMs);
  return dismiss;
}
