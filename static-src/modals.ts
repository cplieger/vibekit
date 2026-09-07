// ---------------------------------------------------------------------------
// Shared modal system — built on @cplieger/ui-primitives' createModal
// (native <dialog>).
//
// Each modal's content element is pre-authored in index.html (id="X-modal",
// data-modal); createModal wraps it in a <dialog class="uip-modal"> appended to
// <body>. The platform then owns focus containment, the top layer, background
// inerting, Escape, nested stacking, and focus-return-to-opener; createModal
// adds ARIA wiring (auto aria-labelledby from a `-title` descendant), drag-safe
// backdrop dismiss, the shared `is-leaving` fade-out, and an iOS-safe
// ref-counted background scroll-lock.
//
// The old overlay-<div> system (initAllModals querying `.modal-overlay`, a
// hand-rolled focus trap via a `modalTraps` WeakMap, `setupOverlayClose`
// mousedown/mouseup dismissal, and openModal/closeModal toggling a `.hidden`
// class) was removed in this rewrite — every one of those is now native or
// provided by createModal.
// ---------------------------------------------------------------------------

import { createModal, type ModalController } from "@cplieger/ui-primitives/modal";
import { el } from "@cplieger/reactive";
import { pollUntil, registerCleanup } from "./actions/index.js";
import { $, byId } from "./dom.js";
import { apiGetTyped, apiPost } from "./api-client.js";
import { decodeWhoamiResponse } from "./wire/decoders.gen.js";
import { isSafeUrl } from "./utils-url.js";

/** Pre-authored modal content element -> its createModal controller. */
const controllers = new Map<HTMLElement, ModalController>();
/** Open-order stack of content elements; closeTopModal closes the topmost. */
const openStack: HTMLElement[] = [];
/** Per-modal callbacks run after the modal finishes closing (via any path). */
const closeCallbacks = new Map<HTMLElement, Set<() => void>>();

/** Register a callback fired after `modal` finishes closing via ANY path
 *  (Close button, backdrop, Escape, or a programmatic close). Used for teardown
 *  that must run regardless of how the modal was dismissed — the login
 *  poll-abort and the MCP add/edit-form cleanup. Safe to call before the modal
 *  is initialised; the callback set is consulted at close time. */
export function onModalClose(modal: HTMLDivElement, fn: () => void): void {
  let set = closeCallbacks.get(modal);
  if (set === undefined) {
    set = new Set();
    closeCallbacks.set(modal, set);
  }
  set.add(fn);
}

/** Runs once a modal has finished its fade-out: drop it from the open stack and
 *  fire any registered close callbacks. Wired as each controller's onClose. */
function handleModalClosed(content: HTMLElement): void {
  const i = openStack.lastIndexOf(content);
  if (i !== -1) {
    openStack.splice(i, 1);
  }
  const cbs = closeCallbacks.get(content);
  if (cbs !== undefined) {
    for (const fn of cbs) {
      fn();
    }
  }
}

/** Lazily create (or fetch) the controller for a pre-authored modal content
 *  element. Idempotent: the controller is created once and reused, so an
 *  openModal call that races ahead of initAllModals still works. */
function ensureModal(content: HTMLElement): ModalController {
  const existing = controllers.get(content);
  if (existing !== undefined) {
    return existing;
  }
  const ctrl = createModal(content, {
    onClose: () => {
      handleModalClosed(content);
    },
  });
  // The content is authored with `.hidden` (display:none) so it can't flash in
  // <body> before createModal wraps it in a closed <dialog>. The <dialog> now
  // owns visibility, so drop the class.
  content.classList.remove("hidden");
  controllers.set(content, ctrl);
  wireCloseButtons(content, ctrl);
  return ctrl;
}

/** Wire any plain Close buttons (`.modal-header-row .icon-btn[aria-label=Close]`)
 *  to the controller's close(). The login modal has none; the platform +
 *  backdrop + Escape still dismiss it. */
function wireCloseButtons(content: HTMLElement, ctrl: ModalController): void {
  for (const btn of content.querySelectorAll('.modal-header-row .icon-btn[aria-label="Close"]')) {
    btn.addEventListener("click", () => {
      ctrl.close();
    });
  }
}

/** Auto-wire all pre-authored modals. Call once at startup. Each `[data-modal]`
 *  element becomes a createModal-managed <dialog>. openModal also lazily
 *  initialises its target, so this is a proactive convenience rather than a
 *  hard prerequisite. */
export function initAllModals(): void {
  for (const content of document.querySelectorAll<HTMLElement>("[data-modal]")) {
    ensureModal(content);
  }
}

// --- Rolling output: shows last 4 lines with click-to-expand ---

const EXPAND_HINT =
  '<svg class="output-expand-hint" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg>';

// Parse the constant expand-hint SVG ONCE at module load; each append imports a
// fresh copy of the cached node rather than re-running DOMParser on every
// output update / modal open.
// eslint-disable-next-line @typescript-eslint/no-non-null-assertion
const EXPAND_HINT_NODE = new DOMParser().parseFromString(EXPAND_HINT, "text/html").body.firstChild!;

/** Manages a rolling output bar that shows the last 4 lines and expands to a modal on click. */
export class RollingOutput {
  private full = "";
  private readonly bar: HTMLDivElement;
  private readonly modalId: string;

  constructor(barEl: HTMLDivElement, modalId: string) {
    this.bar = barEl;
    this.modalId = modalId;
    barEl.addEventListener("click", () => {
      this.openModal();
    });
  }

  clear(): void {
    this.full = "";
    this.bar.classList.add("hidden");
  }

  append(text: string): void {
    this.full += (this.full !== "" ? "\n" : "") + text;
    const lines = this.full.split("\n").filter((l) => l.trim() !== "");
    const textNode = document.createTextNode(lines.slice(-4).join("\n"));
    this.bar.replaceChildren(textNode, document.importNode(EXPAND_HINT_NODE, true));
    this.bar.classList.remove("hidden");
  }

  getText(): string {
    return this.full;
  }

  private openModal(): void {
    const modal = byId<HTMLDivElement>(this.modalId);
    const body = modal.querySelector(".subagent-modal-body, pre")!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition -- defensive check
    if (body !== null) {
      body.textContent = this.full;
      body.scrollTop = body.scrollHeight;
    }
    openModal(modal);
  }
}

/** Open a modal by its content element. Preserves the historical signature so
 *  callers passing `$.mcpModal` / `byId("filepicker-modal")` are unchanged. */
export function openModal(modal: HTMLDivElement): void {
  const ctrl = ensureModal(modal);
  if (!openStack.includes(modal)) {
    openStack.push(modal);
  }
  ctrl.open();
}

/** Close a modal by its content element. No-op if it was never opened. */
export function closeModal(modal: HTMLDivElement): void {
  controllers.get(modal)?.close();
}

/** Close the topmost OPEN modal. Returns true if one was closed. Kept working
 *  so keys.ts's Escape handler is unchanged. The controller's doClose is
 *  idempotent, so this coexists safely with the platform's own Escape handling
 *  — if both fire, the second close is a no-op (no double fade-out, one onClose). */
export function closeTopModal(): boolean {
  for (let i = openStack.length - 1; i >= 0; i--) {
    const content = openStack[i]!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    const ctrl = controllers.get(content);
    if (ctrl?.isOpen === true) {
      ctrl.close();
      return true;
    }
  }
  return false;
}

// showConfirm removed — use confirm() from "./confirm.js" instead.
// The static #confirm-modal element in index.html has also been
// removed; confirm.ts creates its own <dialog> on demand.

// --- Login modal ------------------------------------------------------------

/** Active login-poll abort controller; aborted when the modal is dismissed. */
let loginPollAbort: AbortController | null = null;
let loginPollUnregister: (() => void) | null = null;

/** Abort any in-flight login whoami poll. Wired as the login controller's
 *  onClose (via onModalClose) so ANY dismissal path — Close-less backdrop
 *  click, Escape, or the programmatic close on success — stops the poll. */
function abortLoginPoll(): void {
  loginPollAbort?.abort();
  loginPollAbort = null;
  loginPollUnregister?.();
  loginPollUnregister = null;
}

export function showLoginModal(): void {
  openModal($.loginModal);
}

export function hideLoginModal(): void {
  closeModal($.loginModal);
}

export function initLoginModal(onLoggedIn: () => void): void {
  // Abort the whoami poll on every login-modal close path. The login modal has
  // no Close button, so dismissal is backdrop / Escape / the programmatic close
  // on success — all funnel through the controller's onClose. This must not
  // regress: a dismissed login must never leave a detached poll running.
  onModalClose($.loginModal, abortLoginPoll);

  const freeBtn = byId<HTMLButtonElement>("modal-login-free");
  const ssoBtn = byId<HTMLButtonElement>("modal-login-sso");
  const ssoForm = byId<HTMLDivElement>("modal-sso-form");
  const ssoSubmit = byId<HTMLButtonElement>("modal-sso-submit");
  const providerInput = byId<HTMLInputElement>("modal-provider");
  const regionInput = byId<HTMLInputElement>("modal-region");
  const status = byId<HTMLDivElement>("modal-status");

  freeBtn.addEventListener("click", () => {
    ssoForm.classList.add("hidden");
    status.textContent = "Connecting...";
    doLogin({}, status, onLoggedIn);
  });
  ssoBtn.addEventListener("click", () => {
    ssoForm.classList.toggle("hidden");
    status.textContent = "";
  });

  const submit = (): void => {
    // Auto-prepend https:// so users can paste "amzn.awsapps.com/start"
    // without thinking about the scheme. validateProvider on the
    // server requires an https URL; cover that UX gap here.
    let provider = providerInput.value.trim();
    if (provider !== "" && !/^https?:\/\//i.test(provider)) {
      provider = "https://" + provider;
      providerInput.value = provider;
    }
    if (provider === "") {
      status.textContent = "Start URL is required";
      return;
    }
    status.textContent = "Connecting...";
    doLogin({ provider, region: regionInput.value.trim() || undefined }, status, onLoggedIn);
  };

  ssoSubmit.addEventListener("click", submit);

  // Enter on either input submits. No form element wraps these so we
  // wire keydown manually; matches the main prompt-input UX.
  const onEnter = (e: KeyboardEvent): void => {
    if (e.key === "Enter") {
      e.preventDefault();
      submit();
    }
  };
  providerInput.addEventListener("keydown", onEnter);
  regionInput.addEventListener("keydown", onEnter);
}

function doLogin(
  body: Record<string, string | undefined>,
  status: HTMLDivElement,
  onLoggedIn: () => void,
): void {
  loginPollAbort?.abort();
  loginPollAbort = null;

  const btns = document.querySelectorAll("#login-modal .modal-btn");
  for (const b of btns) {
    (b as HTMLButtonElement).disabled = true;
  }

  interface LoginResp {
    url?: string;
    code?: string;
    error?: string;
    raw?: string;
  }
  const enable = (): void => {
    for (const b of btns) {
      (b as HTMLButtonElement).disabled = false;
    }
  };

  void apiPost<LoginResp>("/api/login", body)
    .then((d) => {
      if (d === null) {
        status.textContent = "Server error";
        return;
      }
      if (d.error !== undefined) {
        // kiro-cli refuses a fresh login while a session exists; the
        // whoami parse bug is the usual reason we end up here. Reload
        // so checkAuthAndStart runs again with the tolerant parser.
        if (d.error === "already_logged_in") {
          status.textContent = "";
          status.append("You\u2019re already signed in. ");
          const reloadBtn = el("button", { type: "button", className: "btn-small" }, "Reload");
          reloadBtn.style.marginInlineStart = "var(--sp-2)";
          reloadBtn.addEventListener("click", () => {
            location.reload();
          });
          status.append(reloadBtn);
          return;
        }
        const detail = d.raw !== undefined ? `\n\nCLI output:\n${d.raw}` : "";
        status.textContent = d.error + detail;
        return;
      }
      if (d.url !== undefined) {
        const codeText = d.code !== undefined ? `Code: ${d.code}` : "";
        status.textContent = "";
        if (codeText) {
          status.append(codeText);
          status.append(el("br"));
        }
        if (isSafeUrl(d.url)) {
          const link = el(
            "a",
            { href: d.url, target: "_blank", rel: "noopener" },
            "Open login page",
          );
          link.style.color = "var(--c-accent)";
          status.append(link);
        } else {
          const span = el("span", null, d.url);
          span.style.color = "var(--c-text-tertiary)";
          status.append(span);
        }
        status.append(el("br"));
        const hint = el("span", null, "Complete login in the browser, then come back.");
        hint.style.color = "var(--c-text-tertiary)";
        status.append(hint);
        const MAX_POLL_ATTEMPTS = 200; // ~10 minutes at 3s intervals
        const ctrl = new AbortController();
        loginPollAbort = ctrl;
        loginPollUnregister?.();
        loginPollUnregister = registerCleanup(() => loginPollAbort?.abort());
        const signal = AbortSignal.any([
          ctrl.signal,
          AbortSignal.timeout(MAX_POLL_ATTEMPTS * 3000),
        ]);
        void (async () => {
          // Wait-then-poll /api/whoami every 3s until it reports signed_in
          // (terminal), the user dismisses (ctrl), or the 10-minute deadline
          // fires — both rolled into `signal`. `signed_out` is the expected
          // answer for most of the window and keeps polling; so does
          // `unavailable`, which is the state that used to be indistinguishable
          // from a sign-out and would have ended the poll at the first hiccup.
          const outcome = await pollUntil(
            (s) => apiGetTyped("/api/whoami", decodeWhoamiResponse, s),
            {
              intervalMs: 3000,
              until: (wd) => wd.state === "signed_in",
              signal,
            },
          );
          if (outcome.status === "done") {
            loginPollAbort = null;
            loginPollUnregister?.(); // eslint-disable-line @typescript-eslint/no-unnecessary-condition
            loginPollUnregister = null;
            onLoggedIn();
            return;
          }
          // Otherwise aborted: distinguish a user dismiss from the deadline.
          if (ctrl.signal.aborted) {
            return;
          } // user dismissed
          loginPollUnregister?.(); // eslint-disable-line @typescript-eslint/no-unnecessary-condition
          loginPollUnregister = null;
          status.textContent = "Login timed out. Please reload and try again.";
        })();
      }
    })
    .finally(enable);
}
