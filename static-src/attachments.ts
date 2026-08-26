// ---------------------------------------------------------------------------
// Attachment pill row: files attached to the next prompt.
//
// Instead of injecting file paths into the textarea, attachments appear
// as pills in a row below the input: the body opens the file in its own
// viewer tab, the `×` removes it. On submit, the list is
// sent alongside the prompt text. The server classifies each by
// extension into one of three: a supported document type (PDF, DOCX,
// XLSX, DOC, XLS, CSV) is inlined as an ACP embedded `resource` block,
// an image (PNG, JPEG, GIF, WebP) as an `image` block, and anything else
// becomes a path reference the agent reads with its file tools.
//
// The list PERSISTS on the chat record, through `set_attachments` on the same
// 600ms debounce the draft uses. It is the draft's twin — authored content parked
// per chat in a map that parallels `drafts` exactly — and before this it was the
// only half of the pair persisted nowhere, so attaching three files and reloading
// lost them while the half-written sentence describing them came back. Every
// property of the draft's save holds here for the draft's reasons: silent in both
// directions, per-chat scope so dispatches serialize, no retry (the next debounce
// supersedes it), and the LOCAL collection stays authoritative for the UI.
// ---------------------------------------------------------------------------

import { createCollection, bindList, effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { buildAttachmentPill } from "./attachment-pill.js";
import { setAttachments } from "./actions/chat.js";
import { debouncedDispatch, type DebouncedDispatch } from "./actions/index.js";

/** Quiet window before a change is persisted. The draft's 600ms, deliberately
 *  shared: the two halves of one composer must not save on two cadences, or a
 *  reload can show a sentence and the files it does not mention yet. */
const SAVE_WAIT = 600;

/** One attached file. */
export interface AttachedFile {
  path: string; // workspace-relative
  name: string; // filename.ext (display)
}

// Ordered keyed collection of attachments (keyed by workspace path). The
// pill row is rendered via bindList (per-pill reactivity) and a hidden-toggle
// effect — no manual renderPills() calls.
//
// This is the LIVE collection: what the visible pill row shows, which is always
// the active chat's. It is not per-chat itself — `stash` below is. There is one
// #attachment-row element and `bindList` is wired to this collection once behind
// the `bound` latch with no unbind, so swapping the collection per chat would
// mean re-binding a list to an element that already has one. Save-and-restore
// against a shared element is also the shape the editor already uses for its
// per-tab state (saveCurrentState / restoreUI against one textarea).
const attached = createCollection<AttachedFile>((a) => a.path);

/** Attachments belonging to chats that are not the active one.
 *
 *  Before this, one module-level collection served every chat and
 *  `activateChatView` called `clearAttachments()`, so attaching three files and
 *  glancing at another tab discarded them. A chat with no entry has no
 *  attachments, so nothing is seeded and nothing needs cleaning up when a chat
 *  is deleted — an entry is at most a few paths. */
const stash = new Map<string, AttachedFile[]>();

/** The chat the live collection currently belongs to. Written only by
 *  stashAttachments / restoreAttachments, so the two cannot disagree about which
 *  chat the visible pills describe. */
let liveChatID = "";

/** Per-chat attachment generation, bumped by every drop.
 *
 *  A send TAKES its attachments and the row empties, so a failure has to put them
 *  back — and a close in between says the chat's staged files are forgotten. The
 *  two race: the failure lands after the close and recreated the stash entry the
 *  close had just dropped, so reopening the chat brought back files it had been
 *  told to let go. A token captured at take time and checked at restore time is
 *  what separates "this chat's current attachment state" from "the one that was
 *  dropped": monotonic, so a genuinely new state never matches an old token and
 *  there is nothing to reset. Absent from the map = generation 0, which is what a
 *  chat nobody has dropped reads as. */
const generations = new Map<string, number>();

/** Chats this device has staged into or seeded, so the server's copy is adopted
 *  ONCE and never over a local list.
 *
 *  `stash` cannot answer this: `restoreAttachments` deletes a chat's entry when it
 *  moves onto the live row, so an absent entry means either "never touched" or
 *  "this chat is the one on screen". The seed has to tell those apart, for the
 *  reason `drafts.has(chatID)` exists on the draft side — a fetch can land after
 *  the user has already started staging, and the newer local list wins. */
const touched = new Set<string>();

let debouncedSave: DebouncedDispatch<{ chatID: string; paths: string[] }> | null = null;

/** Schedule the persist for `chatID`, or send it now when `now` is set.
 *
 *  Built lazily, for the reason initComposerState builds the draft's saver from
 *  app.ts: constructing it at module load would put a dispatch in the import graph
 *  of every test that so much as reads a pill. */
function persist(chatID: string, paths: string[], now = false): void {
  if (chatID === "") {
    return;
  }
  touched.add(chatID);
  debouncedSave ??= debouncedDispatch(setAttachments, { wait: SAVE_WAIT });
  if (now) {
    void debouncedSave.flush({ chatID, paths });
    return;
  }
  debouncedSave({ chatID, paths });
}

/** Persist the LIVE row under the chat it belongs to. The one-argument case,
 *  because every mutation of the visible row is a mutation of that chat. */
function persistLive(): void {
  persist(
    liveChatID,
    attached.items().map((a) => a.path),
  );
}

/** The chat's current attachment generation, for a caller that will hand it back
 *  to addAttachmentTo after an await. */
export function attachmentGeneration(chatID: string): number {
  return generations.get(chatID) ?? 0;
}

let bound = false;
function ensureBound(): void {
  if (bound) {
    return;
  }
  bound = true;
  const row = $.attachmentRow;
  bindList(row, attached, {
    mount: (att) => buildAttachmentPill(att, { onRemove: removeAttachment }),
  });
  effect(() => {
    row.classList.toggle("hidden", attached.ids.value.length === 0);
  });
}

/** Split a workspace path into the record the pill row renders. */
function toAttached(path: string): AttachedFile {
  const parts = path.split("/");
  return { path, name: parts[parts.length - 1] ?? path };
}

/** Add a file to the ACTIVE chat's attachment list. */
export function addAttachment(path: string): void {
  // Deduplicate by path.
  if (attached.has(path)) {
    return;
  }
  ensureBound();
  attached.upsert(toAttached(path));
  persistLive();
}

/** Add a file to a NAMED chat's attachment list, live if that chat is the one on
 *  screen and into its stash otherwise.
 *
 *  Exists for the failed-send restore. `takeAttachments` empties the row the
 *  moment Send fires, and the send is asynchronous, so the user can switch tabs
 *  before the failure lands — putting the pills back with `addAttachment` would
 *  then hang one chat's files off another chat's prompt.
 *
 *  `generation` is the token the caller read before it sent. A stale one means the
 *  chat's attachment state was DROPPED while the send was in flight (the chat was
 *  closed), and restoring into a state that has been forgotten would resurrect
 *  files on the next open. Omit it for a first-hand add, which cannot be stale. */
export function addAttachmentTo(chatID: string, path: string, generation?: number): void {
  if (generation !== undefined && generation !== attachmentGeneration(chatID)) {
    return;
  }
  if (chatID === "" || chatID === liveChatID) {
    addAttachment(path);
    return;
  }
  const held = stash.get(chatID) ?? [];
  if (held.some((a) => a.path === path)) {
    return;
  }
  const next = [...held, toAttached(path)];
  stash.set(chatID, next);
  persist(
    chatID,
    next.map((a) => a.path),
  );
}

/** Remove an attachment by path. */
function removeAttachment(path: string): void {
  attached.remove(path);
  persistLive();
}

/** Take all attachments (clears the list). Returns the array for the
 *  prompt payload.
 *
 *  The clear is persisted IMMEDIATELY rather than on the debounce, and it is
 *  persisted at all even though `prompt` clears the record server-side. Two
 *  reasons: a STEER also takes the row and is not the prompt path, so the server
 *  clears nothing for it; and a debounced clear would still be in the air when the
 *  send's own response lands, so the two writes would race for a value they agree
 *  on. Sending it now makes the outcome the same whichever arrives second. */
export function takeAttachments(): AttachedFile[] {
  const out = attached.items();
  attached.clear();
  if (out.length > 0) {
    persist(liveChatID, [], true);
  }
  return out;
}

// There is no clearAttachments any more. Its one caller was activateChatView,
// where clearing was the bug: switching tabs threw the outgoing chat's staged
// files away instead of parking them. stashAttachments replaced it.

/** Park the live row's attachments under the chat they belong to and empty it.
 *
 *  Called before the store's active chat changes, because the outgoing chat's id
 *  is unrecoverable afterwards — the same ordering constraint the editor's
 *  `saveCurrentState()` has, and the reason it runs before `setActiveFilePath`. */
export function stashAttachments(): void {
  if (liveChatID !== "") {
    const items = attached.items();
    if (items.length > 0) {
      stash.set(liveChatID, items);
    } else {
      stash.delete(liveChatID);
    }
  }
  // Get the pending save out under the OUTGOING id, for the reason
  // flushComposerDraft runs here: after this the id is unrecoverable and the
  // debounce would fire against `liveChatID === ""`, which persists nothing.
  flushAttachments();
  liveChatID = "";
  attached.clear();
}

/** Send a pending attachment save now. Chat switch, tab close and unload.
 *
 *  Only when something is pending: with nothing pending the last debounce has
 *  already sent the current list, and a flush would be one more POST carrying a
 *  value the server holds. */
export function flushAttachments(): void {
  if (liveChatID === "" || debouncedSave?.isPending() !== true) {
    return;
  }
  void debouncedSave.flush({
    chatID: liveChatID,
    paths: attached.items().map((a) => a.path),
  });
}

/** Put `chatID`'s attachments on the live row and make it that chat's. */
export function restoreAttachments(chatID: string): void {
  liveChatID = chatID;
  const held = stash.get(chatID);
  attached.clear();
  if (held === undefined || held.length === 0) {
    return;
  }
  stash.delete(chatID);
  ensureBound();
  for (const a of held) {
    attached.upsert(a);
  }
}

/** Forget a closed or deleted chat's parked attachments.
 *
 *  The generation bump is the other half: a send already in flight took its
 *  attachments before this ran, and its failure path would otherwise recreate the
 *  entry just dropped. */
export function dropAttachments(chatID: string): void {
  stash.delete(chatID);
  touched.delete(chatID);
  generations.set(chatID, attachmentGeneration(chatID) + 1);
  if (chatID === liveChatID) {
    liveChatID = "";
    attached.clear();
  }
}

/** Adopt the server's stored list for `chatID`, once its record has loaded.
 *
 *  The seed loses to anything local, the same rule seedComposerState follows for
 *  the same reason: the fetch can land after the user has started staging files,
 *  and a list still being built outranks the copy last flushed. A chat this device
 *  has already staged into or already seeded keeps its own copy.
 *
 *  What this buys is the reload case: both maps start empty, so the server's list
 *  is what comes back. */
export function seedAttachments(chatID: string, paths: readonly string[]): void {
  if (chatID === "" || touched.has(chatID)) {
    return;
  }
  touched.add(chatID);
  if (paths.length === 0) {
    return;
  }
  const held = paths.map(toAttached);
  if (chatID !== liveChatID) {
    stash.set(chatID, held);
    return;
  }
  // The live row is only written when it is EMPTY, which is the same rule the
  // draft's seed and the failed-send restore both follow: a row the user has
  // already put something in is newer than anything a fetch carries.
  if (attached.ids.peek().length > 0) {
    return;
  }
  ensureBound();
  for (const a of held) {
    attached.upsert(a);
  }
}

/** Adopt a `draft_changed` frame's attachment list for a chat this device is NOT
 *  staging into.
 *
 *  The live row is authoritative and the frame is ignored for it — for the reason
 *  composer-state.ts's header gives about the draft map: the visible row is where
 *  the user's hand is, and adopting a remote list would delete a pill mid-gesture
 *  or restore one just removed. A chat that is not on screen has no such claim, and
 *  converging it is the whole point of the event.
 *
 *  Unlike the seed this does NOT lose to a local copy: the frame is newer by
 *  construction, having been produced by a write the server accepted. */
export function adoptRemoteAttachments(chatID: string, paths: readonly string[]): void {
  if (chatID === "" || chatID === liveChatID) {
    return;
  }
  touched.add(chatID);
  if (paths.length === 0) {
    stash.delete(chatID);
    return;
  }
  stash.set(chatID, paths.map(toAttached));
}

/** Test seam: reset the module between cases. */
export function _resetAttachmentsForTest(): void {
  debouncedSave?.cancel();
  debouncedSave = null;
  stash.clear();
  touched.clear();
  generations.clear();
  liveChatID = "";
  attached.clear();
}

// buildAttachmentPill lives in attachment-pill.ts. A sent turn's header draws
// the same pill, and that consumer is a pure `fundamentals/` view which must not
// reach a module that touches `$` — see the header comment there.
