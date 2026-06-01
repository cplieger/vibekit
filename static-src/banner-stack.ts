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
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActiveId } from "./store.js";
import { load, save } from "./ui-state.js";
import { reconcile } from "./reconcile.js";
import type { BannerLevel } from "./types.js";

interface BannerEntry {
  readonly code: string;
  readonly chatID: string;
  message: string;
  readonly level: BannerLevel;
  readonly dismissible: boolean;
  el: HTMLDivElement;
}

const banners = new Map<string, BannerEntry>();

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
    // Replace message in place.
    const msg = existing.el.querySelector(".banner-msg");
    if (msg !== null) {
      msg.textContent = message;
    }
    existing.message = message;
    return;
  }
  const el = document.createElement("div");
  el.className = `banner banner-${level}`;
  el.setAttribute("role", level === "error" ? "alert" : "status");
  const msg = document.createElement("span");
  msg.className = "banner-msg";
  msg.textContent = message;
  el.appendChild(msg);
  if (dismissible) {
    const btn = document.createElement("button");
    btn.type = "button";
    btn.className = "banner-dismiss";
    btn.textContent = "\u00d7";
    btn.ariaLabel = "Dismiss";
    btn.addEventListener("click", () => {
      removeBanner(chatID, code);
      persistDismiss(chatID, code);
    });
    el.appendChild(btn);
  }
  const entry: BannerEntry = { code, chatID, message, level, dismissible, el };
  banners.set(key, entry);
  renderStack();
}

function removeBanner(chatID: string, code: string): void {
  const key = bannerKey(chatID, code);
  const entry = banners.get(key);
  if (entry === undefined) {
    return;
  }
  entry.el.remove();
  banners.delete(key);
  clearDismiss(chatID, code);
}

/** Remove all banners matching any of the given codes for a chat. */
export function clearBannerCodes(chatID: string, codes: string[]): void {
  for (const code of codes) {
    removeBanner(chatID, code);
  }
}

/** Drop every banner (and its dismissed-state persistence) for a
 *  chat that no longer exists. Called from the chat_deleted bus
 *  handler so orphan BannerEntry objects + localStorage entries
 *  don't accumulate over a long session.
 *
 *  We iterate the Map keys defensively (a delete inside the loop
 *  would invalidate a forward iterator on some engines) and also
 *  prune the dismissed_banners localStorage entries for that chat. */
export function clearBannersForChat(chatID: string): void {
  const prefix = `${chatID}:`;
  // 1. Detach DOM + drop in-memory entries.
  for (const key of [...banners.keys()]) {
    if (key.startsWith(prefix)) {
      const entry = banners.get(key);
      if (entry !== undefined) {
        entry.el.remove();
      }
      banners.delete(key);
    }
  }
  // 2. Prune persisted dismissals so localStorage doesn't grow forever.
  const state = load();
  const filtered = state.dismissed_banners.filter((k) => !k.startsWith(prefix));
  if (filtered.length !== state.dismissed_banners.length) {
    save({ dismissed_banners: filtered });
  }
}

/** Re-render: show only banners for the active chat. */
export function renderStack(): void {
  const container = $.bannerStack;
  if (!container.hasAttribute("aria-label")) {
    container.setAttribute("aria-label", "Notifications");
    container.setAttribute("aria-live", "polite");
  }
  const activeID = getActiveId();
  const visible: BannerEntry[] = [];
  for (const entry of banners.values()) {
    if (entry.chatID === activeID) {
      visible.push(entry);
    }
  }
  reconcile(container, visible, {
    key: (e: BannerEntry) => bannerKey(e.chatID, e.code),
    // Reuse the entry-owned element so banner identity (and any
    // ongoing transitions / focus) persists across re-renders.
    mount: (e: BannerEntry) => e.el,
  });
  pruneStaleDissmissals();
}

const PRUNE_THRESHOLD = 200;

/** Prune dismissed_banners entries that exceed the threshold. Keeps
 *  only the most recent entries (tail of the array). */
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
