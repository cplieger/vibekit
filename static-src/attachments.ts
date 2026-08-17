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
// ---------------------------------------------------------------------------

import { createCollection, bindList, effect } from "@cplieger/reactive";
import { $ } from "./dom.js";
import { buildAttachmentPill } from "./attachment-pill.js";

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
  stash.set(chatID, [...held, toAttached(path)]);
}

/** Remove an attachment by path. */
function removeAttachment(path: string): void {
  attached.remove(path);
}

/** Take all attachments (clears the list). Returns the array for the
 *  prompt payload. */
export function takeAttachments(): AttachedFile[] {
  const out = attached.items();
  attached.clear();
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
  liveChatID = "";
  attached.clear();
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
  generations.set(chatID, attachmentGeneration(chatID) + 1);
  if (chatID === liveChatID) {
    liveChatID = "";
    attached.clear();
  }
}

// buildAttachmentPill lives in attachment-pill.ts. A sent turn's header draws
// the same pill, and that consumer is a pure `fundamentals/` view which must not
// reach a module that touches `$` — see the header comment there.
