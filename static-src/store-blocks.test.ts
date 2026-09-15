// The chronological block array, and the pads that keep it aligned.
//
// `block_index` is the SERVER's position in one array that the client fills from
// TWO event streams (text/thinking over `message_chunk`, tool calls over
// `tool_call`), so a frame can name an index past the end of what has arrived and
// the gap has to be reserved. Every test here is a way that reservation went
// wrong and corrupted the rest of the turn; measured live before the fix, one
// turn held 128 server-side tool_use blocks as 2 tool groups plus 122
// zero-height rows.
//
// Its own file rather than a block in store.test.ts, matching the split that
// store-load.test.ts and per-chat-store.test.ts already make.
import { describe, it, expect, beforeEach } from "vitest";
import {
  setSessions,
  setActive,
  get,
  appendChunk,
  appendMessage,
  upsertMessage,
  upsertToolCall,
  chunkWatermark,
  clearChunkWatermark,
  noteAdoptedSnapshot,
  snapshotBlockBase,
  isTruncatedSnapshot,
  clearAdoptedSnapshots,
} from "./store.js";
import { projectTurns } from "./turns.js";
import sheet from "./css/13-messages.css?raw";
import type { Block, Message, Session, ToolCall } from "./types.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

const CHAT = "c-blocks";
const MSG = "m-1";

beforeEach(() => {
  setSessions([makeSession(CHAT)]);
  setActive(CHAT);
  // Both side tables outlive `setSessions`, and a case that leaves either standing
  // silently arranges the next one: a stale watermark makes an equal `seq` read as
  // already-folded and the chunk never reaches the block array at all, which is a green
  // (or a red) for a reason the case does not name.
  clearChunkWatermark(CHAT);
  clearAdoptedSnapshots(CHAT);
});

const tc = (id: string): ToolCall =>
  ({ id, title: id, status: "pending", kind: "execute" }) as unknown as ToolCall;

const blocks = (): Block[] => get(CHAT)?.messages.find((m) => m.id === MSG)?.blocks ?? [];

/** A pad: a kind, and nothing behind it. */
const isPad = (b: Block | undefined): boolean =>
  b !== undefined &&
  b.text === undefined &&
  b.thinking === undefined &&
  b.tool_call_id === undefined;

describe("block index mirroring", () => {
  it("pins the FIRST tool call of a message to its reported index", () => {
    // The message-creation branch used to hard-code index 0, so a turn whose
    // first frame to reach the client was a tool_call at index 2 began
    // misaligned by two and stayed wrong for the rest of the turn.
    upsertToolCall(CHAT, MSG, tc("t1"), 2);
    const b = blocks();
    expect(b.length).toBe(3);
    expect(b[2]?.tool_call_id).toBe("t1");
    expect(isPad(b[0])).toBe(true);
    expect(isPad(b[1])).toBe(true);
  });

  it("keeps a later tool call at its index instead of dropping it into a pad", () => {
    appendChunk(CHAT, MSG, "hello", false, 0, "");
    upsertToolCall(CHAT, MSG, tc("t1"), 2);
    const b = blocks();
    expect(b.length).toBe(3);
    expect(b[0]?.text).toBe("hello");
    expect(b[2]?.type).toBe("tool_use");
    expect(b[2]?.tool_call_id).toBe("t1");
  });

  it("does not cascade: a hole does not cost every tool call after it", () => {
    // The regression this pins. The write was guarded by
    // `if (blocks[blockIndex] === undefined)`, which its own padding had just
    // made false, so the tool_use block was dropped AND the array stayed short —
    // so the next tool call padded again and dropped itself the same way.
    appendChunk(CHAT, MSG, "a", false, 0, "");
    for (let i = 1; i <= 6; i++) {
      upsertToolCall(CHAT, MSG, tc(`t${String(i)}`), i);
    }
    const b = blocks();
    expect(b.length).toBe(7);
    for (let i = 1; i <= 6; i++) {
      expect(b[i]?.tool_call_id).toBe(`t${String(i)}`);
    }
    expect(b.filter(isPad).length).toBe(0);
  });

  it("lets the real frame correct a pad's guessed kind", () => {
    // A pad is `text` because that kind mounts a fillable node. When the frame
    // turns out to be reasoning, the kind must follow it: while it did not, the
    // delta landed in `thinking` on a block still typed `text`, and
    // `syncMountedText` read `text` — so the trace rendered as an empty row and
    // the reasoning was dropped outright.
    upsertToolCall(CHAT, MSG, tc("t1"), 2);
    expect(blocks()[1]?.type).toBe("text");
    appendChunk(CHAT, MSG, "why", true, 1, "");
    const b = blocks();
    expect(b[1]?.type).toBe("thinking");
    expect(b[1]?.thinking).toBe("why");
    expect(b[2]?.tool_call_id).toBe("t1");
  });

  it("adopts the subtask id of the frame that fills a pad", () => {
    // Grouping a workflow step's blocks depends on it.
    upsertToolCall(CHAT, MSG, tc("t1"), 2);
    appendChunk(CHAT, MSG, "step text", false, 1, "wf:run:step");
    expect(blocks()[1]?.agent_subtask_id).toBe("wf:run:step");
  });

  it("stamps a pad with the subtask id of the frame that reserved it", () => {
    // A pad stands in for a block the client has not RECEIVED, so it must not read as
    // the parent agent's own work: `turns.ts` `isStepMessage` is a universal over the
    // array, and an untagged reservation among a step's blocks opens a headerless turn
    // card for output that belongs to the turn above it.
    appendChunk(CHAT, MSG, "step text", false, 3, "wf:w1:root");
    const b = blocks();
    expect(b.length).toBe(4);
    expect(b.slice(0, 3).every(isPad)).toBe(true);
    expect(b.slice(0, 3).map((x) => x.agent_subtask_id)).toEqual([
      "wf:w1:root",
      "wf:w1:root",
      "wf:w1:root",
    ]);
  });

  it("clears an inherited subtask id when the real frame carries none", () => {
    appendChunk(CHAT, MSG, "step text", false, 3, "wf:w1:root");
    // The precondition, asserted so the correction below is about an id that was really
    // there rather than one nothing ever stamped.
    expect(blocks()[1]?.agent_subtask_id).toBe("wf:w1:root");

    appendChunk(CHAT, MSG, "the chat's own work", false, 1, "");

    const filled = blocks()[1];
    expect(filled?.text).toBe("the chat's own work");
    // A spread of an ABSENT id writes nothing, so the guess survives unless the repair
    // removes it first — and a block seated on a delegate it does not belong to is the
    // same misplacement as one seated on none.
    expect(filled?.agent_subtask_id).toBeUndefined();
  });

  it("never retypes a block that already carries content", () => {
    appendChunk(CHAT, MSG, "keep", false, 0, "");
    appendChunk(CHAT, MSG, "-more", false, 0, "");
    const b = blocks();
    expect(b[0]?.type).toBe("text");
    expect(b[0]?.text).toBe("keep-more");
  });
});

// ---------------------------------------------------------------------------
// A WINDOWED block array: `block_index` stays the server's ABSOLUTE position while a
// capped snapshot delivers only the TAIL of that array, re-indexed from zero. So the
// store holds the base and subtracts it; without that, a live chunk at absolute 130
// padded a 13-block window out to 131 entries with ~117 untagged placeholders, which
// is what the hidden-row screen in the header comment above was made of.
// ---------------------------------------------------------------------------
const BASE = 117;
/** Blocks in the window, so it covers ABSOLUTE 117..129 inclusive. */
const WINDOW = 13;

const toolCalls = (): readonly ToolCall[] =>
  get(CHAT)?.messages.find((m) => m.id === MSG)?.tool_calls ?? [];

/** The state a capped `live_turn` leaves behind: the record of where the delivered
 *  array sits, then the array itself, in the order `adoptLiveTurn` uses. */
function adoptCappedWindow(): void {
  noteAdoptedSnapshot(CHAT, MSG, { blockBase: BASE, truncated: true });
  upsertMessage(CHAT, {
    id: MSG,
    role: "assistant",
    ts: 1,
    content: "the tail",
    blocks: Array.from({ length: WINDOW }, (_, i): Block => ({ type: "text", text: `b${i}` })),
  } as Message);
}

describe("a windowed block array", () => {
  it("aligns a live chunk against a capped live_turn snapshot with zero pads", () => {
    adoptCappedWindow();
    // ABSOLUTE 130: the first index past the window, so it maps to local 13, `padBlocks`
    // is a no-op and the push appends. 129 could not distinguish a correct base from an
    // off-by-one — it maps to the window's LAST resident block, which the next case pins.
    appendChunk(CHAT, MSG, "more", false, 130, "wf:w1:root", 1);
    const b = blocks();
    expect(b.length).toBe(14);
    expect(b.filter(isPad).length).toBe(0);
    expect(b[13]?.text).toBe("more");
  });

  it("extends the window's last resident block for a chunk naming its absolute index", () => {
    adoptCappedWindow();
    // ABSOLUTE 129 maps to local 12, the last block the snapshot delivered, so the delta
    // joins it and nothing is appended. The boundary an off-by-one breaks in the other
    // direction — and the fixture's block 12 is a `text` block, because the extend arm
    // writes `existing.text` only for a non-reasoning delta.
    appendChunk(CHAT, MSG, "more", false, 129, "wf:w1:root", 1);
    const b = blocks();
    expect(b.length).toBe(13);
    expect(b[12]?.text).toBe("b12more");
  });

  it("drops a chunk addressing a block the snapshot withheld", () => {
    adoptCappedWindow();
    // ABSOLUTE 5 is BELOW the window, so there is no slot: local -112.
    appendChunk(CHAT, MSG, "orphan", false, 5, "", 7);
    const b = blocks();
    expect(b.length).toBe(13);
    // The resident block that shares the withheld index's LOCAL spelling. Without the
    // base this delta lands here, silently appended to a block the server already sent.
    expect(b[5]?.text).toBe("b5");
    // The flat field still grows, and the watermark still rises: the chunk WAS seen, so a
    // redelivery must not fold it a second time. Both are why the conversion sits after
    // the flat-field append rather than at the door.
    expect(get(CHAT)?.messages.find((m) => m.id === MSG)?.content).toBe("the tailorphan");
    expect(chunkWatermark(CHAT, MSG)).toBe(7);
  });

  it("records a tool call whose block the snapshot withheld", () => {
    adoptCappedWindow();
    upsertToolCall(CHAT, MSG, tc("t1"), 5);
    // The call is recorded whatever happens to the block: its card is keyed by call id and
    // its home in `tool_calls` is independent of block position, so an entry-point return
    // would drop a call that has somewhere to live.
    expect(toolCalls().map((c) => c.id)).toEqual(["t1"]);
    expect(blocks().length).toBe(13);
    // THE NEGATIVE-KEY PROBE. An unguarded write at a negative index sets a non-index
    // STRING property while `length` stays put, so a length assertion alone cannot see it.
    expect(Object.hasOwn(blocks(), "-112")).toBe(false);
  });

  it("keeps the window across a re-ingest and drops it on the persist echo", () => {
    adoptCappedWindow();
    // `mergeMessage` adopts a non-empty incoming `blocks` wholesale, so the clear may not
    // live on the shared merge path: `message_created` and `message_updated` route through
    // it carrying PARTIAL messages, and `adoptLiveTurn` upserts in the same tick it
    // records the snapshot.
    upsertMessage(CHAT, { id: MSG, role: "assistant", ts: 2, content: "more tail" });
    expect(snapshotBlockBase(CHAT, MSG)).toBe(BASE);
    expect(isTruncatedSnapshot(CHAT, MSG)).toBe(true);

    // The persist echo carries the whole array, whose indices ARE absolute, so the window
    // is spent — and `appendMessage` is the single door it comes through.
    appendMessage(CHAT, {
      id: MSG,
      role: "assistant",
      ts: 3,
      content: "the whole reply",
      blocks: Array.from({ length: 130 }, (_, i): Block => ({ type: "text", text: `b${i}` })),
    } as Message);
    expect(snapshotBlockBase(CHAT, MSG)).toBe(0);
    expect(isTruncatedSnapshot(CHAT, MSG)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// A frame for a message the store has NEVER SEEN, with a base already on record.
//
// `adoptLiveTurn` records the base and upserts the message in one synchronous pass
// against a session `loadMessages` has already confirmed, so nothing lands between the
// two writes; what this guards is the store's OWN contract for the shape, whichever
// order a caller produces it in — and `MessageChunkPayload.BlockIndex` is documented
// non-monotonic, so a below-window index is ordinary once a base is on record.
//
// Minting a carrier there is what the whole change exists to delete: non-empty `content`
// (or a `tool_calls` entry) with an EMPTY block array defeats `carriesNothing`, defeats
// `isStepMessage`, and so opens a HEADERLESS turn whose body is built from blocks alone —
// the empty "Agent-initiated turn" card. The guard belongs here rather than in the
// projection: `turns.ts` would be guarding the consumer of a value this code path creates.
// ---------------------------------------------------------------------------
describe("an unknown message id with a base on record", () => {
  /** One settled turn, so the next assistant row would OPEN a headerless one. */
  function seedClosedTurn(): void {
    setSessions([
      {
        ...makeSession(CHAT),
        message_count: 2,
        messages: [
          { id: "u1", role: "user", ts: 1, content: "do the thing" },
          {
            id: "a1",
            role: "assistant",
            ts: 2,
            content: "did the thing",
            turn_outcome: "completed",
            blocks: [{ type: "text", text: "did the thing" }],
          },
        ] as Message[],
      },
    ]);
    setActive(CHAT);
  }

  it("mints no carrier for a chunk naming a block the snapshot withheld", () => {
    seedClosedTurn();
    noteAdoptedSnapshot(CHAT, "m-new", { blockBase: BASE, truncated: true });

    appendChunk(CHAT, "m-new", "orphan", false, 5, "", 1);

    expect(get(CHAT)?.messages.map((m) => m.id)).toEqual(["u1", "a1"]);
    // The chunk WAS seen, so the watermark rises even though nothing was written; a
    // redelivery at or below it must not be folded in.
    expect(chunkWatermark(CHAT, "m-new")).toBe(1);
    // The phantom, stated as the reader sees it: one turn, and it has a trigger.
    const turns = projectTurns(get(CHAT)?.messages ?? [], false);
    expect(turns.length).toBe(1);
    expect(turns[0]?.trigger?.id).toBe("u1");
  });

  it("mints no carrier for a tool call naming a block the snapshot withheld", () => {
    seedClosedTurn();
    noteAdoptedSnapshot(CHAT, "m-new", { blockBase: BASE, truncated: true });

    upsertToolCall(CHAT, "m-new", tc("t1"), 5);

    expect(get(CHAT)?.messages.map((m) => m.id)).toEqual(["u1", "a1"]);
    // `tool_calls` alone defeats `carriesNothing`, so a minted carrier opens the same
    // headerless card the chunk arm would.
    expect(projectTurns(get(CHAT)?.messages ?? [], false).length).toBe(1);
  });
});

describe("a reserved slot costs no row", () => {
  // The other half of the same defect. The block mounter is append-only: the
  // empty bubble IS the position its text fills into later, so it cannot be
  // skipped at mount time — the ROW hides instead. The renderer stamps
  // `.is-empty` (the class lifecycle is pinned in messages-blocks.test.ts,
  // driven by the bubble's own blank reports); this half proves the shipped
  // stylesheet actually hides on it. The rule is read out of the stylesheet
  // rather than copied here.
  let style: HTMLStyleElement;
  let host: HTMLElement;

  const row = (cls: string, inner: string): HTMLElement => {
    const el = document.createElement("div");
    el.className = cls;
    el.innerHTML = inner;
    host.appendChild(el);
    return el;
  };

  beforeEach(() => {
    const rule = /\.msg-row\.is-empty\s*\{[^}]*\}/.exec(sheet);
    expect(rule, "the empty-row rule is missing from css/13-messages.css").not.toBeNull();
    style = document.createElement("style");
    style.textContent = rule?.[0] ?? "";
    document.head.appendChild(style);
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  it("hides a row the renderer marked empty", () => {
    const el = row("msg-row is-empty", '<div class="message assistant"></div>');
    expect(getComputedStyle(el).display).toBe("none");
  });

  it("keeps an unmarked row visible, whatever its bubble holds", () => {
    // Content-bearing and caret-carrying rows are one case to the stylesheet:
    // no mark, no hiding. Which rows earn the mark is the renderer's contract.
    const filled = row("msg-row", '<div class="message assistant"><p>hi</p></div>');
    const live = row("msg-row", '<div class="message assistant streaming"></div>');
    expect(getComputedStyle(filled).display).not.toBe("none");
    expect(getComputedStyle(live).display).not.toBe("none");
  });
});
