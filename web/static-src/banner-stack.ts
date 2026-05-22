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
  chatID: string, code: string, message: string,
  level: BannerLevel, dismissible: boolean,
): void {
  if (isDismissed(chatID, code)) return;
  const key = bannerKey(chatID, code);
  const existing = banners.get(key);
  if (existing !== undefined) {
    // Replace message in place.
    const msg = existing.el.querySelector(".banner-msg");
    if (msg !== null) msg.textContent = message;
    existing.message = message;
    return;
  }
  const el = document.createElement("div");
  el.className = `banner banner-${level}`;
  el.setAttribute("role", "alert");
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
  if (entry === undefined) return;
  entry.el.remove();
  banners.delete(key);
  clearDismiss(chatID, code);
}

/** Remove all banners matching any of the given codes for a chat. */
export function clearBannerCodes(chatID: string, codes: string[]): void {
  for (const code of codes) removeBanner(chatID, code);
}

/** Re-render: show only banners for the active chat. */
export function renderStack(): void {
  const container = $.bannerStack;
  container.replaceChildren();
  const activeID = getActiveId();
  for (const entry of banners.values()) {
    if (entry.chatID === activeID) container.appendChild(entry.el);
  }
}

/** Auto-clear transient banners (rate_limit) on successful turn end. */
export function onTurnEnded(chatID: string): void {
  removeBanner(chatID, "rate_limit");
}
