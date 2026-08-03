// ---------------------------------------------------------------------------
// Message view: signal-driven reactive renderer.
//
// One effect watches store.version + the active session's messages array
// and reconciles them into $.messages by message id. Per-message factories
// (buildUser / buildAssistant / buildEvent) own initial DOM construction;
// per-message updaters (updateAssistant, updateEvent) own incremental
// changes.
//
// Assistant bodies are composed ENTIRELY from the fundamentals/ primitives by
// the single block dispatcher in messages-blocks.ts — this module is the shell
// that mounts and updates them by message identity, owns the streaming-effect
// registry + avatar rows, and drives turn finalization from store state.
//
// The "liquid" feel comes from CSS:
//   - @starting-style + transitions on `.msg-row` for entry animations
//   - .streaming class on the active assistant bubble (subtle pulse)
//   - interpolate-size: allow-keywords on :root so height: auto can
//     animate (set in css/01-tokens.css)
//   - content-visibility: auto on rows so off-screen messages don't pay
//     paint cost
// ---------------------------------------------------------------------------

import type { Message } from "./types.js";
import { getActive, getActiveId, get, messagesVersion, activeSession } from "./store.js";
import { clearStreamingSig, clearReasoningSig, clearAllBlockSigs } from "./store-signals.js";
import { effect, el } from "@cplieger/reactive";
import { reconcile, KEY_ATTR, type ReconcileSpec } from "./reconcile.js";
import { $ } from "./dom.js";
import { getScrollEl, scrollToBottom, resetScrollState, setLoadMore } from "./scroll.js";
import {
  buildTurnHeader,
  updateTurnHeader,
  type TurnHeaderData,
} from "./fundamentals/turn-header.js";
import {
  buildTurnFooter,
  updateTurnFooter,
  hasTurnSummary,
  type TurnSummaryData,
} from "./fundamentals/turn-footer.js";
import { projectTurns, turnLedger, turnAnchorID, type Turn } from "./turns.js";
import { mountTurnRail, observeTurns, resetTurnRail, loadTurnRail } from "./turn-rail.js";
import {
  buildAssistantBody,
  updateAssistantBody,
  finalizeAssistantBody,
  disposeAssistantBody,
  resetBlockRenders,
  refreshGroupHeader,
  initBlockRenderer,
} from "./messages-blocks.js";
import { explainError as explainErrorAction } from "./actions/messages.js";
import { rewindChat } from "./actions/rewind.js";
import { initMessageActions, clearActionBindings } from "./messages-actions.js";
import { confirm as confirmDialog } from "./confirm.js";
import { disposeAllToolEffects, initToolCallbacks } from "./messages-tools.js";
import { buildEvent, updateEvent, buildSystemFallback } from "./messages-events.js";
import { attachTurnActions, initTurnActionCallbacks } from "./messages-turn-actions.js";
import { syncCodeReferences } from "./code-refs.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";

// ---------------------------------------------------------------------------
// Public re-exports
// ---------------------------------------------------------------------------

export { getScrollEl, scrollToBottom, setLoadMore };
// Re-exported for the same reason the scroll helpers are: this module owns the
// rail (it mounts it and feeds it the painted cards), so chat.ts reaching the
// rail THROUGH here keeps ownership in one place instead of two modules driving
// the same surface.
export { loadTurnRail };

// ---------------------------------------------------------------------------
// Module state
// ---------------------------------------------------------------------------

const messagesEl = $.messages;

/** Per-message-id metadata kept for the duration the message is mounted. */
interface MessageState {
  el: HTMLElement;
  /** True while this is the live streaming bubble; transitions to false
   *  on turn end via finalizeStreamingIfNeeded(). */
  streaming: boolean;
}
const messageStates = new Map<string, MessageState>();

/** bindLoadingState unsubs accumulated within a chat. Cleared on
 *  message removal (via reconcile.onRemove) and on chat switch. */
const bindUnbinds = new Map<string, (() => void)[]>();
function pushBind(key: string, unbind: () => void): void {
  let arr = bindUnbinds.get(key);
  if (arr === undefined) {
    arr = [];
    bindUnbinds.set(key, arr);
  }
  arr.push(unbind);
}

/** Per-message streaming effect cleanups. Disposed both on turn end
 *  (when the message stays mounted but stops streaming) and on full
 *  unmount. A single message can register multiple cleanups (one per
 *  live text/thinking block + subagent/todo status effects). Separate
 *  from bindUnbinds so tool-card loading-state bindings survive turn end. */
const streamingEffects = new Map<string, (() => void)[]>();
function pushStreamingEffect(id: string, fn: () => void): void {
  const arr = streamingEffects.get(id);
  if (arr === undefined) {
    streamingEffects.set(id, [fn]);
  } else {
    arr.push(fn);
  }
}
function disposeStreamingEffect(id: string): void {
  const arr = streamingEffects.get(id);
  if (arr !== undefined) {
    for (const fn of arr) {
      fn();
    }
    streamingEffects.delete(id);
  }
  clearStreamingSig(id);
  clearReasoningSig(id);
}

/** IDs of messages newly appended at the end since the last paint
 *  (i.e. streaming arrival). buildMessage uses this to mark new rows
 *  with `data-chat-entry` so the CSS entry animation plays for new
 *  content but NOT for chat-switch replay or pagination prepend. */
const appendNewIds = new Set<string>();
let lastNewestId: string | undefined;
let lastActiveId: string | undefined;

/** Per-paint stagger index for messages mounted in a single reconcile
 *  pass (chat-switch). Indexed from the bottom so the most-recent
 *  messages animate first, with a cap at 8 to prevent the cascade
 *  from looking laggy on long histories. */
const staggerIndex = new Map<string, number>();

// Avatars (parsed once, cloned per use).
const KIRO_AVATAR =
  '<svg class="avatar-icon" width="18" height="18" viewBox="5.9 5.9 36.2 36.2" fill="none"><path d="M35.08 17.80C35.05 17.92 34.99 18.27 34.95 18.50C34.91 18.72 34.87 18.95 34.84 19.16C34.81 19.38 34.78 19.60 34.76 19.81C34.75 20.02 34.74 20.22 34.75 20.43C34.77 20.63 34.79 20.83 34.84 21.03C34.89 21.23 34.96 21.42 35.04 21.60C35.12 21.79 35.22 21.97 35.33 22.15C35.44 22.33 35.57 22.50 35.70 22.67C35.83 22.85 35.98 23.02 36.12 23.20C36.27 23.38 36.42 23.56 36.57 23.74C36.72 23.92 36.87 24.11 37.01 24.30C37.15 24.50 37.29 24.69 37.41 24.89C37.54 25.09 37.65 25.30 37.75 25.50C37.85 25.70 37.93 25.91 38.00 26.11C38.06 26.31 38.10 26.52 38.13 26.71C38.15 26.91 38.16 27.10 38.14 27.29C38.12 27.47 38.09 27.65 38.03 27.83C37.98 28.00 37.90 28.17 37.81 28.33C37.71 28.49 37.60 28.65 37.47 28.79C37.34 28.94 37.18 29.08 37.02 29.21C36.85 29.35 36.67 29.47 36.48 29.59C36.28 29.70 36.08 29.81 35.86 29.90C35.65 30.00 35.43 30.09 35.21 30.18C34.99 30.26 34.76 30.34 34.54 30.41C34.32 30.49 34.10 30.56 33.88 30.64C33.67 30.71 33.46 30.78 33.25 30.87C33.04 30.95 32.84 31.04 32.65 31.14C32.46 31.24 32.27 31.35 32.09 31.48C31.92 31.61 31.75 31.76 31.60 31.92C31.46 32.08 31.32 32.25 31.20 32.44C31.08 32.62 30.97 32.82 30.87 33.02C30.77 33.21 30.69 33.42 30.60 33.63C30.51 33.84 30.43 34.06 30.34 34.28C30.26 34.49 30.17 34.71 30.08 34.93C29.99 35.14 29.90 35.36 29.80 35.56C29.70 35.77 29.59 35.97 29.47 36.16C29.36 36.34 29.24 36.52 29.11 36.68C28.98 36.83 28.85 36.98 28.71 37.10C28.57 37.22 28.43 37.33 28.29 37.41C28.15 37.49 28.00 37.56 27.86 37.61C27.71 37.65 27.56 37.68 27.41 37.70C27.26 37.71 27.10 37.71 26.94 37.69C26.78 37.67 26.61 37.63 26.44 37.58C26.27 37.52 26.09 37.45 25.91 37.37C25.73 37.28 25.55 37.17 25.37 37.06C25.19 36.94 25.01 36.81 24.84 36.67C24.66 36.54 24.48 36.38 24.31 36.23C24.14 36.08 23.96 35.93 23.79 35.77C23.62 35.62 23.45 35.46 23.27 35.31C23.09 35.15 22.92 35.00 22.72 34.86C22.53 34.72 22.34 34.59 22.12 34.47C21.91 34.35 21.68 34.24 21.45 34.16C21.21 34.08 20.96 34.02 20.72 33.98C20.48 33.94 20.23 33.93 19.98 33.93C19.74 33.93 19.50 33.95 19.27 33.97C19.03 33.99 18.80 34.02 18.56 34.05C18.33 34.08 18.10 34.12 17.87 34.15C17.65 34.18 17.42 34.21 17.20 34.23C16.98 34.25 16.76 34.27 16.55 34.27C16.34 34.28 16.14 34.27 15.95 34.25C15.76 34.24 15.58 34.21 15.42 34.17C15.26 34.13 15.11 34.08 14.99 34.02C14.86 33.97 14.75 33.90 14.65 33.83C14.55 33.76 14.47 33.68 14.40 33.60C14.33 33.51 14.27 33.43 14.21 33.32C14.16 33.22 14.11 33.11 14.07 32.98C14.03 32.85 14.00 32.71 13.98 32.55C13.96 32.40 13.95 32.22 13.95 32.04C13.95 31.86 13.96 31.67 13.98 31.47C14.00 31.27 14.03 31.05 14.07 30.84C14.10 30.63 14.15 30.41 14.20 30.19C14.24 29.96 14.30 29.74 14.34 29.51C14.39 29.27 14.44 29.04 14.48 28.79C14.52 28.55 14.56 28.30 14.57 28.03C14.58 27.77 14.59 27.49 14.56 27.21C14.53 26.94 14.48 26.65 14.41 26.37C14.33 26.10 14.22 25.83 14.10 25.58C13.99 25.33 13.85 25.10 13.71 24.88C13.57 24.66 13.42 24.46 13.27 24.26C13.13 24.07 12.97 23.88 12.83 23.70C12.69 23.51 12.55 23.34 12.41 23.16C12.28 22.99 12.15 22.81 12.04 22.64C11.93 22.48 11.83 22.31 11.74 22.15C11.65 21.99 11.58 21.83 11.53 21.69C11.47 21.55 11.43 21.42 11.41 21.30C11.38 21.18 11.37 21.07 11.37 20.98C11.37 20.89 11.38 20.82 11.40 20.74C11.42 20.67 11.44 20.62 11.47 20.55C11.50 20.49 11.54 20.43 11.59 20.36C11.65 20.30 11.71 20.22 11.79 20.15C11.87 20.07 11.97 19.99 12.09 19.91C12.21 19.84 12.35 19.75 12.50 19.67C12.65 19.60 12.82 19.52 13.00 19.44C13.18 19.37 13.38 19.30 13.58 19.22C13.78 19.15 13.99 19.08 14.21 19.01C14.43 18.94 14.66 18.87 14.89 18.78C15.13 18.70 15.37 18.61 15.62 18.50C15.86 18.39 16.12 18.27 16.38 18.12C16.63 17.97 16.90 17.79 17.13 17.59C17.37 17.38 17.60 17.14 17.80 16.91C17.99 16.67 18.16 16.40 18.31 16.15C18.46 15.91 18.58 15.65 18.69 15.41C18.80 15.17 18.89 14.94 18.98 14.71C19.07 14.48 19.15 14.26 19.23 14.05C19.32 13.85 19.39 13.64 19.48 13.46C19.56 13.27 19.64 13.09 19.72 12.93C19.81 12.77 19.89 12.62 19.98 12.50C20.06 12.37 20.15 12.26 20.22 12.17C20.30 12.09 20.38 12.02 20.44 11.97C20.50 11.92 20.56 11.89 20.60 11.87C20.65 11.84 20.69 11.84 20.72 11.83C20.76 11.82 20.79 11.82 20.84 11.82C20.89 11.82 20.94 11.82 21.01 11.84C21.08 11.85 21.16 11.87 21.26 11.91C21.36 11.95 21.47 12.00 21.60 12.06C21.72 12.13 21.86 12.21 22.00 12.30C22.14 12.40 22.29 12.51 22.45 12.63C22.60 12.75 22.77 12.89 22.93 13.03C23.10 13.17 23.27 13.32 23.45 13.48C23.64 13.63 23.82 13.80 24.03 13.96C24.24 14.12 24.46 14.29 24.71 14.45C24.96 14.60 25.23 14.77 25.52 14.90C25.81 15.03 26.14 15.15 26.46 15.23C26.78 15.31 27.12 15.35 27.43 15.38C27.75 15.40 28.06 15.39 28.35 15.38C28.63 15.36 28.90 15.32 29.16 15.28C29.41 15.25 29.65 15.20 29.89 15.16C30.12 15.12 30.44 15.07 30.55 15.05L29.70 9.92C29.58 9.94 29.18 10.01 28.96 10.05C28.74 10.08 28.54 10.12 28.38 10.15C28.21 10.17 28.08 10.19 27.98 10.20C27.88 10.21 27.81 10.20 27.77 10.20C27.72 10.20 27.72 10.20 27.70 10.20C27.68 10.19 27.69 10.20 27.65 10.18C27.62 10.17 27.57 10.15 27.50 10.10C27.42 10.05 27.32 9.99 27.20 9.89C27.08 9.80 26.93 9.68 26.77 9.55C26.60 9.41 26.41 9.25 26.21 9.09C26.01 8.92 25.78 8.74 25.54 8.56C25.30 8.39 25.04 8.20 24.76 8.02C24.48 7.85 24.18 7.67 23.86 7.52C23.54 7.37 23.20 7.22 22.84 7.11C22.49 7.00 22.10 6.90 21.72 6.85C21.33 6.80 20.92 6.78 20.51 6.82C20.11 6.85 19.70 6.92 19.31 7.04C18.92 7.16 18.54 7.33 18.19 7.53C17.84 7.72 17.51 7.96 17.22 8.22C16.93 8.47 16.67 8.75 16.44 9.03C16.21 9.32 16.01 9.62 15.82 9.91C15.64 10.21 15.49 10.51 15.35 10.80C15.22 11.09 15.10 11.38 14.99 11.65C14.89 11.92 14.80 12.18 14.72 12.42C14.64 12.65 14.57 12.87 14.50 13.07C14.44 13.26 14.38 13.42 14.33 13.56C14.27 13.69 14.23 13.80 14.19 13.87C14.16 13.95 14.14 13.99 14.11 14.03C14.09 14.07 14.09 14.08 14.07 14.11C14.04 14.14 14.02 14.16 13.96 14.20C13.90 14.24 13.83 14.29 13.71 14.35C13.60 14.41 13.46 14.48 13.29 14.55C13.12 14.62 12.91 14.70 12.69 14.78C12.47 14.87 12.23 14.96 11.98 15.07C11.72 15.18 11.45 15.29 11.17 15.43C10.90 15.56 10.61 15.71 10.33 15.88C10.05 16.05 9.76 16.24 9.49 16.45C9.22 16.67 8.95 16.91 8.70 17.17C8.46 17.44 8.23 17.73 8.03 18.05C7.84 18.36 7.66 18.71 7.54 19.07C7.41 19.42 7.33 19.80 7.28 20.17C7.24 20.55 7.24 20.93 7.27 21.29C7.30 21.66 7.38 22.02 7.48 22.36C7.57 22.70 7.71 23.02 7.85 23.33C7.99 23.64 8.16 23.92 8.33 24.20C8.50 24.47 8.68 24.72 8.86 24.96C9.04 25.19 9.23 25.41 9.40 25.62C9.57 25.82 9.75 26.01 9.90 26.18C10.06 26.35 10.20 26.51 10.32 26.65C10.44 26.79 10.55 26.91 10.63 27.01C10.71 27.12 10.77 27.20 10.82 27.28C10.87 27.35 10.90 27.40 10.93 27.46C10.95 27.52 10.97 27.57 10.99 27.64C11.00 27.72 11.02 27.80 11.02 27.92C11.03 28.03 11.02 28.17 11.01 28.34C11.00 28.50 10.97 28.70 10.94 28.91C10.92 29.12 10.88 29.35 10.84 29.60C10.81 29.85 10.77 30.12 10.75 30.39C10.72 30.67 10.69 30.97 10.68 31.27C10.68 31.57 10.68 31.88 10.70 32.19C10.73 32.51 10.76 32.83 10.84 33.15C10.91 33.46 11.00 33.79 11.12 34.09C11.25 34.40 11.41 34.70 11.59 34.98C11.78 35.26 12.00 35.52 12.24 35.76C12.49 35.99 12.76 36.19 13.05 36.36C13.33 36.54 13.64 36.67 13.94 36.78C14.25 36.89 14.57 36.96 14.88 37.02C15.19 37.07 15.51 37.09 15.81 37.10C16.12 37.11 16.43 37.09 16.72 37.07C17.01 37.04 17.29 37.00 17.56 36.96C17.84 36.91 18.10 36.86 18.34 36.81C18.58 36.76 18.81 36.71 19.02 36.66C19.24 36.62 19.43 36.57 19.60 36.54C19.78 36.51 19.93 36.49 20.07 36.48C20.20 36.47 20.32 36.46 20.42 36.47C20.53 36.47 20.62 36.48 20.72 36.50C20.82 36.53 20.91 36.55 21.02 36.60C21.13 36.65 21.25 36.72 21.38 36.80C21.52 36.88 21.66 36.98 21.82 37.10C21.98 37.22 22.15 37.35 22.33 37.50C22.51 37.64 22.71 37.80 22.91 37.95C23.12 38.11 23.33 38.27 23.56 38.42C23.79 38.58 24.03 38.74 24.28 38.88C24.53 39.02 24.80 39.15 25.07 39.26C25.34 39.37 25.62 39.47 25.91 39.54C26.19 39.61 26.49 39.65 26.79 39.67C27.08 39.68 27.38 39.67 27.67 39.62C27.96 39.57 28.25 39.49 28.53 39.38C28.80 39.27 29.06 39.13 29.30 38.97C29.54 38.80 29.76 38.61 29.97 38.40C30.17 38.20 30.36 37.97 30.52 37.74C30.69 37.51 30.84 37.26 30.97 37.01C31.11 36.77 31.23 36.51 31.33 36.26C31.44 36.01 31.53 35.76 31.62 35.52C31.71 35.28 31.79 35.04 31.86 34.82C31.93 34.60 32.00 34.38 32.07 34.19C32.14 34.00 32.20 33.82 32.27 33.65C32.34 33.49 32.40 33.35 32.48 33.22C32.55 33.09 32.62 32.99 32.70 32.88C32.78 32.78 32.87 32.69 32.97 32.61C33.08 32.52 33.19 32.44 33.32 32.36C33.46 32.28 33.61 32.20 33.78 32.12C33.95 32.04 34.14 31.96 34.34 31.88C34.54 31.80 34.76 31.72 34.99 31.63C35.21 31.55 35.45 31.45 35.69 31.35C35.92 31.25 36.17 31.14 36.41 31.02C36.64 30.89 36.89 30.76 37.11 30.61C37.34 30.46 37.57 30.30 37.77 30.12C37.98 29.95 38.17 29.75 38.35 29.55C38.52 29.34 38.67 29.11 38.80 28.88C38.92 28.65 39.02 28.39 39.10 28.14C39.17 27.89 39.21 27.62 39.22 27.36C39.24 27.09 39.22 26.83 39.18 26.56C39.14 26.30 39.07 26.04 38.99 25.79C38.90 25.53 38.79 25.28 38.67 25.04C38.55 24.80 38.41 24.57 38.26 24.35C38.11 24.12 37.95 23.91 37.80 23.70C37.64 23.50 37.47 23.30 37.32 23.12C37.16 22.93 37.00 22.75 36.86 22.58C36.71 22.41 36.57 22.25 36.45 22.09C36.33 21.94 36.21 21.79 36.12 21.64C36.03 21.50 35.95 21.36 35.88 21.22C35.82 21.08 35.77 20.95 35.73 20.81C35.70 20.67 35.68 20.53 35.66 20.37C35.65 20.21 35.65 20.05 35.67 19.87C35.68 19.69 35.70 19.50 35.73 19.30C35.76 19.10 35.80 18.88 35.84 18.66C35.88 18.43 35.94 18.07 35.96 17.95Z" fill="#9046FF"/><circle cx="30.12" cy="12.48" r="2.60" fill="#9046FF"/><circle cx="35.52" cy="17.88" r="0.45" fill="#9046FF"/></svg>';
// There is no USER_AVATAR: the user's request is the turn card's header band,
// so nothing needs a face to attribute it.

function svgTemplate(markup: string): () => Node {
  const tpl = document.createElement("template");
  tpl.innerHTML = markup;
  const content = tpl.content;
  return () => content.cloneNode(true);
}
const cloneKiroAvatar = svgTemplate(KIRO_AVATAR);

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

let mounted = false;

// Initialize callbacks for extracted modules.
initToolCallbacks({
  pushBind,
  refreshGroupHeader,
  explainError,
});
initTurnActionCallbacks({ svgTemplate });
initBlockRenderer({ pushStreamingEffect, makeRow });
// The refusal callout's Rewind CTA reuses the standard rewind flow (confirm →
// branch → open the new tab). Injected — refusal.ts can't import messages.ts.
setRefusalRewindHandler((m) => {
  void handleRewindClick(m).catch((e: unknown) => {
    console.warn("refusal rewind failed", e);
  });
});

/** Mount the chat view. Idempotent. Called once at app boot from app.ts.
 *  Subscribes to store.version and reconciles the message list on every
 *  bump. Streaming markdown chunks flow through per-block signals bound
 *  at mount, not through this effect. */
export function mountChatView(): void {
  if (mounted) {
    return;
  }
  mounted = true;
  initMessageActions();
  // The rail lives in the transcript's positioned outer wrapper rather than in
  // the scroller, so it stays put instead of scrolling away with the content.
  mountTurnRail($.messagesWrapOuter);
  effect(() => {
    void messagesVersion.value;
    void activeSession.value;
    paint();
  });
}

function paint(): void {
  const session = getActive();
  if (session === undefined) {
    // No session for the active id. Only clear when there is genuinely NO
    // active chat (all closed). A transient undefined during a chat switch or
    // a not-yet-loaded session must NOT wipe the DOM — that empty reconcile
    // pass, immediately followed by a re-populate, was the flashing bug.
    if (getActiveId() === "") {
      teardownAll();
    }
    return;
  }
  // Mark genuinely-new appended messages (streaming arrival) so only
  // those get the entry animation. Chat-switches and paginated prepends
  // are silent (no animation).
  appendNewIds.clear();
  staggerIndex.clear();
  const isChatSwitch = lastActiveId !== session.id;
  if (!isChatSwitch && lastNewestId !== undefined) {
    // Reverse scan: lastNewestId is always near the tail (set at end of
    // previous paint), so scanning backward is O(1) amortized.
    let idx = -1;
    for (let i = session.messages.length - 1; i >= 0; i--) {
      if (session.messages[i]?.id === lastNewestId) {
        idx = i;
        break;
      }
    }
    if (idx >= 0) {
      for (let i = idx + 1; i < session.messages.length; i++) {
        const id = session.messages[i]?.id;
        if (id !== undefined) {
          appendNewIds.add(id);
        }
      }
    }
  } else if (isChatSwitch) {
    // Cascade the last 8 messages on chat-switch so they stagger
    // visually rather than flashing in together.
    const total = session.messages.length;
    for (let i = Math.max(0, total - 8); i < total; i++) {
      const id = session.messages[i]?.id;
      if (id !== undefined) {
        staggerIndex.set(id, total - 1 - i);
      }
    }
  }
  reconcile(messagesEl, projectTurns(session.messages, session.thinking), turnSpec);
  // Tell the rail which cards exist so it can track the turn in view. Re-run per
  // paint because the set changes as pages load and turns arrive.
  observeTurns(messagesEl.querySelectorAll<HTMLElement>(":scope > .turn"));
  finalizeStreamingIfNeeded(session.messages);
  lastNewestId = session.messages[session.messages.length - 1]?.id;
  lastActiveId = session.id;
}

/**
 * rewindConfirmText builds the confirmation shown before branching a new
 * chat from a past turn. It surfaces what the user is rewinding from — the
 * prompt preview plus the following assistant turn's tool-call and
 * touched-file counts — mirroring kiro-cli 2.7's enriched /rewind preview,
 * built from data vibekit already persists (no extra round-trip). All
 * field reads are defensive so a sparse/legacy message never throws.
 */
function rewindConfirmText(m: Message, next: Message | undefined): string {
  const promptRaw = (m.content ?? "").trim().replace(/\s+/g, " ");
  const prompt = promptRaw.length > 100 ? promptRaw.slice(0, 100) + "…" : promptRaw;
  const lines = ["Rewind from this turn?", ""];
  if (prompt.length > 0) {
    lines.push(`Prompt: "${prompt}"`);
  }
  if (next?.role === "assistant") {
    const calls = next.tool_calls ?? [];
    if (calls.length > 0) {
      const files = [
        ...new Set(
          calls.flatMap((c) => (c.locations ?? []).map((l) => l.path.split("/").pop() ?? l.path)),
        ),
      ];
      const toolPart = `${String(calls.length)} tool call${calls.length === 1 ? "" : "s"}`;
      const filePart =
        files.length > 0
          ? `, ${String(files.length)} file${files.length === 1 ? "" : "s"} touched (${files.slice(0, 4).join(", ")}${files.length > 4 ? ", …" : ""})`
          : "";
      lines.push(`This turn's response: ${toolPart}${filePart}.`);
    }
  }
  lines.push("");
  lines.push(
    "Creates a new chat branched from this point. File contents on disk are not affected (use Restore for that).",
  );
  return lines.join("\n");
}

/**
 * handleRewindClick confirms the rewind, dispatches it, then opens AND
 * activates the returned branch chat. Mirrors chat.ts's openPreviousSession
 * pattern: refresh the header list so the branch session exists in the store,
 * then open its tab (openChatTab activates it via onShow → activateChatView,
 * which loads the branch's messages). chat.ts / store-load.ts are imported
 * dynamically because chat.ts statically imports this module — a static import
 * back would be a cycle. The confirm uses the app's confirmDialog (not the
 * native, unstyled, focus-trap-less window.confirm).
 */
async function handleRewindClick(m: Message): Promise<void> {
  const session = getActive();
  if (session === undefined) {
    return;
  }
  const turnIdx = session.messages.findIndex((msg) => msg.id === m.id);
  if (turnIdx < 0) {
    return;
  }
  const proceed = await confirmDialog(rewindConfirmText(m, session.messages[turnIdx + 1]));
  if (!proceed) {
    return;
  }
  const res = await rewindChat.dispatch({ chatID: session.id, turnIndex: turnIdx });
  const newID = res?.rewind_id ?? "";
  if (newID === "") {
    return;
  }
  const [storeLoad, chatMod] = await Promise.all([import("./store-load.js"), import("./chat.js")]);
  await storeLoad.loadList();
  chatMod.openChatTab(newID, get(newID)?.name ?? `Rewind: ${session.name}`);
}

/** Clear all per-message state, e.g. when the last chat is closed (active
 *  session genuinely gone). A real session arriving repaints from scratch. */
function teardownAll(): void {
  for (const arr of bindUnbinds.values()) {
    for (const fn of arr) {
      fn();
    }
  }
  bindUnbinds.clear();
  for (const id of [...streamingEffects.keys()]) {
    disposeStreamingEffect(id);
  }
  disposeAllToolEffects();
  resetBlockRenders();
  clearAllBlockSigs();
  messageStates.clear();
  clearActionBindings();
  resetScrollState();
  resetTurnRail();
  reconcile(messagesEl, [] as Turn[], turnSpec);
}

// ---------------------------------------------------------------------------
// Reconcile specs
//
// Two levels, both keyed. The outer list is TURNS keyed by the turn's opening
// message id; each card's `.turn-body` is an inner keyed list of that turn's
// messages. Nesting is safe because reconcile only considers children carrying
// its key attribute, so a card's unkeyed header and footer are invisible to
// the inner pass and the inner pass is invisible to the outer one.
// ---------------------------------------------------------------------------

const turnSpec: ReconcileSpec<Turn> = {
  key: (t) => t.id,
  mount: (t) => {
    const card = buildTurn(t);
    // Only animate a genuinely-new turn; chat-switch replay and pagination
    // prepends mount silently. A new turn's id is its trigger's message id,
    // which is what paint() records in appendNewIds.
    if (appendNewIds.has(t.id)) {
      card.setAttribute("data-chat-entry", "");
    }
    const stagger = staggerIndex.get(t.id);
    if (stagger !== undefined && stagger > 0) {
      card.style.setProperty("--stagger-index", String(stagger));
    }
    return card;
  },
  update: updateTurn,
  onRemove: (card) => {
    // Dispose the body's messages: the inner reconcile never runs again for a
    // removed card, so its onRemove would not fire on its own.
    const rows = card.querySelectorAll<HTMLElement>(`:scope > .turn-body > [${KEY_ATTR}]`);
    for (const row of rows) {
      const key = row.getAttribute(KEY_ATTR);
      if (key !== null) {
        disposeMessage(key);
      }
    }
  },
};

const messageSpec: ReconcileSpec<Message> = {
  key: (m) => m.id,
  mount: (m) => {
    const node = buildMessage(m);
    // Licensed-code attribution footnote + model-refusal callout. One call
    // site here + in update() covers mount + update, keyed off
    // m.code_references / m.refusal.
    if (m.role === "assistant") {
      syncCodeReferences(node, m);
      syncRefusal(node, m);
    }
    // Only animate genuinely-new appended messages; chat-switch replay
    // and pagination prepends mount silently. See paint() for how
    // appendNewIds is populated.
    if (appendNewIds.has(m.id)) {
      node.setAttribute("data-chat-entry", "");
    }
    const stagger = staggerIndex.get(m.id);
    if (stagger !== undefined && stagger > 0) {
      node.style.setProperty("--stagger-index", String(stagger));
    }
    // isLikelyLiveStreaming already returns false for non-assistant roles.
    const liveStreaming = isLikelyLiveStreaming(m);
    messageStates.set(m.id, { el: node, streaming: liveStreaming });
    // Historical / reloaded assistant turns finalize at mount — they never
    // pass through the live-stream finalize path — so attach the copy/export
    // turn-actions row here. Live turns get it later via finalizeTurn when the
    // stream ends. (This is why switching away and back to a chat used to drop
    // the buttons: re-mounted turns were finalized but never decorated.)
    if (m.role === "assistant" && !liveStreaming) {
      const bubble = node.querySelector<HTMLDivElement>(".message.assistant");
      if (bubble !== null) {
        attachTurnActions(bubble);
      }
    }
    return node;
  },
  update: (el, m) => {
    updateMessage(el, m);
    if (m.role === "assistant") {
      syncCodeReferences(el, m);
      syncRefusal(el, m);
    }
  },
  onRemove: (_el, key) => {
    disposeMessage(key);
  },
};

/** Drop every per-message resource for `key`. Called from the body reconcile's
 *  onRemove, and from the turn reconcile's onRemove for each of a discarded
 *  card's rows — a removed card's inner list never reconciles again, so its
 *  own onRemove would never fire. */
function disposeMessage(key: string): void {
  const arr = bindUnbinds.get(key);
  if (arr !== undefined) {
    for (const fn of arr) {
      fn();
    }
    bindUnbinds.delete(key);
  }
  disposeStreamingEffect(key);
  // Flush any live markdown stream, then drop the block render state
  // (cleanup only — the message row is being removed).
  finalizeAssistantBody(key);
  disposeAssistantBody(key);
  messageStates.delete(key);
}

// ---------------------------------------------------------------------------
// Per-role builders + updaters
// ---------------------------------------------------------------------------

/** Build one message of a turn's BODY. A user message never reaches here:
 *  projectTurns promotes it to its turn's header, so the body holds only what
 *  the trigger caused. An unexpected role still renders as a plain system row
 *  rather than vanishing from the transcript. */
function buildMessage(m: Message): HTMLElement {
  switch (m.role) {
    case "assistant":
      return buildAssistant(m);
    case "event":
      return buildEvent(m) ?? buildSystemFallback(m);
    case "user":
      return buildSystemFallback(m);
  }
}

function updateMessage(el: HTMLElement, m: Message): void {
  if (m.role === "assistant") {
    updateAssistant(el, m);
  } else if (m.role === "event") {
    updateEvent(el, m);
  }
  // user messages are immutable once mounted.
}

// --- The turn card ---

/** Build one turn: tinted header (the trigger), plain body (the work), tinted
 *  footer (the outcome ledger). One card type for every turn — a one-word
 *  answer and a forty-tool-call refactor are the same object, differing only
 *  in how much body they have. Density comes from type scale, not from
 *  structural variation. */
function buildTurn(t: Turn): HTMLElement {
  const card = el("div", { className: "turn" });
  card.dataset["outcome"] = t.outcome;
  // The permalink target. `#turn-{n}` addresses a turn from a ledger row, a
  // search hit or the rail.
  card.id = turnAnchorID(t.n);

  const header = buildTurnHeader(headerData(t));
  mountRewind(header, t);
  card.appendChild(header);

  const body = el("div", { className: "turn-body" });
  card.appendChild(body);
  reconcile(body, t.body, messageSpec);

  mountTurnFooter(card, t);

  // A new user turn pops the reader back to the bottom. scrollToBottom() does
  // an explicit RAF-paced scroll that lands on the new card immediately
  // (suppressScroll would have blocked the auto-scroll for the very turn that
  // just arrived).
  if (t.trigger !== undefined) {
    scrollToBottom();
  }
  return card;
}

function updateTurn(card: HTMLElement, t: Turn): void {
  card.dataset["outcome"] = t.outcome;
  const header = card.querySelector<HTMLElement>(":scope > .turn-header");
  if (header !== null) {
    updateTurnHeader(header, headerData(t));
    mountRewind(header, t);
  }
  const body = card.querySelector<HTMLElement>(":scope > .turn-body");
  if (body !== null) {
    reconcile(body, t.body, messageSpec);
  }
  mountTurnFooter(card, t);
}

function headerData(t: Turn): TurnHeaderData {
  const request = t.trigger?.content;
  return {
    n: t.n,
    outcome: t.outcome,
    ts: t.ts,
    // An empty prompt is not a request; fall through to the system-trigger
    // rendering rather than showing a blank header band.
    request: request !== undefined && request.trim() !== "" ? request : undefined,
  };
}

/** Mount the Rewind action into the header's action slot, once.
 *
 *  Offered on every turn with a real trigger: rewinding is independent of
 *  whether the agent wrote a file, so a read-only Q&A turn can still be
 *  rewound from. A turn the user did not ask for has no user message to
 *  address, so it gets no button. */
function mountRewind(header: HTMLElement, t: Turn): void {
  const slot = header.querySelector<HTMLElement>(":scope > .turn-head-row > .turn-head-actions");
  if (slot === null) {
    return;
  }
  const trigger = t.trigger;
  if (trigger === undefined) {
    slot.replaceChildren();
    return;
  }
  if (slot.firstElementChild !== null) {
    return; // already mounted; the handler closes over a stable message id
  }
  const btn = el(
    "button",
    {
      className: "turn-rewind",
      type: "button",
      title: "Rewind conversation from this point",
      "aria-label": "Rewind from this turn",
    },
    "Rewind",
  );
  btn.addEventListener("click", () => {
    void handleRewindClick(trigger).catch((e: unknown) => {
      console.warn("[messages] rewind failed", e);
    });
  });
  slot.appendChild(btn);
}

/** Mount / refresh the turn's outcome ledger as the card's last child.
 *
 *  Turn-scoped rather than message-scoped: a turn can hold more than one
 *  assistant message (a mid-turn model switch splits it), and the ledger
 *  describes the TURN, so it sums across them and renders once. */
function mountTurnFooter(card: HTMLElement, t: Turn): void {
  const led = turnLedger(t);
  const data: TurnSummaryData = {
    credits: led.credits,
    elapsedMs: led.elapsedMs,
    changedFiles: led.changedFiles,
    commands: led.commands,
    reads: led.reads,
    outcome: t.outcome,
  };
  const existing = card.querySelector<HTMLDivElement>(":scope > .turn-footer");
  if (!hasTurnSummary(data)) {
    existing?.remove();
    return;
  }
  if (existing === null) {
    card.appendChild(buildTurnFooter(data));
  } else {
    updateTurnFooter(existing, data);
  }
}

// --- Assistant ---

/** Build an assistant turn. The whole body — text bubbles, reasoning,
 *  tool cards/groups, subagent blocks, todo checklists, plan, turn footer —
 *  is composed by the single block dispatcher (messages-blocks.ts) from the
 *  message's canonical `blocks` array. */
function buildAssistant(m: Message): HTMLElement {
  const wrap = el("div", { className: "msg-wrap msg-wrap-assistant" });
  buildAssistantBody(wrap, m, isLikelyLiveStreaming(m));
  return wrap;
}

/** Incremental update: mount newly-arrived blocks + refresh plan/footer.
 *  Per-block and per-tool signals feed streaming deltas straight into the
 *  already-mounted primitives, so this only handles structural growth. */
function updateAssistant(wrap: HTMLElement, m: Message): void {
  const state = messageStates.get(m.id);
  if (state === undefined) {
    return;
  }
  updateAssistantBody(wrap, m, state.streaming);
}

/** Finalize a streamed assistant turn: flush every markdown stream + seal
 *  every reasoning trace (via the block dispatcher), then attach the
 *  copy/export turn-actions row. */
function finalizeTurn(id: string, root: HTMLElement): void {
  finalizeAssistantBody(id);
  const bubble = root.querySelector<HTMLDivElement>(".message.assistant");
  if (bubble !== null) {
    attachTurnActions(bubble);
  }
}

/** Walk the message STATE (messageStates + the session's thinking flag), not
 *  the DOM, to decide which live turns to finalize: a streaming turn finalizes
 *  when either (a) another message arrived after it, or (b) the agent stopped
 *  thinking (turn ended). Driven from the same effect that paints, so it stays
 *  consistent with store state. */
function finalizeStreamingIfNeeded(messages: readonly Message[]): void {
  const lastAssistantIdx = lastAssistantIndex(messages);
  const session = getActive();
  const isThinking = session?.thinking ?? false;
  for (const [id, st] of messageStates) {
    if (!st.streaming) {
      continue;
    }
    const stillLast = id === messages[lastAssistantIdx]?.id;
    if (!stillLast || !isThinking) {
      st.streaming = false;
      finalizeTurn(id, st.el);
      disposeStreamingEffect(id);
    }
  }
}

function lastAssistantIndex(messages: readonly Message[]): number {
  for (let i = messages.length - 1; i >= 0; i--) {
    if (messages[i]?.role === "assistant") {
      return i;
    }
  }
  return -1;
}

/** Heuristic: an assistant message is "live streaming" when its parent
 *  session is currently thinking AND this is the last assistant in the
 *  array. Replay path skips this. */
function isLikelyLiveStreaming(m: Message): boolean {
  if (m.role !== "assistant") {
    return false;
  }
  const session = getActive();
  if (session === undefined) {
    return false;
  }
  if (!session.thinking) {
    return false;
  }
  const idx = lastAssistantIndex(session.messages);
  return idx >= 0 && session.messages[idx]?.id === m.id;
}

// --- Helpers ---

/** An avatar row for a top-level assistant bubble. Only the agent gets one:
 *  the user's side of the exchange is the turn card's header band, which is
 *  identified by its tint and its position rather than by a face. */
function makeRow(): HTMLDivElement {
  const row = el("div", { className: "msg-row" }) as HTMLDivElement;
  const avatar = el("div", { className: "msg-avatar" });
  avatar.appendChild(cloneKiroAvatar());
  row.appendChild(avatar);
  return row;
}

async function explainError(errorText: string, toolTitle: string): Promise<string> {
  const d = await explainErrorAction.dispatch({ errorText, context: toolTitle });
  return d?.output ?? "";
}
