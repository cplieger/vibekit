// ---------------------------------------------------------------------------
// Settling a deep-linked chat id the store holds NO row for.
//
// This is the whole of what the router used to do inline, and it moved here for
// two reasons that are the same reason twice: `applyRoute` is a private function
// in the composition root, so nothing it holds has a test address, and what it
// held was not routing. Three of the four rules below are about EVIDENCE — what
// licenses a terminal claim, what licenses silence, and whether a verdict that
// arrived a round trip late still describes the screen — and every one of them
// had shipped unpinned.
//
// The standing principle they implement: never derive a terminal verdict from
// data this client may simply not have yet, and never let a failure be silent.
// A pasted link that resolves to nothing has to say something, and what it says
// must be what the server actually established.
// ---------------------------------------------------------------------------

import { resolveUnknownChat } from "./chat.js";
import { chatListLoaded, serverMayAnswer } from "./store-load.js";
import { parseRoute, replaceRoute } from "./router.js";
import { getActiveTabRoute } from "./tabs.js";
import { error as toastError } from "./toast.js";

/** What settling a deep-linked id did, for a test to read.
 *
 *  A return value rather than an inspectable side effect, because three of the
 *  four outcomes are indistinguishable from the outside: `held` and `stale` both
 *  leave the URL alone and raise nothing, and only the reason differs. */
export type DeepLinkOutcome =
  /** The chat exists and its tab is open. */
  | "opened"
  /** The SERVER said there is no such chat: the URL was canonicalized and the
   *  reader told so. The one terminal outcome. */
  | "gone"
  /** Nobody answered. The URL still names the id and the reader has a retry. */
  | "unresolved"
  /** Nobody answered and the reader has ALREADY been told the server is
   *  unreachable, so nothing was raised. The URL is held either way. */
  | "held"
  /** An answer arrived for a location the reader has since left. Dropped. */
  | "stale";

/** Whether the URL still names the chat we asked about.
 *
 *  The answer arrives a round trip after the route was applied, and in that window
 *  a tab click or a back press moves the location. Replacing a NEWER location with
 *  a verdict about an older one is the same stale-answer defect the verdict itself
 *  exists to remove, one layer up — so the id is captured before the await and
 *  compared against the location as it is NOW, never re-read off the route object.
 *
 *  It is also what makes the unresolved notice bearable: a notice about a link the
 *  reader abandoned is noise, and this is what stops one. */
function stillNames(id: string): boolean {
  const now = parseRoute(location.pathname);
  return now.kind === "chat" && now.id === id;
}

/** Canonicalize a URL that names no record: point it at what is on screen.
 *
 *  `getActiveTabRoute()` rather than a literal, so the URL ends up naming the view
 *  the reader is actually looking at; the empty chat route is the fallback for a
 *  strip with nothing active, which is the same canonicalization boot performs
 *  for "/". */
function canonicalize(): void {
  replaceRoute(getActiveTabRoute() ?? { kind: "chat", id: "" });
}

/** Settle a deep-linked chat id the store has no row for, by ASKING the server.
 *
 *  Four outcomes, and each one is a different piece of evidence:
 *
 *   - the chat EXISTS, so the link works and its tab opens;
 *   - the SERVER says it is gone, which is the only thing that licenses saying so;
 *   - nobody answered, so the URL is held and the reader gets a NON-terminal
 *     notice with a retry that re-asks;
 *   - nobody answered and the reader has already been told the server is
 *     unreachable, so nothing is raised.
 *
 *  THE ASK GATE IS EVIDENCE THE SERVER CANNOT ANSWER, not the absence of a chat
 *  list — see `serverMayAnswer`. Its false arm is round 3's fix and is preserved
 *  exactly: a reload of any `/chat/<id>` against a restarting server holds the URL
 *  and stays quiet, because boot has already raised "Couldn't load your chats."
 *  with a Reload and a second notice would be the same failure reported twice.
 *
 *  THE NOTICE GATE IS `chatListLoaded()`, and it is a different question from the
 *  ask gate rather than a duplicate of it. Boot toasts whenever its list load
 *  failed, and a load that failed did not latch — so an unlatched list means the
 *  reader is already holding a notice about this server, and an `unresolved` here
 *  would be the second one. A list that HAS landed means nothing has told them
 *  anything, and then this notice is the only signal a pasted link produces at all.
 *
 *  Never rejects: every path returns an outcome. The router voids the promise, and
 *  the retry re-enters through the same door. */
export async function settleDeepLinkedChat(id: string): Promise<DeepLinkOutcome> {
  if (!serverMayAnswer()) {
    return "held";
  }
  const verdict = await resolveUnknownChat(id);
  if (verdict === "opened") {
    // `resolveUnknownChat` has already opened the tab, and a refusal there raised
    // its own notice through the action framework — so saying anything here would
    // be reporting one refusal twice.
    return "opened";
  }
  if (!stillNames(id)) {
    return "stale";
  }
  if (verdict === "gone") {
    canonicalize();
    toastError("That conversation no longer exists.");
    return "gone";
  }
  if (!chatListLoaded()) {
    return "held";
  }
  // The URL is deliberately NOT canonicalized: holding the id is what makes the
  // retry — and a reload, and a re-share of the same link — address the chat the
  // reader asked for rather than the fallback they landed on.
  //
  // The wording claims nothing about the conversation, which is the whole point:
  // the server failed to answer, so "no longer exists" would be exactly the
  // terminal claim this path refuses to make. Retry re-asks the server rather
  // than reloading the page, because a reload throws away a live SSE connection,
  // every open tab's state and the transcript underneath, to repeat one GET.
  toastError("Couldn't open that conversation — the server didn't answer.", {
    label: "Retry",
    onClick: () => {
      void settleDeepLinkedChat(id);
    },
  });
  return "unresolved";
}
