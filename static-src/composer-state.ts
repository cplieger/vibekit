// ---------------------------------------------------------------------------
// Per-chat composer state: the text typed and not sent, plus the attachments
// staged beside it.
//
// The composer is ONE textarea and ONE pill row shared by every chat, so its
// contents have to be saved and restored as the active chat changes. That is the
// discipline the editor already runs against its own shared textarea:
// `activateFile` calls `saveCurrentState()` before it retargets, then
// `restoreUI(state)` — and the ordering is not stylistic, because the outgoing
// key is unrecoverable once the store has moved on. `activateChatView` had the
// restore half of that pair for nothing (`clearAttachments()`, which discarded
// rather than parked) and no save half at all, so typed text bled from one
// conversation into the next and staged attachments were thrown away by a glance
// at another tab.
//
// The draft is persisted SERVER-SIDE, on the chat record, not in localStorage:
// it then follows the user across devices and joins the state that is already
// per-chat and canonical (model, mode, supervised, effort). Saving is
// user-transparent — a 600ms debounce, no spinner, no toast — reusing the shape
// the steering textarea already uses (`debouncedDispatch` plus `isPending()` /
// `flush()` on teardown). Retention rides `chat_retention_days` with no second
// TTL, because a draft is a field in the one chat file the purge deletes; the
// server's SetDraft is what keeps that honest by not stamping UpdatedAt.
//
// This module holds the LOCAL working copy. It is authoritative for the UI, and
// the server's value only ever SEEDS it (see seedComposerDraft), for two
// reasons: a chat switch must restore instantly rather than wait for a fetch,
// and a draft the user is still typing outranks the copy they last flushed.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { get } from "./store.js";
import { setDraft } from "./actions/chat.js";
import { debouncedDispatch, registerCleanup, type DebouncedDispatch } from "./actions/index.js";
import { stashAttachments, restoreAttachments, dropAttachments } from "./attachments.js";

/** Quiet window before an edit is persisted. Same 600ms as the steering
 *  textarea, so the app has one autosave cadence rather than two. */
const DRAFT_SAVE_WAIT = 600;

/** chat id → composer text. A chat with no entry has never been typed into on
 *  this device and takes the server's draft on first load. */
const drafts = new Map<string, string>();

/** The chat the live textarea currently belongs to. "" between a save and the
 *  restore that follows it, which is why noteComposerText no-ops on "": there is
 *  no chat for the keystroke to belong to. */
let liveChatID = "";

let debouncedSave: DebouncedDispatch<{ chatID: string; text: string }> | null = null;

/** Wire the autosave, the blur flush and the unload flush.
 *
 *  Called from app.ts rather than from initPromptInput, and this module wires its
 *  own listeners rather than being called by that one, for an import reason:
 *  send-state.ts imports prompt-input.ts to push the send button's state, and
 *  transport.ts imports send-state.ts — so prompt-input statically importing
 *  anything that reaches the transport (which the draft action does) would close
 *  a cycle. The two modules are peers under the composition root instead:
 *  prompt-input owns the composer's BEHAVIOUR, this owns its per-chat STATE, and
 *  they meet on the element. prompt-input announces its programmatic writes with
 *  an `input` event (setComposerValue) so this layer sees them. */
export function initComposerState(): void {
  if (debouncedSave !== null) {
    return;
  }
  debouncedSave = debouncedDispatch(setDraft, { wait: DRAFT_SAVE_WAIT });
  const el = $.promptInput;
  const onInput = (): void => {
    noteComposerText(el.value);
  };
  const onBlur = (): void => {
    flushComposerDraft();
  };
  // pagehide rather than beforeunload: beforeunload does not fire when iOS
  // discards a backgrounded tab, which is the case this app meets most often.
  // Best-effort by nature — an unload can outrun the POST — which is why the
  // debounce is short enough that most drafts are already saved by the time it
  // fires.
  const onPageHide = (): void => {
    flushComposerDraft();
  };
  el.addEventListener("input", onInput);
  el.addEventListener("blur", onBlur);
  window.addEventListener("pagehide", onPageHide);
  registerCleanup(() => {
    el.removeEventListener("input", onInput);
    el.removeEventListener("blur", onBlur);
    window.removeEventListener("pagehide", onPageHide);
    flushComposerDraft();
    debouncedSave?.cancel();
  });
}

/** Record the composer's current text for the active chat and schedule its save.
 *
 *  Driven by the `input` listener above, which covers typing AND the two
 *  programmatic writes prompt-input announces (the clear on submit and the
 *  failed-send restore). Exported because the tests drive it directly and because
 *  it names the one write path into the draft map. */
export function noteComposerText(text: string): void {
  if (liveChatID === "") {
    return;
  }
  if (drafts.get(liveChatID) === text) {
    return;
  }
  drafts.set(liveChatID, text);
  debouncedSave?.({ chatID: liveChatID, text });
}

/** The live chat's draft, read from the MAP and never from the element.
 *
 *  The textarea is not always showing the draft: ArrowUp displays a previously
 *  submitted prompt through a write that deliberately emits no `input` event
 *  (prompt-input's setInputValue), so the box holds history while the map still
 *  holds what the user typed. Reading `$.promptInput.value` here sent that
 *  history to the server AND replaced the map entry with it, which lost the draft
 *  in both places at once — the textarea is a display surface, and every write
 *  that means "this is the draft now" arrives through `input`. */
function liveDraft(): string {
  return drafts.get(liveChatID) ?? "";
}

/** Flush a pending draft save now. Blur, chat switch, tab close and unload.
 *
 *  Only when something is pending: with nothing pending the last debounce has
 *  already sent the current text, and a flush would be one more POST carrying a
 *  value the server holds. Pending also means noteComposerText ran, so the map
 *  has an entry and liveDraft() is the text that scheduled the save. */
export function flushComposerDraft(): void {
  if (liveChatID === "" || debouncedSave?.isPending() !== true) {
    return;
  }
  void debouncedSave.flush({ chatID: liveChatID, text: liveDraft() });
}

/** Park the composer's contents under the chat they belong to.
 *
 *  MUST run before the store's active chat changes: after that the outgoing id
 *  is gone and the text has nowhere to go. The parking itself is already done —
 *  every write that means "this is the draft" lands in the map through the
 *  `input` listener — so this only has to get the pending save out under the
 *  OUTGOING id and hand the pill row over. */
export function saveComposerState(): void {
  flushComposerDraft();
  stashAttachments();
  liveChatID = "";
}

/** Put `chatID`'s composer contents on screen.
 *
 *  A chat with no draft gets an EMPTY box rather than being left alone: leaving
 *  it alone is precisely the bleed this exists to stop. */
export function restoreComposerState(chatID: string): void {
  liveChatID = chatID;
  $.promptInput.value = drafts.get(chatID) ?? "";
  restoreAttachments(chatID);
}

/** Adopt the server's stored draft for `chatID`, once its record has loaded.
 *
 *  The seed loses to anything local, in both directions. A chat this device has
 *  already typed into keeps its own copy, because the fetch can land after the
 *  user has started the next message; and the box is only written when it is
 *  EMPTY, which is the same rule the failed-send restore follows for the same
 *  reason. What this buys is the reload case: the map starts empty, so the
 *  server's draft is what comes back. */
export function seedComposerDraft(chatID: string): void {
  if (drafts.has(chatID)) {
    return;
  }
  const text = get(chatID)?.draft ?? "";
  drafts.set(chatID, text);
  if (text === "" || chatID !== liveChatID) {
    return;
  }
  if ($.promptInput.value === "") {
    $.promptInput.value = text;
  }
}

/** Forget a closed or deleted chat's composer state.
 *
 *  Local only — a close that keeps the record keeps its draft server-side, and
 *  reopening the chat seeds it back. */
export function dropComposerState(chatID: string): void {
  drafts.delete(chatID);
  dropAttachments(chatID);
  if (chatID === liveChatID) {
    liveChatID = "";
  }
}

/** Test seam: reset the module between cases. */
export function _resetComposerStateForTest(): void {
  debouncedSave?.cancel();
  debouncedSave = null;
  drafts.clear();
  liveChatID = "";
}
