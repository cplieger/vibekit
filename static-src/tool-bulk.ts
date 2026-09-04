// ---------------------------------------------------------------------------
// The rest of a tool call, fetched when a reader asks for it.
//
// A transcript response carries each tool call's claim line plus a windowed
// PREVIEW of its input, output and diffs — one measured chat's 465 calls hold
// 12.17 MB between them, and a single message reached 9.1 MB — so a card built
// from a page load or a scroll-up sets `has_full` and the bulk is one request
// away at `GET /api/chats/{id}/tools/{toolCallID}`.
//
// Its own module rather than a function in `tool-card.ts`, for two reasons. The
// card builder has no network dependency today and every test that mounts a card
// would inherit `api-client` behind it. And the memoisation belongs to the CALL,
// not to a card: the same tool call can be on screen twice (a transcript card and
// a subagent group's copy), and both must read one answer rather than race two
// requests for a megabyte each.
// ---------------------------------------------------------------------------

import { apiGetTyped } from "./api-client.js";
import { decodeToolCallBulk } from "./wire/decoders.gen.js";
import type { ToolCallBulk } from "./wire/types.gen.js";

/** In-flight and settled fetches, keyed by chat and call.
 *
 *  The PROMISE is cached rather than the value, which is what collapses two
 *  cards asking at once into one request. A failed fetch resolves to `null` and
 *  is dropped from the cache, so a reader who closes and re-opens the card
 *  retries rather than being told "unavailable" forever by a cached failure. */
const pending = new Map<string, Promise<ToolCallBulk | null>>();

function key(chatID: string, toolCallID: string): string {
  return `${chatID}\u0000${toolCallID}`;
}

/** Fetch one tool call's whole input, output and diffs.
 *
 *  `null` when the chat or the call is unknown to the server, or the request
 *  failed — a caller renders what it has rather than an error, because the
 *  preview it already holds is the honest fallback. */
export function toolCallBulk(chatID: string, toolCallID: string): Promise<ToolCallBulk | null> {
  if (chatID === "" || toolCallID === "") {
    return Promise.resolve(null);
  }
  const k = key(chatID, toolCallID);
  const held = pending.get(k);
  if (held !== undefined) {
    return held;
  }
  const p = apiGetTyped(
    `/api/chats/${encodeURIComponent(chatID)}/tools/${encodeURIComponent(toolCallID)}`,
    decodeToolCallBulk,
  ).then((d) => {
    if (d === null) {
      pending.delete(k);
    }
    return d;
  });
  pending.set(k, p);
  return p;
}

/** Drop every cached bulk. Called when a chat's transcript is reloaded from
 *  scratch, because a tool call's content can still grow — a terminal's full
 *  stream replaces the ACP fragments at completion — and a cached answer from
 *  before that would be a stale one held forever. */
export function forgetToolCallBulk(): void {
  pending.clear();
}
