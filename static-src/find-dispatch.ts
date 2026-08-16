// ---------------------------------------------------------------------------
// Ctrl-F belongs to the ACTIVE TAB.
//
// The chord had two meanings already: open find-in-chat, and on a second press
// hand the key back to the browser's native find. It now has two DESTINATIONS
// and the same two meanings in each: over a files or editor tab it finds in
// files, everywhere else it finds in the conversation. There is no third global
// meaning, which is why this is one listener with one branch rather than a
// second capture-phase keydown for the same chord.
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
// for overriding the key at all.
//
// A module rather than an inline listener in app.ts so the routing itself is
// testable; app.ts still owns the registration, and nothing but app.ts and the
// test imports this.
// ---------------------------------------------------------------------------

import { getActiveTabKind } from "./tabs.js";
import { handleFindHotkey } from "./find-in-chat.js";
import { handleFindInFilesHotkey } from "./files-search.js";

/** Route Ctrl-F / Cmd-F to the find that belongs to the active tab. Registered
 *  on document in the capture phase, so the browser's native find can be
 *  pre-empted before it opens. */
export function handleFindKey(e: KeyboardEvent): void {
  const kind = getActiveTabKind();
  if (kind === "files" || kind === "editor") {
    handleFindInFilesHotkey(e);
    return;
  }
  handleFindHotkey(e);
}
