// ---------------------------------------------------------------------------
// Banner stack: per-chat persistent banners above the transcript.
//
// Init errors (agent_not_found, agent_config_error, model_not_found),
// rate limits, and MCP failures surface here. Each banner is keyed on
// (chat_id, code) so the same error replaces rather than duplicates.
//
// Banners are per-device (dismissal state in localStorage) and auto-
// clear when the underlying condition resolves (e.g. agent_switched
// clears agent_not_found + agent_config_error).
//
// State is a createCollection<BannerEntry>; the stack is rendered by a single
// bindList over a computed active-chat view, so add / remove / chat-switch all
// flow through ONE reactive render source (no direct DOM mutation that could
// desync the reconcile view).
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { activeSession } from "./store.js";
import { load, save } from "./ui-state.js";
import { el, createCollection, bindList, computed } from "@cplieger/reactive";
import type { BannerLevel } from "./types.js";

interface BannerEntry {
  readonly code: string;
  readonly chatID: string;
  message: string;
  readonly level: BannerLevel;
  readonly dismissible: boolean;
  el: HTMLDivElement;
}

const banners = createCollection<BannerEntry>((e) => bannerKey(e.chatID, e.code));

// Visible banners = those for the active chat. Tracks the collection structure
// + activeSession (so a chat switch re-renders) and stays shallow-equal so a
// no-op recompute doesn't reconcile.
const visibleIds = computed<readonly string[]>(
  () => {
    const activeID = activeSession.value?.id ?? "";
    return banners
      .items()
      .filter((e) => e.chatID === activeID)
      .map((e) => bannerKey(e.chatID, e.code));
  },
  { equals: (a, b) => a.length === b.length && a.every((x, i) => x === b[i]) },
);

let bound = false;
function ensureBound(): void {
  if (bound) {
    return;
  }
  bound = true;
  const container = $.bannerStack;
  container.setAttribute("aria-label", "Notifications");
  container.setAttribute("aria-live", "polite");
  // Reuse the entry-owned element so banner identity (and any ongoing
  // transitions / focus) persists across re-renders.
  bindList(
    container,
    { ids: visibleIds, signalFor: (id) => banners.signalFor(id) },
    { mount: (e) => e.el },
  );
}

function bannerKey(chatID: string, code: string): string {
  return `${chatID}:${code}`;
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

export function showBanner(
  chatID: string,
  code: string,
  message: string,
  level: BannerLevel,
  dismissible: boolean,
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
    return;
  }
  const msg = el("span", { className: "banner-msg" }, message);
  const node = el(
    "div",
    {
      className: `banner banner-${level}`,
      role: level === "error" ? "alert" : "status",
    },
    msg,
  ) as HTMLDivElement;
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
  pruneStaleDissmissals();
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

/** Ensure the stack is mounted/bound. The bindList renders reactively from the
 *  collection + activeSession, so a chat switch re-renders automatically; this
 *  is retained for the chat-switch call site and is idempotent. */
export function renderStack(): void {
  ensureBound();
}

const PRUNE_THRESHOLD = 200;

/** Prune dismissed_banners entries that exceed the threshold. Keeps only the
 *  most recent entries (tail of the array). */
function pruneStaleDissmissals(): void {
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
