// ---------------------------------------------------------------------------
// Banner stack: per-chat persistent banners above the transcript.
//
// Init errors (agent_not_found, agent_config_error), rate limits (including
// the v3 system/notify "model under load" notice), and MCP failures surface
// here. Each banner is keyed on (chat_id, code) so the same error replaces
// rather than duplicates.
//
// Banners are per-device (dismissal state in localStorage) and auto-clear
// when the underlying condition resolves.
//
// State is a createCollection<BannerEntry>; the stack is rendered by a single
// bindList over a computed active-chat view, so add / remove / chat-switch all
// flow through ONE reactive render source (no direct DOM mutation that could
// desync the reconcile view).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { activeSession } from "./store.js";
import { load, save } from "./ui-state.js";
import { isSafeURL } from "./url-safety.js";
import { el, createCollection, bindList, computed } from "@cplieger/reactive";
import { join } from "@cplieger/keyenc";
import type { BannerLevel } from "./types.js";

/** Optional clickable affordance rendered inside a banner. Two shapes, and the
 *  distinction is not cosmetic:
 *
 *   - `href` is an EXTERNAL navigation (the open_external_url "Open sign-in
 *     page" affordance). The URL is server-supplied and untrusted, so it goes
 *     through isSafeURL and only http/https renders; it opens in a new tab.
 *   - `onClick` is an IN-APP jump (a deep link to a Settings control). It
 *     renders a button and carries no URL at all, which is the point: a relative
 *     path like `/settings/permissions?highlight=x` throws inside isSafeURL's
 *     `new URL()` and would be silently dropped, and laundering an internal
 *     navigation through the guard that exists for untrusted URLs — then opening
 *     it in a new tab — is the wrong shape for jumping across your own app.
 *
 *  Exactly one of the two is used; `onClick` wins if both are set. */
export interface BannerLink {
  readonly label: string;
  readonly href?: string;
  readonly onClick?: () => void;
}

interface BannerEntry {
  readonly code: string;
  readonly chatID: string;
  message: string;
  readonly level: BannerLevel;
  readonly dismissible: boolean;
  el: HTMLDivElement;
}

const banners = createCollection<BannerEntry>((e) => bannerKey(e.chatID, e.code));

/** Sentinel chatID for app-global banners: visible on EVERY chat (and
 *  on the empty no-chat state), used for conditions that aren't scoped
 *  to one conversation — e.g. the degraded-runtime banner when
 *  kiro-cli is unavailable. Global banners clear via
 *  `clearBannerCodes(GLOBAL_BANNER, [...])`, never by chat switches. */
export const GLOBAL_BANNER = "*";

// Visible banners = those for the active chat, plus app-global ones.
// Tracks the collection structure + activeSession (so a chat switch
// re-renders) and stays shallow-equal so a no-op recompute doesn't
// reconcile.
const visibleIds = computed<readonly string[]>(
  () => {
    const activeID = activeSession.value?.id ?? "";
    return banners
      .items()
      .filter((e) => e.chatID === activeID || e.chatID === GLOBAL_BANNER)
      .map((e) => bannerKey(e.chatID, e.code));
  },
  { equals: (a, b) => a.length === b.length && a.every((x, i) => x === b[i]) },
);

let bound = false;

/** Mount + bind the banner stack. The bindList renders reactively from the
 *  collection + activeSession, so add / remove / chat-switch all re-render
 *  automatically; this is idempotent (the `bound` flag guards against
 *  double-binding) and is called both internally by `showBanner` and from the
 *  chat-switch call site in chat.ts. */
export function ensureBound(): void {
  if (bound) {
    return;
  }
  bound = true;
  const container = $.bannerStack;
  container.setAttribute("aria-label", "Notifications");
  // The stack container is the SINGLE live region for banners; individual
  // banner nodes carry no role/aria-live (see showBanner) so an added banner
  // announces exactly once.
  container.setAttribute("aria-live", "polite");
  // Reuse the entry-owned element so banner identity (and any ongoing
  // transitions / focus) persists across re-renders.
  bindList(
    container,
    { ids: visibleIds, signalFor: (id) => banners.signalFor(id) },
    { mount: (e) => e.el },
  );
}

/** Collection + localStorage key for one banner.
 *
 *  Built with keyenc `join` so neither field can forge a boundary. This is
 *  byte-identical to the old `${chatID}:${code}` template for every key this
 *  app produces — a chat id is `[A-Za-z0-9_-]` (api.ValidChatID, so no ":" and
 *  no "\\") and a code is a call-site literal from the same class — so NO
 *  persisted dismissal under `dismissed_banners` is invalidated by the
 *  adoption. The join is what keeps that true if either field ever loosens. */
function bannerKey(chatID: string, code: string): string {
  return join(chatID, code);
}

function isDismissed(chatID: string, code: string): boolean {
  return load().dismissed_banners.includes(bannerKey(chatID, code));
}

function persistDismiss(chatID: string, code: string): void {
  const state = load();
  const key = bannerKey(chatID, code);
  if (!state.dismissed_banners.includes(key)) {
    save({ dismissed_banners: [...state.dismissed_banners, key] });
  }
}

function clearDismiss(chatID: string, code: string): void {
  const state = load();
  const key = bannerKey(chatID, code);
  const filtered = state.dismissed_banners.filter((k) => k !== key);
  if (filtered.length !== state.dismissed_banners.length) {
    save({ dismissed_banners: filtered });
  }
}

/** Build the banner's affordance: a button for an in-app jump, an anchor for a
 *  safe external URL, or null when neither is available (an unsafe href is
 *  dropped rather than rendered inert). */
function buildBannerLink(link: BannerLink): HTMLElement | null {
  if (link.onClick !== undefined) {
    const btn = el("button", { type: "button", className: "banner-link" }, link.label);
    btn.addEventListener("click", link.onClick);
    return btn;
  }
  if (link.href === undefined || !isSafeURL(link.href)) {
    return null;
  }
  return el(
    "a",
    {
      className: "banner-link",
      href: link.href,
      target: "_blank",
      rel: "noopener noreferrer",
    },
    link.label,
  );
}

/** Insert/replace the banner link on an existing banner node, before the
 *  dismiss button when present. */
function updateBannerLink(node: HTMLDivElement, link: BannerLink): void {
  node.querySelector(".banner-link")?.remove();
  const a = buildBannerLink(link);
  if (a === null) {
    return;
  }
  const dismissBtn = node.querySelector(".banner-dismiss");
  if (dismissBtn !== null) {
    node.insertBefore(a, dismissBtn);
  } else {
    node.appendChild(a);
  }
}

export function showBanner(
  chatID: string,
  code: string,
  message: string,
  level: BannerLevel,
  dismissible: boolean,
  link?: BannerLink,
): void {
  if (isDismissed(chatID, code)) {
    return;
  }
  const key = bannerKey(chatID, code);
  const existing = banners.get(key);
  if (existing !== undefined) {
    // Replace message in place on the entry-owned element (a single text node;
    // the row's identity is unchanged, so no structural reconcile is needed).
    const msg = existing.el.querySelector(".banner-msg");
    if (msg !== null) {
      msg.textContent = message;
    }
    existing.message = message;
    if (link !== undefined) {
      updateBannerLink(existing.el, link);
    }
    return;
  }
  const msg = el("span", { className: "banner-msg" }, message);
  // No per-banner role/aria-live: the stack container (ensureBound) is the single
  // aria-live="polite" region, so individual banners are NOT separately live —
  // nesting a role="alert"/"status" child inside a live region double-announces
  // (or announces at a conflicting politeness). Same decoupling toast.ts uses.
  const node = el(
    "div",
    {
      className: `banner banner-${level}`,
    },
    msg,
  ) as HTMLDivElement;
  if (link !== undefined) {
    const a = buildBannerLink(link);
    if (a !== null) {
      node.appendChild(a);
    }
  }
  if (dismissible) {
    const btn = el(
      "button",
      { type: "button", className: "banner-dismiss", "aria-label": "Dismiss" },
      "\u00d7",
    );
    btn.addEventListener("click", () => {
      removeBanner(chatID, code);
      persistDismiss(chatID, code);
    });
    node.appendChild(btn);
  }
  const entry: BannerEntry = { code, chatID, message, level, dismissible, el: node };
  ensureBound();
  banners.upsert(entry);
  pruneStaleDismissals();
}

function removeBanner(chatID: string, code: string): void {
  if (!banners.has(bannerKey(chatID, code))) {
    return;
  }
  banners.remove(bannerKey(chatID, code));
  clearDismiss(chatID, code);
}

/** Remove all banners matching any of the given codes for a chat. */
export function clearBannerCodes(chatID: string, codes: string[]): void {
  for (const code of codes) {
    removeBanner(chatID, code);
  }
}

/** Drop every banner (and its dismissed-state persistence) for a chat that no
 *  longer exists. Called from the chat_deleted bus handler so orphan entries +
 *  localStorage entries don't accumulate over a long session. */
export function clearBannersForChat(chatID: string): void {
  // Prefix scan rather than a keyenc call: the library has no "prefix of a
  // key" primitive (it does not export its escaper), so the separator is
  // written out here. It stays correct because a chat id contains neither
  // reserved character (api.ValidChatID: [A-Za-z0-9_-]), so `join` emits it
  // verbatim and `${chatID}:` is exactly the first component of every key for
  // this chat. The trailing ":" is what keeps the scan from over-matching —
  // chat "abc" must not clear chat "abcd" (pinned by a test in
  // banner-stack.test.ts). If chat ids ever admit ":" or "\\", this scan is
  // the site that breaks and must switch to splitting each key instead.
  const prefix = `${chatID}:`;
  // 1. Drop in-memory entries (bindList detaches their elements reactively).
  for (const key of banners.ids.peek()) {
    if (key.startsWith(prefix)) {
      banners.remove(key);
    }
  }
  // 2. Prune persisted dismissals so localStorage doesn't grow forever.
  const state = load();
  const filtered = state.dismissed_banners.filter((k) => !k.startsWith(prefix));
  if (filtered.length !== state.dismissed_banners.length) {
    save({ dismissed_banners: filtered });
  }
}

const PRUNE_THRESHOLD = 200;

/** Prune dismissed_banners entries that exceed the threshold. Keeps only the
 *  most recent entries (tail of the array). */
function pruneStaleDismissals(): void {
  const state = load();
  if (state.dismissed_banners.length <= PRUNE_THRESHOLD) {
    return;
  }
  save({ dismissed_banners: state.dismissed_banners.slice(-PRUNE_THRESHOLD) });
}

/** Auto-clear transient banners (rate_limit) on successful turn end. */
export function onTurnEnded(chatID: string): void {
  removeBanner(chatID, "rate_limit");
}
