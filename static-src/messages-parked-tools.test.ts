// ---------------------------------------------------------------------------
// The tool layer under the multiplexer: composite tool identity, the parked
// terminal buffer, and the refcounted run clock.
//
// Tool call ids are backend-authored with no cross-chat uniqueness guarantee;
// with parked views RESIDENT, two chats' identical ids share the page, so the
// card registries key on toolCallSigKey(chatID, toolID). This suite pins the
// three behaviors that keying decision exists for.
//
// REAL store, renderer and tool layer; the scroll subsystem is the canonical
// mock (geometry plays no part here) and the network edge is stubbed — the run
// store fetches run state through api-client, and the fake payload is how a
// test makes a run "live".
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Block, Message, Session, ToolCall } from "./types.js";

for (const id of [
  "chat-view",
  "messages-wrap-outer",
  "messages-wrap",
  "messages",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  if (id === "scroll-bottom") {
    d.appendChild(document.createElement("span"));
  }
  document.body.appendChild(d);
}

vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// The network edge: run-store's fetch path. A live run answers with one
// running node so `runIsLive` is true and the card's clock arms.
const apiGetMock = vi.hoisted(() => vi.fn());
vi.mock("./api-client.js", () => ({
  apiGet: apiGetMock,
  apiPost: vi.fn(),
  apiGetTyped: vi.fn(),
}));

const store = await import("./store.js");
const sigs = await import("./store-signals.js");
const messages = await import("./messages.js");
const blocks = await import("./messages-blocks.js");
const { appendTerminalChunk } = await import("./messages-tools.js");

messages.mountChatView();

let seq = 0;
function freshID(prefix: string): string {
  return `${prefix}-${String(++seq)}`;
}

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "",
    usage: { context_size: 0 },
    ...over,
  } as unknown as Session;
}

function user(id: string, content: string): Message {
  return { id, role: "user", ts: 1, content } as Message;
}

function assistant(id: string, blocks_: Block[], toolCalls: ToolCall[] = []): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: blocks_,
    tool_calls: toolCalls,
  } as unknown as Message;
}

function call(id: string, over: Record<string, unknown> = {}): ToolCall {
  return {
    id,
    title: `run ${id}`,
    kind: "execute",
    status: "in_progress",
    ts: 0,
    ...over,
  } as unknown as ToolCall;
}

function seed(...sessions: Session[]): void {
  store.setSessions(sessions);
  const first = sessions[0];
  if (first !== undefined) {
    store.setActive(first.id);
    store.bumpMessages(first.id);
  }
}

function viewOf(chatID: string): HTMLElement {
  const el = messages.transcriptViewFor(chatID);
  if (el === null) {
    throw new Error(`no resident view for ${chatID}`);
  }
  return el;
}

async function flushed(): Promise<void> {
  await Promise.resolve();
}

beforeEach(() => {
  messages.teardownAll();
  store.setSessions([]);
  store.setActive("");
  apiGetMock.mockReset();
  apiGetMock.mockResolvedValue(null);
});

afterEach(() => {
  vi.useRealTimers();
});

// ---------------------------------------------------------------------------
// Composite tool identity.
// ---------------------------------------------------------------------------

describe("composite tool identity", () => {
  it("two chats with IDENTICAL tool ids park and unpark without cross-corruption", async () => {
    // The exact id collision the composite key exists for: both chats' wire
    // frames name the same tool_call.id.
    const a = freshID("c-same");
    const b = freshID("c-same");
    const shared = "toolu_shared";
    const msgA = freshID("m");
    const msgB = freshID("m");
    seed(
      session(a, {
        messages: [
          user(freshID("u"), "hi"),
          assistant(
            msgA,
            [{ type: "tool_use", tool_call_id: shared } as Block],
            [call(shared, { title: "chat A's call" })],
          ),
        ],
        message_count: 2,
      }),
      session(b, {
        messages: [
          user(freshID("u"), "hi"),
          assistant(
            msgB,
            [{ type: "tool_use", tool_call_id: shared } as Block],
            [call(shared, { title: "chat B's call" })],
          ),
        ],
        message_count: 2,
      }),
    );
    await flushed();

    // Park A, mount B — both cards are on the page now, one per view.
    store.setActive(b);
    await flushed();
    const cardA = viewOf(a).querySelector<HTMLElement>(".tool-call");
    const cardB = viewOf(b).querySelector<HTMLElement>(".tool-call");
    expect(cardA).not.toBeNull();
    expect(cardB).not.toBeNull();

    // B's update lands on B's card only: A is parked AND keyed apart.
    store.upsertToolCall(b, msgB, call(shared, { title: "chat B's call", status: "completed" }), 0);
    await flushed();
    expect(cardB?.dataset["outcome"]).toBe("ok");
    expect(cardA?.dataset["outcome"]).not.toBe("ok");

    // A's own completion arrives while A is parked; unparking replays it onto
    // A's card from A's signal — not B's.
    store.upsertToolCall(a, msgA, call(shared, { title: "chat A's call", status: "failed" }), 0);
    await flushed();
    expect(cardA?.dataset["outcome"]).not.toBe("fail");

    store.setActive(a);
    await flushed();
    expect(cardA?.dataset["outcome"]).toBe("fail");
    // B's card kept its own outcome through A's unpark.
    expect(cardB?.dataset["outcome"]).toBe("ok");
    // Both signals exist side by side under their composite keys.
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(a, shared))).toBeDefined();
    expect(sigs.toolCallSigs.get(sigs.toolCallSigKey(b, shared))).toBeDefined();
  });
});

// ---------------------------------------------------------------------------
// The parked terminal buffer.
// ---------------------------------------------------------------------------

describe("the parked terminal buffer", () => {
  it("drains once at resume and drops the oldest past 64 KB", async () => {
    const a = freshID("c-term");
    const b = freshID("c-term");
    const msgID = freshID("m");
    const termID = freshID("term");
    const tc = call("t-term", { terminal_id: termID });
    seed(
      session(a, {
        messages: [
          user(freshID("u"), "run it"),
          assistant(msgID, [{ type: "tool_use", tool_call_id: tc.id } as Block], [tc]),
        ],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "x")], message_count: 1 }),
    );
    await flushed();
    // The link exists (mount claimed the terminal); live output lands in the
    // card while active.
    appendTerminalChunk(termID, "live line\n", [], 0);
    const pre = (): string =>
      viewOf(a).querySelector(".tool-call .tool-output pre")?.textContent ?? "";
    expect(pre()).toBe("live line\n");

    store.setActive(b);
    await flushed();

    // Three 30 KB chunks while parked: 90 KB > the 64 KB cap, so the OLDEST
    // drops (shell scrollback semantics — resume shows the newest output).
    const chunk = (label: string): string =>
      `${label}${"x".repeat(30 * 1024 - label.length - 1)}\n`;
    const oldest = chunk("oldest");
    const middle = chunk("middle");
    const newest = chunk("newest");
    let offset = "live line\n".length;
    for (const c of [oldest, middle, newest]) {
      appendTerminalChunk(termID, c, [], offset);
      offset += c.length;
    }
    // Nothing reached the parked DOM.
    expect(pre()).toBe("live line\n");

    store.setActive(a);
    await flushed();
    // Drained once: the two newest chunks landed, the oldest was dropped.
    const text = pre();
    expect(text.startsWith("live line\n")).toBe(true);
    expect(text).toContain("middle");
    expect(text).toContain("newest");
    expect(text).not.toContain("oldest");

    // A second park/unpark cycle with no new output replays nothing — the
    // buffer was consumed by the drain, not merely read.
    store.setActive(b);
    await flushed();
    store.setActive(a);
    await flushed();
    expect(pre()).toBe(text);
  });
});

// ---------------------------------------------------------------------------
// The refcounted run clock.
// ---------------------------------------------------------------------------

describe("the run clock", () => {
  /** A live run: one running leaf, started in the past, never ended — the
   *  shape `runIsLive` and the card's clock both key on. */
  function liveRunPayload(wf: string): unknown {
    return {
      workflowId: wf,
      state: {
        workflowId: wf,
        status: "running",
        root: {
          nodeId: "n1",
          type: "step",
          status: "running",
          startedAt: new Date(Date.now() - 5000).toISOString(),
        },
      },
    };
  }

  it("survives a park while another surface holds the same run", async () => {
    vi.useFakeTimers();

    const a = freshID("c-clock");
    const b = freshID("c-clock");
    const wf = freshID("wf");
    apiGetMock.mockImplementation(() => Promise.resolve(liveRunPayload(wf)));
    const msgID = freshID("m");
    const launch = call("t-launch", { workflow_id: wf, title: "Run Workflow" });
    seed(
      session(a, {
        messages: [
          user(freshID("u"), "run the workflow"),
          assistant(msgID, [{ type: "tool_use", tool_call_id: launch.id } as Block], [launch]),
        ],
        message_count: 2,
      }),
      session(b, { messages: [user(freshID("u"), "x")], message_count: 1 }),
    );
    await flushed();
    // Let the mocked run fetch resolve into the cell.
    await vi.advanceTimersByTimeAsync(0);

    const transcriptCard = viewOf(a).querySelector<HTMLElement>(".run-card");
    expect(transcriptCard).not.toBeNull();

    // The second surface: a detached render (the subagent page's shape) hosting
    // the SAME workflow's launch — it holds its own clock ref on `wf`.
    const host = document.createElement("div");
    document.body.appendChild(host);
    blocks.buildDetachedBody(
      host,
      assistant(
        msgID,
        [{ type: "tool_use", tool_call_id: launch.id } as Block],
        [launch],
      ) as Message,
      a,
      "subtask-1",
      false,
    );
    await vi.advanceTimersByTimeAsync(0);
    const detachedClock = host.querySelector<HTMLElement>(".run-clock");
    expect(detachedClock).not.toBeNull();

    // Park A: the transcript card releases ITS hold; the detached surface's
    // hold keeps the shared interval alive.
    store.setActive(b);
    await flushed();

    // The run_progress leg of the freeze: the handler's exact call is an
    // invalidate, whose refetch writes the run's cell — the parked card's
    // render effect is suspended, so nothing may reach its DOM.
    const parkedView = viewOf(a);
    const watcher = new MutationObserver(() => undefined);
    watcher.observe(parkedView, {
      childList: true,
      subtree: true,
      characterData: true,
      attributes: true,
    });
    const { invalidateRun } = await import("./run-store.js");
    invalidateRun(wf);
    await vi.advanceTimersByTimeAsync(0);
    expect(watcher.takeRecords()).toHaveLength(0);
    watcher.disconnect();

    const before = detachedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    const after = detachedClock?.textContent ?? "";
    expect(after).not.toBe("");
    expect(after).not.toBe(before);

    // The parked transcript card's clock did NOT advance: its hold released.
    const parkedClock = viewOf(a).querySelector<HTMLElement>(".run-clock");
    const parkedBefore = parkedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    expect(parkedClock?.textContent ?? "").toBe(parkedBefore);

    // Unpark: the transcript card re-arms, re-reads its cell and ticks again.
    store.setActive(a);
    await flushed();
    const resumedBefore = parkedClock?.textContent ?? "";
    await vi.advanceTimersByTimeAsync(2100);
    expect(parkedClock?.textContent ?? "").not.toBe(resumedBefore);

    blocks.disposeDetachedBody(msgID, "subtask-1");
    host.remove();
  });
});
