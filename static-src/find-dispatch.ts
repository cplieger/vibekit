// ---------------------------------------------------------------------------
// "Find" belongs to the ACTIVE TAB — and so does the toolbar button that opens
// it.
//
// The chord had two meanings already: open a find, and on a second press hand
// the key back to the browser's native find. It now has FIVE destinations and
// the same two meanings in each: a files or editor tab finds in the buffer or
// the tree, the configuration browser focuses its metadata filter, History
// focuses its cross-chat box, and everything else finds in the conversation.
// There is no sixth global meaning, which is why this is one listener with one
// switch rather than a second capture-phase keydown for the same chord.
//
// THE BUTTON COMES THROUGH HERE TOO, and that is the fix for a dead door.
// `#find-btn` called openChatFind directly, so on a files or editor tab it ran
// find-in-chat's context guard, found the chat view hidden, and returned —
// a visible control that did nothing on two of the app's views. Routing it here
// makes the button and the chord the same decision, which is also why they
// cannot drift: `toggleFindForActiveTab` and `handleFindKey` read the same
// `getActiveTabKind()`.
//
// Keyed on the tab, not the view. The tab store already knows which tab is
// active and what kind it is (getActiveTabKind), so reading it here is reading
// the answer rather than inferring it from which view element happens to be
// unhidden. It reads the TabSpec's kind rather than the route's, because an
// editor tab's kind is "editor" while its route's is "file" — a binding keyed on
// the route would be a second vocabulary for one question.
//
// Neither escape hatch lives here. Each destination owns its own second-press
// fall-through, so this function never calls preventDefault: whether a press is
// consumed is the receiving find's judgement, and it is the a11y justification
// for overriding the key at all. Two destinations can DECLINE — the editor over
// a diff pane, an image or rendered markdown, where a line number means nothing
// and the browser's own find is the better tool; and the docs page on its
// Workflows tab, which has no filterable inventory. Both then fall through to
// the transcript's handler, which declines in turn because the chat view is
// hidden, so native find opens. A find that could not take you to a match it
// counted would be a control that does nothing.
//
// A module rather than an inline listener in app.ts so the routing itself is
// testable; app.ts still owns the registration, and nothing but app.ts and the
// test imports this.
// ---------------------------------------------------------------------------

import { getActiveTabKind } from "./tabs.js";
import { handleFindHotkey, toggleChatFind } from "./find-in-chat.js";
import { handleFindInFilesHotkey, toggleFilesSearch } from "./files-search.js";
import { handleEditorFindHotkey, toggleEditorFind } from "./editor-find.js";
import { findFocuser } from "./find-registry.js";

/** Route Ctrl-F / Cmd-F to the find that belongs to the active tab. Registered
 *  on document in the capture phase, so the browser's native find can be
 *  pre-empted before it opens. */
export function handleFindKey(e: KeyboardEvent): void {
  switch (getActiveTabKind()) {
    case "editor":
      // The editor's find declines over a non-source surface, and the fall
      // through to find-in-chat then declines too (the chat view is hidden), so
      // native find gets the key.
      if (handleEditorFindHotkey(e)) {
        return;
      }
      handleFindHotkey(e);
      return;
    case "files":
      handleFindInFilesHotkey(e);
      return;
    case "docs":
    case "history":
      // Both are PERMANENT boxes, so the chord focuses rather than reveals. Each
      // may decline (Workflows has no filterable inventory; a page whose box is
      // not built yet has nothing to focus), and the fall through then declines
      // too because the chat view is hidden, so native find gets the key.
      if (focusRegistered(e, getActiveTabKind() ?? "")) {
        return;
      }
      handleFindHotkey(e);
      return;
    default:
      handleFindHotkey(e);
  }
}

/** Toggle the find that belongs to the active tab. What `#find-btn` means.
 *
 *  The two page-level boxes are PERMANENT furniture, so there is nothing to
 *  toggle: the button focuses them instead. */
export function toggleFindForActiveTab(): void {
  switch (getActiveTabKind()) {
    case "editor":
      toggleEditorFind();
      return;
    case "files":
      toggleFilesSearch();
      return;
    case "docs":
    case "history":
      findFocuser(getActiveTabKind() ?? "")?.();
      return;
    default:
      toggleChatFind();
  }
}

/** The shared chord guard. Each destination re-checks it, so the dispatcher's
 *  own pre-checks below use the same test rather than a second spelling. */
function isFindChord(e: KeyboardEvent): boolean {
  return e.key.toLowerCase() === "f" && (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey;
}

/** Focus a registered page box, consuming the chord only when it accepted.
 *
 *  Synchronous by necessity: `preventDefault` cannot be called from a promise
 *  callback, because by then the browser has already opened its own find. The
 *  registry (find-registry.ts) is how a lazily-loaded page becomes reachable
 *  synchronously without this module importing it. */
function focusRegistered(e: KeyboardEvent, kind: string): boolean {
  if (!isFindChord(e)) {
    return false;
  }
  const focused = findFocuser(kind)?.() ?? false;
  if (focused) {
    e.preventDefault();
  }
  return focused;
}
