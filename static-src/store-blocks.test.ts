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
import { setSessions, setActive, get, appendChunk, upsertToolCall } from "./store.js";
import sheet from "./css/13-messages.css?raw";
import type { Block, Session, ToolCall } from "./types.js";

function makeSession(chatID: string): Session {
  return {
    id: chatID,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
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

  it("never retypes a block that already carries content", () => {
    appendChunk(CHAT, MSG, "keep", false, 0, "");
    appendChunk(CHAT, MSG, "-more", false, 0, "");
    const b = blocks();
    expect(b[0]?.type).toBe("text");
    expect(b[0]?.text).toBe("keep-more");
  });
});

describe("a reserved slot costs no row", () => {
  // The other half of the same defect, and it lives in CSS because the block
  // mounter is append-only: the empty bubble IS the position its text fills into
  // later, so it cannot be skipped at mount time. The rule under test is read out
  // of the shipped stylesheet rather than copied here.
  let style: HTMLStyleElement;
  let host: HTMLElement;

  const row = (inner: string): HTMLElement => {
    const el = document.createElement("div");
    el.className = "msg-row";
    el.innerHTML = inner;
    host.appendChild(el);
    return el;
  };

  beforeEach(() => {
    const rule = /\.msg-row:has\([^{]*\{[^}]*\}/.exec(sheet);
    expect(rule, "the empty-bubble rule is missing from css/13-messages.css").not.toBeNull();
    style = document.createElement("style");
    style.textContent = rule?.[0] ?? "";
    document.head.appendChild(style);
    host = document.createElement("div");
    document.body.appendChild(host);
  });

  it("hides a row whose settled bubble is empty", () => {
    const el = row('<div class="message assistant"></div>');
    expect(getComputedStyle(el).display).toBe("none");
  });

  it("shows the row the moment the bubble has content", () => {
    const el = row('<div class="message assistant"><p>hi</p></div>');
    expect(getComputedStyle(el).display).not.toBe("none");
  });

  it("keeps a live bubble visible so it can carry its caret", () => {
    const el = row('<div class="message assistant streaming"></div>');
    expect(getComputedStyle(el).display).not.toBe("none");
  });
});
