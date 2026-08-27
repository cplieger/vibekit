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
// server's SetDraft is what keeps that honest by not stamping UpdatedAt — and the
// retention reaper now SKIPS a chat holding a draft for exactly that reason, or
// the field the purge ages from could never see the work.
//
// The ATTACHMENTS are the draft's twin and persist the same way, through
// `set_attachments` on the same cadence; `attachments.ts` owns that half. They
// travel together on one event in the other direction too (`draft_changed`),
// because a receiver cannot know which of the two commands fired.
//
// This module holds the LOCAL working copy. It is authoritative for the UI, and
// the server's value only ever SEEDS it (see seedComposerState), for two
// reasons: a chat switch must restore instantly rather than wait for a fetch,
// and a draft the user is still typing outranks the copy they last flushed. The
// one exception is a chat this device is NOT typing in — see
// adoptRemoteComposerState.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { get } from "./store.js";
import { setDraft } from "./actions/chat.js";
import { debouncedDispatch, registerCleanup, type DebouncedDispatch } from "./actions/index.js";
import {
  stashAttachments,
  restoreAttachments,
  dropAttachments,
  seedAttachments,
  adoptRemoteAttachments,
  flushAttachments,
  _resetAttachmentsForTest,
} from "./attachments.js";

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
    flushAttachments();
  };
  // pagehide rather than beforeunload: beforeunload does not fire when iOS
  // discards a backgrounded tab, which is the case this app meets most often.
  // Best-effort by nature — an unload can outrun the POST — which is why the
  // debounce is short enough that most drafts are already saved by the time it
  // fires.
  const onPageHide = (): void => {
    flushComposerDraft();
    flushAttachments();
  };
  el.addEventListener("input", onInput);
  el.addEventListener("blur", onBlur);
  window.addEventListener("pagehide", onPageHide);
  registerCleanup(() => {
    el.removeEventListener("input", onInput);
    el.removeEventListener("blur", onBlur);
    window.removeEventListener("pagehide", onPageHide);
    flushComposerDraft();
    flushAttachments();
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

/** Point the composer at `chatID`, parking whatever it was holding first.
 *
 *  The save-and-restore pair as ONE call, for the places that move the store's
 *  active chat WITHOUT going through `activateChatView`. There are three, and
 *  each left a window in which `getActiveId()` named the new chat while this
 *  module still named the old one: `createSession` and `openTangentChat` set the
 *  active chat and then AWAIT a tab round trip before the activation runs, and
 *  `removeChat` reassigns it to `remaining[0]` with no activation at all.
 *
 *  A keystroke in that window is filed under the chat the user just left, which
 *  is both halves of one reported bug. The text disappears out of the box when
 *  the activation finally repaints it from the new chat's empty entry, and it
 *  comes back the next time the PREVIOUS chat is opened — after `saveComposerState`
 *  has flushed it to the server as that chat's draft, so it survives a reload
 *  too. Retargeting here rather than at the activation is what makes the box the
 *  new chat's from the moment the store agrees, so the same keystroke is filed
 *  under the chat that will receive it and the later activation repaints the same
 *  value it already holds.
 *
 *  Idempotent, which is what lets it run ahead of an activation that will do the
 *  same thing: a restore reads the map, and the map is what every write lands in. */
export function retargetComposer(chatID: string): void {
  saveComposerState();
  restoreComposerState(chatID);
}

/** Adopt the server's stored composer state for `chatID`, once its record has
 *  loaded: the draft AND the attachments staged beside it, because they are one
 *  composer and a reload that restored the sentence without the files it describes
 *  is worse than restoring neither.
 *
 *  The seed loses to anything local, in both directions. A chat this device has
 *  already typed into keeps its own copy, because the fetch can land after the
 *  user has started the next message; and the box is only written when it is
 *  EMPTY, which is the same rule the failed-send restore follows for the same
 *  reason. What this buys is the reload case: the maps start empty, so the
 *  server's draft is what comes back. */
export function seedComposerState(chatID: string): void {
  seedAttachments(chatID, get(chatID)?.attachments ?? []);
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

/** Put a failed send's text back where the user left it, under the chat that
 *  sent it.
 *
 *  Lives here rather than in prompt-input.ts because the answer to "put it back"
 *  is per-chat, and the per-chat map is here. The version it replaces wrote the
 *  shared box unconditionally, so a prompt that failed after the reader had moved
 *  on landed in whatever conversation was on screen — and the announced `input`
 *  event then recorded it as THAT chat's draft and flushed it to the server. The
 *  attachment half of the same restore has always been chat-scoped
 *  (`addAttachmentTo`), which is what made the text half's omission visible.
 *
 *  Two things lose to a live draft, for the reason the seed loses to one: the send
 *  is asynchronous, so the user may already be typing the next message. A recorded
 *  draft wins outright, and the BOX is only written when it is empty — those are
 *  different tests because the textarea is a display surface (ArrowUp puts history
 *  in it without touching the map), so a history preview must not be overwritten
 *  even though the draft behind it is empty.
 *
 *  Persisted like a keystroke, not just parked: without the save a reload loses
 *  text the user can see sitting in the box. */
export function restoreFailedSend(chatID: string, text: string): void {
  if (chatID === "" || text === "") {
    return;
  }
  if ((drafts.get(chatID) ?? "") !== "") {
    return;
  }
  drafts.set(chatID, text);
  debouncedSave?.({ chatID, text });
  if (chatID === liveChatID && $.promptInput.value === "") {
    $.promptInput.value = text;
  }
}

/** Adopt a `draft_changed` frame for a chat this device is NOT typing in.
 *
 *  The LOCAL map is authoritative for the live chat — see this module's header —
 *  so the frame updates the map for any other chat and is ignored for the one on
 *  screen. Adopting it there would overwrite the box under the caret with a value
 *  that was current 600ms ago on another device, which is the drift the local copy
 *  exists to prevent; the live chat converges on its next activation instead.
 *
 *  Unlike the seed this does NOT lose to a local copy: a frame is newer by
 *  construction, having been produced by a write the server accepted, and the map
 *  entry it replaces is what this device flushed before it stopped typing. */
export function adoptRemoteComposerState(chatID: string, text: string, paths: string[]): void {
  adoptRemoteAttachments(chatID, paths);
  if (chatID === "" || chatID === liveChatID) {
    return;
  }
  drafts.set(chatID, text);
}

/** Forget a closed or deleted chat's composer state.
 *
 *  Local only — a close that keeps the record keeps its draft server-side, and
 *  reopening the chat seeds it back.
 *
 *  Clearing the BOX is part of dropping the live chat's state, and it is not
 *  cosmetic. On the ordinary close the tab store activates a neighbour straight
 *  after this, and `restoreComposerState` overwrites the box anyway; on the close
 *  that empties the strip there is no neighbour and nothing followed, so the text
 *  stayed on screen in a composer that was still live while `removeChat` had
 *  already moved the store's active chat to an unrelated row. Send then posted it
 *  there. Anything typed in that window was parked nowhere either, because
 *  `liveChatID` is "" and `noteComposerText` no-ops on "". */
export function dropComposerState(chatID: string): void {
  drafts.delete(chatID);
  dropAttachments(chatID);
  if (chatID === liveChatID) {
    liveChatID = "";
    $.promptInput.value = "";
  }
}

/** Test seam: reset the module between cases. */
export function _resetComposerStateForTest(): void {
  debouncedSave?.cancel();
  debouncedSave = null;
  drafts.clear();
  liveChatID = "";
  _resetAttachmentsForTest();
}
