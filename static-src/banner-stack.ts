// ---------------------------------------------------------------------------
// Banner stack: per-chat persistent banners above the transcript.
//
// Init errors (agent_not_found, agent_config_error), rate limits (including
// the v3 system/notify "model under load" notice), and MCP failures surface
// here. Each banner is keyed on (chat_id, code) so the same error replaces
// rather than duplicates.
//
// Banners are per-device and auto-clear when the underlying condition resolves.
// The DISMISSALS are per-device too, keyed per chat in localStorage — a phone
// dismissing a banner must not silence the desktop, which is web-terminal's rule
// verbatim: an acknowledgement is the viewer's. They briefly lived in the
// server-owned arrangement as a flat `dismissed_banners` list, and that shared
// them.
//
// State is a createCollection<BannerEntry>; the stack is rendered by a single
// bindList over a computed active-chat view, so add / remove / chat-switch all
// flow through ONE reactive render source (no direct DOM mutation that could
// desync the reconcile view).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { activeSession } from "./store.js";
import { LS_DISMISSED_BANNERS_KEY } from "./ls-keys.js";
import { readPerChat, writePerChat } from "./per-chat-store.js";
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

/** The COLLECTION key for one banner. No longer a localStorage key: the
 *  dismissals moved to a per-chat map, so the only composite left is the in-memory
 *  one the reactive collection is keyed by.
 *
 *  Still built with keyenc `join` rather than a template literal, and that is not
 *  vestigial: `clearBannersForChat` scans these keys by chat prefix, so a code
 *  containing the separator could otherwise let one chat's entry read as another's.
 *  It is byte-identical to `${chatID}:${code}` for every key this app produces — a
 *  chat id is `[A-Za-z0-9_-]` (ids.ValidChatID, so no ":" and no "\\") and a code
 *  is a call-site literal from the same class — which is what makes that scan
 *  correct today; the join is what keeps it correct if either field ever loosens. */
function bannerKey(chatID: string, code: string): string {
  return join(chatID, code);
}

/** Read the dismissals, per chat. Codes only: the CHAT is the map key now, so a
 *  composite key has nothing to compose and no boundary anything could forge. */
function dismissals(): Record<string, string[]> {
  return readPerChat(LS_DISMISSED_BANNERS_KEY, validCodes);
}

/** Validate one chat's dismissed codes, dropping anything that is not a
 *  non-empty string. */
function validCodes(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) {
    return undefined;
  }
  const out = v.filter((c): c is string => typeof c === "string" && c !== "");
  return out.length > 0 ? out : undefined;
}

/** Write ONE chat's codes, which is what lets the store evict by chat. An empty
 *  list is a delete, so a chat with nothing dismissed keeps no slot. */
function writeCodes(map: Record<string, string[]>, chatID: string, codes: string[]): void {
  writePerChat(LS_DISMISSED_BANNERS_KEY, map, chatID, codes.length > 0 ? codes : undefined);
}

function isDismissed(chatID: string, code: string): boolean {
  return (dismissals()[chatID] ?? []).includes(code);
}

function persistDismiss(chatID: string, code: string): void {
  const map = dismissals();
  const codes = map[chatID] ?? [];
  if (codes.includes(code)) {
    return;
  }
  writeCodes(map, chatID, [...codes, code]);
}

function clearDismiss(chatID: string, code: string): void {
  const map = dismissals();
  const codes = map[chatID] ?? [];
  const filtered = codes.filter((c) => c !== code);
  if (filtered.length !== codes.length) {
    writeCodes(map, chatID, filtered);
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

/** Drop every in-memory banner for a chat that no longer exists. Called from the
 *  chat_deleted bus handler so orphan BannerEntry objects (and the DOM nodes they
 *  own) don't accumulate over a long session.
 *
 *  It no longer prunes the persisted DISMISSALS, and that half is gone rather than
 *  relocated: it existed only because the state was one global list where an entry
 *  per deleted chat accumulated forever. The dismissals are keyed per chat now,
 *  bounded by chat count with oldest-first eviction (per-chat-store.ts), so nothing
 *  has to be told a chat is gone — and a chat CAN go without this being called at
 *  all, since retention purges one with no client involved. */
export function clearBannersForChat(chatID: string): void {
  // Prefix scan rather than a keyenc call: the library has no "prefix of a
  // key" primitive (it does not export its escaper), so the separator is
  // written out here. It stays correct because a chat id contains neither
  // reserved character (ids.ValidChatID: [A-Za-z0-9_-]), so `join` emits it
  // verbatim and `${chatID}:` is exactly the first component of every key for
  // this chat. The trailing ":" is what keeps the scan from over-matching —
  // chat "abc" must not clear chat "abcd" (pinned by a test in
  // banner-stack.test.ts). If chat ids ever admit ":" or "\\", this scan is
  // the site that breaks and must switch to splitting each key instead.
  const prefix = `${chatID}:`;
  // bindList detaches the removed entries' elements reactively.
  for (const key of banners.ids.peek()) {
    if (key.startsWith(prefix)) {
      banners.remove(key);
    }
  }
}

// There is no pruneStaleDismissals. It capped ONE flat array of `chat:code` keys
// at 200 entries, which is the bound a global list needs; the per-chat store owns
// the bound now and applies it per chat with oldest-first eviction.

/** Auto-clear transient banners (rate_limit) on successful turn end. */
export function onTurnEnded(chatID: string): void {
  removeBanner(chatID, "rate_limit");
}
