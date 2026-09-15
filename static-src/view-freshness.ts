// Must this tab's view fetch on activation? The one freshness question, for all nine
// kinds, answered off the digest subjects' version map.
//
// It must never import `store.js` or `tabs.js`: a convenience helper over either
// (`viewStaleForSession(s)`, `refreshActive()`) closes a cycle.
import { forgetSubject, hasSubject } from "./subject-versions.js";
import type { TabKind } from "./types.js";

/** The chat whose transcript a view projects: the chat itself, or the launching chat
 *  behind a subagent page's composite `<chatID>/<subtaskID>` ref (the first slash is the
 *  seam, `tab-materialize.ts`'s codec). Every other kind has no subject and reads "". */
function subjectChat(kind: TabKind, ref: string): string {
  switch (kind) {
    case "chat":
      return ref;
    case "subagent": {
      const cut = ref.indexOf("/");
      return cut > 0 ? ref.slice(0, cut) : "";
    }
    default:
      return "";
  }
}

/** A view whose kind has a digest subject is fresh while the map holds a version for it:
 *  a held version means the projection was applied and the wake digest will name it when
 *  it moves. Every other kind has no subject, so it reads STALE and refetches on every
 *  activation, as it always has — and for `files` that refetch is load-bearing: N
 *  browsers share one view element, so the activation is what re-points it at its own
 *  directory rather than the previous tab's. */
export function viewStale(kind: TabKind, ref: string): boolean {
  const chat = subjectChat(kind, ref);
  return chat === "" || !hasSubject("chat", chat);
}

/** Drop a view's claim: the window behind it is gone (`evictChatMessages`) or the subject
 *  is (`removeChat`). Both of a chat's subjects go, since the live turn's content lived in
 *  the same window. NOT called from `tabs.ts` — a closed tab's subject still exists, and
 *  dropping its record would make reopening it pay a full message-window GET for nothing. */
export function forgetView(kind: TabKind, ref: string): void {
  const chat = subjectChat(kind, ref);
  if (chat === "") {
    return;
  }
  forgetSubject("chat", chat);
  forgetSubject("live_turn", chat);
}
