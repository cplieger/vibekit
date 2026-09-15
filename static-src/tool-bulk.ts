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
//
// A BULK IS IMMUTABLE, and that is what makes holding one safe rather than a
// staleness risk. The growth a tool call's content does — `adoptTerminalOutput`
// replacing the ACP fragments with the terminal's full stream — happens on the
// terminal status frame, in the live buffer, and the buffer is flushed to the
// chat file at `turn_ended`. `HasFull` has exactly one writer server-side
// (`previewToolCall`, reached only from the `GET /api/chats/{id}` read of
// PERSISTED messages) and the live SSE frames never set it, so a card can only
// ask for a bulk once its content is final. So there is no invalidation here and
// there must not be one: what a cache of immutable megabytes needs is a BOUND.
// ---------------------------------------------------------------------------

import { apiGetTyped } from "./api-client.js";
import { decodeToolCallBulk } from "./wire/decoders.gen.js";
import type { TextSpan, ToolCallBulk, ToolDiff } from "./wire/types.gen.js";

/** What a reveal renders, and all this module holds.
 *
 *  Deliberately NOT the wire `ToolCallBulk`. The server sends `input` alongside
 *  these three and nothing reads it: a card's input block is gated on
 *  `opts.live`, and a `has_full` card is never live, so holding it would keep a
 *  written file's whole content for nobody. Dropping it at the door also leaves
 *  every field measurable in O(1), which the budget below is charged against —
 *  `input` is `unknown`, so costing it would mean walking or stringifying it.
 *
 *  Fields are total rather than optional so a caller reads them without
 *  restating a default per site.
 *
 *  Exported because `tool-card.ts` DECLARES a table of the content pieces a bulk
 *  can fill, and a member's `apply` signature has to name the thing it is handed.
 *  `NonNullable<Awaited<ReturnType<typeof toolCallBulk>>>` at that declaration is
 *  noise. Nothing else about this module is public: the cache is not invalidated
 *  and the byte budget is not configurable. */
export interface ToolBulk {
  readonly output: string;
  readonly outputSpans: TextSpan[];
  readonly diffs: ToolDiff[];
}

/** The ceiling on retained payload, in bytes.
 *
 *  BYTES rather than an entry count, because one entry's cost spans three orders
 *  of magnitude: the measured chat averages ~26 KB per call (12.17 MB over 465)
 *  while a single `execute` can carry the whole of a terminal's stream. A count
 *  cap sized for the average admits tens of megabytes of the tail — the bound
 *  `@cplieger/web-terminal-ui`'s scrollback store already had to correct once.
 *
 *  4 MiB because the DOM is the durable copy: a reveal writes its bulk into the
 *  page and never reads it again, so retention serves only a SECOND reader of the
 *  same call — the sibling copy on a subagent or run subpage, or a card dropped
 *  from the resident block window and re-opened. That is a handful of calls, not
 *  a session's worth, and a miss costs one re-request rather than any content. */
export const MAX_RETAINED_BYTES = 4 * 1024 * 1024;

/** A JS string is UTF-16, so a code unit is two bytes of heap. */
const BYTES_PER_CODE_UNIT = 2;

/** A `TextSpan` is five small integers; ~48 bytes as an object with its header.
 *  Counted because an ANSI-heavy output carries one per styled run, so the spans
 *  of a large output are a real charge even though no single one is. */
const BYTES_PER_SPAN = 48;

interface Entry {
  readonly promise: Promise<ToolBulk | null>;
  /** This entry's charge against the budget, or `undefined` while its request is
   *  in flight — which is also what makes an in-flight entry unevictable, since
   *  collapsing concurrent readers onto one request is this module's whole job.
   *  A settled entry legitimately costs 0 (an empty output with no diffs), so the
   *  distinction is the field's ABSENCE and never a zero. */
  cost: number | undefined;
}

const held = new Map<string, Entry>();
let retained = 0;

function key(chatID: string, toolCallID: string): string {
  return `${chatID}\u0000${toolCallID}`;
}

function costOf(bulk: ToolBulk): number {
  let units = bulk.output.length;
  for (const d of bulk.diffs) {
    units += d.path.length + (d.old_text?.length ?? 0) + d.new_text.length;
  }
  return units * BYTES_PER_CODE_UNIT + bulk.outputSpans.length * BYTES_PER_SPAN;
}

/** Evict settled entries, least recently read first, until the budget holds.
 *
 *  `Map` iterates in insertion order and a hit re-inserts, so insertion order IS
 *  read order. Deleting during that walk is defined behaviour: the iterator goes
 *  on to the entries it has not reached. */
function sweep(): void {
  for (const [k, e] of held) {
    if (retained <= MAX_RETAINED_BYTES) {
      return;
    }
    if (e.cost === undefined) {
      continue;
    }
    held.delete(k);
    retained -= e.cost;
  }
}

function settle(k: string, entry: Entry, d: ToolCallBulk | null): ToolBulk | null {
  const mine = held.get(k) === entry;
  if (d === null) {
    if (mine) {
      held.delete(k);
    }
    return null;
  }
  const bulk: ToolBulk = {
    output: d.output ?? "",
    outputSpans: d.output_spans ?? [],
    diffs: d.diffs ?? [],
  };
  if (!mine) {
    return bulk;
  }
  const cost = costOf(bulk);
  if (cost > MAX_RETAINED_BYTES) {
    // Answered but not held: evicting every other entry for one that still would
    // not fit trades the whole cache for nothing.
    held.delete(k);
    return bulk;
  }
  entry.cost = cost;
  retained += cost;
  sweep();
  return bulk;
}

/** Fetch one tool call's whole output, style spans and diffs.
 *
 *  `null` when the chat or the call is unknown to the server, or the request
 *  failed — a caller renders what it has rather than an error, because the
 *  preview it already holds is the honest fallback. A failure is not retained, so
 *  a reader who closes and re-opens the card retries rather than being told
 *  "unavailable" forever by a held rejection. */
export function toolCallBulk(chatID: string, toolCallID: string): Promise<ToolBulk | null> {
  if (chatID === "" || toolCallID === "") {
    return Promise.resolve(null);
  }
  const k = key(chatID, toolCallID);
  const hit = held.get(k);
  if (hit !== undefined) {
    held.delete(k);
    held.set(k, hit);
    return hit.promise;
  }
  const entry: Entry = {
    cost: undefined,
    promise: apiGetTyped(
      `/api/chats/${encodeURIComponent(chatID)}/tools/${encodeURIComponent(toolCallID)}`,
      decodeToolCallBulk,
    ).then((d) => settle(k, entry, d)),
  };
  held.set(k, entry);
  return entry.promise;
}

/** Drop every held bulk. Test seam: the cache is module state, so a file's cases
 *  would otherwise inherit each other's retention. */
export function _resetToolBulkForTest(): void {
  held.clear();
  retained = 0;
}
