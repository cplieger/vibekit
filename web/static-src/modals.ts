// ---------------------------------------------------------------------------
// Shared modal utilities (close, overlay dismiss, confirm dialog, login)
// ---------------------------------------------------------------------------

import { $, el } from "./dom.js";
import { escText } from "./strings.js";
import { apiGet, apiPost } from "./api-client.js";
import { isSafeUrl } from "./utils-url.js";
import { registerCleanup } from "./actions/cleanup.js";
import { trapFocus } from "./focus-trap.js";
import type { WhoamiResponse } from "./wire/types.gen.js";

/** Active focus-trap release functions keyed by modal element. */
const modalTraps = new WeakMap<HTMLElement, () => void>();

export function closeModal(modal: HTMLDivElement): void {
  if (modal === $.loginModal) {
    loginPollAbort?.abort();
    loginPollAbort = null;
    loginPollUnregister?.();
    loginPollUnregister = null;
  }
  const release = modalTraps.get(modal);
  if (release) { release(); modalTraps.delete(modal); }
  modal.classList.add("hidden");
}

function setupOverlayClose(modal: HTMLDivElement): void {
  let downOnOverlay = false;
  modal.addEventListener("mousedown", (e: Event) => { downOnOverlay = e.target === modal; });
  modal.addEventListener("mouseup", (e: Event) => {
    if (downOnOverlay && e.target === modal) closeModal(modal);
    downOnOverlay = false;
  });
}

/** Auto-wire all modals: close button + overlay dismiss.
 *  Call once at startup. Finds all .modal-overlay elements and wires
 *  their close buttons (any child with [data-close] or .icon-btn in
 *  .modal-header-row) and overlay click-to-dismiss. */
export function initAllModals(): void {
  for (const overlay of document.querySelectorAll(".modal-overlay")) {
    const modal = overlay as HTMLDivElement;
    modal.setAttribute("aria-modal", "true");
    modal.setAttribute("role", "dialog");
    setupOverlayClose(modal);
    // Wire close buttons: any button inside .modal-header-row that has aria-label="Close".
    for (const btn of modal.querySelectorAll('.modal-header-row .icon-btn[aria-label="Close"]')) {
      btn.addEventListener("click", () => closeModal(modal));
    }
  }
}

// --- Rolling output: shows last 4 lines with click-to-expand ---

const EXPAND_HINT = '<svg class="output-expand-hint" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/></svg>';

/** Manages a rolling output bar that shows the last 4 lines and expands to a modal on click. */
export class RollingOutput {
  private full = "";
  private readonly bar: HTMLDivElement;
  private readonly modalId: string;

  constructor(barEl: HTMLDivElement, modalId: string) {
    this.bar = barEl;
    this.modalId = modalId;
    barEl.addEventListener("click", () => this.openModal());
  }

  clear(): void {
    this.full = "";
    this.bar.classList.add("hidden");
  }

  append(text: string): void {
    this.full += (this.full !== "" ? "\n" : "") + text;
    const lines = this.full.split("\n").filter((l) => l.trim() !== "");
    this.bar.innerHTML = escText(lines.slice(-4).join("\n")) + EXPAND_HINT;
    this.bar.classList.remove("hidden");
  }

  getText(): string { return this.full; }

  private openModal(): void {
    const modal = el<HTMLDivElement>(this.modalId);
    const body = modal.querySelector(".subagent-modal-body, pre") as HTMLPreElement;
    if (body !== null) { body.textContent = this.full; body.scrollTop = body.scrollHeight; }
    openModal(modal);
  }
}

/** Close the topmost visible modal. Returns true if a modal was closed. */
export function closeTopModal(): boolean {
  const modals = document.querySelectorAll(".modal-overlay:not(.hidden)");
  if (modals.length === 0) return false;
  const last = modals[modals.length - 1] as HTMLDivElement;
  closeModal(last);
  return true;
}

/** Open a modal with focus trap. Prefer this over raw classList manipulation. */
export function openModal(modal: HTMLDivElement): void {
  modal.classList.remove("hidden");
  const release = trapFocus(modal);
  modalTraps.set(modal, release);
}

// showConfirm removed — use confirm() from "./confirm.js" instead.
// The static #confirm-modal element in index.html has also been
// removed; confirm.ts creates its own <dialog> on demand.

/** Active login-poll abort controller; aborted when the modal is dismissed. */
let loginPollAbort: AbortController | null = null;
let loginPollUnregister: (() => void) | null = null;

export function showLoginModal(): void {
  openModal($.loginModal);
}

export function hideLoginModal(): void {
  loginPollAbort?.abort();
  loginPollAbort = null;
  loginPollUnregister?.();
  loginPollUnregister = null;
  $.loginModal.classList.add("hidden");
}

export function initLoginModal(onLoggedIn: () => void): void {
  const freeBtn = el<HTMLButtonElement>("modal-login-free");
  const ssoBtn = el<HTMLButtonElement>("modal-login-sso");
  const ssoForm = el<HTMLDivElement>("modal-sso-form");
  const ssoSubmit = el<HTMLButtonElement>("modal-sso-submit");
  const providerInput = el<HTMLInputElement>("modal-provider");
  const regionInput = el<HTMLInputElement>("modal-region");
  const status = el<HTMLDivElement>("modal-status");

  freeBtn.addEventListener("click", () => {
    ssoForm.classList.add("hidden");
    status.textContent = "Connecting...";
    doLogin({}, status, onLoggedIn);
  });
  ssoBtn.addEventListener("click", () => { ssoForm.classList.toggle("hidden"); status.textContent = ""; });

  const submit = (): void => {
    // Auto-prepend https:// so users can paste "amzn.awsapps.com/start"
    // without thinking about the scheme. validateProvider on the
    // server requires an https URL; cover that UX gap here.
    let provider = providerInput.value.trim();
    if (provider !== "" && !/^https?:\/\//i.test(provider)) {
      provider = "https://" + provider;
      providerInput.value = provider;
    }
    if (provider === "") { status.textContent = "Start URL is required"; return; }
    status.textContent = "Connecting...";
    doLogin({ provider, region: regionInput.value.trim() || undefined }, status, onLoggedIn);
  };

  ssoSubmit.addEventListener("click", submit);

  // Enter on either input submits. No form element wraps these so we
  // wire keydown manually; matches the main prompt-input UX.
  const onEnter = (e: KeyboardEvent): void => {
    if (e.key === "Enter") { e.preventDefault(); submit(); }
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
  for (const b of btns) (b as HTMLButtonElement).disabled = true;

  type LoginResp = { url?: string; code?: string; error?: string; raw?: string };
  const enable = (): void => {
    for (const b of btns) (b as HTMLButtonElement).disabled = false;
  };

  apiPost<LoginResp>("/api/login", body).then((d) => {
    if (d === null) { status.textContent = "Server error"; return; }
    if (d.error !== undefined) {
      // kiro-cli refuses a fresh login while a session exists; the
      // whoami parse bug is the usual reason we end up here. Reload
      // so checkAuthAndStart runs again with the tolerant parser.
      if (d.error === "already_logged_in") {
        status.innerHTML = "You're already signed in. "
          + '<button type="button" id="login-reload" class="btn-small" '
          + 'style="margin-inline-start:var(--sp-2)">Reload</button>';
        const reloadBtn = document.getElementById("login-reload");
        reloadBtn?.addEventListener("click", () => location.reload());
        return;
      }
      const detail = d.raw !== undefined ? `\n\nCLI output:\n${d.raw}` : "";
      status.textContent = d.error + detail;
      return;
    }
    if (d.url !== undefined) {
      const codeText = d.code !== undefined ? `Code: ${d.code}` : "";
      const urlHtml = isSafeUrl(d.url)
        ? `<a href="${escText(d.url)}" target="_blank" rel="noopener" style="color:var(--c-accent)">Open login page</a>`
        : `<span style="color:var(--c-text-tertiary)">${escText(d.url)}</span>`;
      status.innerHTML = `${escText(codeText)}<br>${urlHtml}<br>`
        + `<span style="color:var(--c-text-tertiary)">Complete login in the browser, then come back.</span>`;
      const MAX_POLL_ATTEMPTS = 200; // ~10 minutes at 3s intervals
      const ctrl = new AbortController();
      loginPollAbort = ctrl;
      loginPollUnregister?.();
      loginPollUnregister = registerCleanup(() => loginPollAbort?.abort());
      const signal = AbortSignal.any([ctrl.signal, AbortSignal.timeout(MAX_POLL_ATTEMPTS * 3000)]);
      void (async () => {
        while (!signal.aborted) {
          await new Promise<void>((r) => setTimeout(r, 3000));
          if (signal.aborted) break;
          const wd = await apiGet<WhoamiResponse>("/api/whoami", signal);
          if (signal.aborted) break;
          if (wd?.email !== undefined && wd.email !== "") {
            loginPollAbort = null;
            loginPollUnregister?.();
            loginPollUnregister = null;
            onLoggedIn();
            return;
          }
        }
        if (ctrl.signal.aborted) return; // user dismissed
        loginPollUnregister?.();
        loginPollUnregister = null;
        status.textContent = "Login timed out. Please reload and try again.";
      })();
    }
  }).finally(enable);
}
