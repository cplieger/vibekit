// ---------------------------------------------------------------------------
// paint() branches on the DECLARED render cause (design §B2): the store says
// what a version bump was FOR, and the renderer skips exactly the work that
// cause makes unnecessary.
//
//   chunk — a mounted block's signal painted the text; paint is tail
//           bookkeeping only. No projection, no reconcile, no fold pass, no
//           per-turn update.
//   tool  — the owning message's card refreshes through the existing keyed
//           update; sibling turns are never touched.
//   shape — the full pass, and it must run even when nothing about the array's
//           SHAPE says so (gpt R3-H3's case: an in-place same-length
//           message_updated is invisible to any identity- or length-based
//           inference, so the store's declaration is the only honest signal).
//
// The skip assertions are spy deltas on the real seams paint drives —
// projectTurns (projection), reconcile (mount/update), isTurnOpen (fold pass),
// observeTurns (rail) and the messages-blocks entry points (per-turn updates) —
// plus element-identity checks over `#messages`' children, because a reconcile
// that rebuilt a card would mint new nodes even if it produced equal markup.
// Everything below drives the REAL store: sessions land through setSessions /
// appendChunk / upsertToolCall / upsertMessage, and the paint under test is the
// one the transcript effect runs.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi } from "vitest";
import type { Message, Session, ToolCall } from "./types.js";

// The renderer's import graph reaches the shared DOM registry, which throws on
// a missing app root. These ids have to exist before the import is evaluated
// (the composer pair because a send-state effect in the graph tracks the active
// chat and paints the send button on every switch).
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// The scroller is mocked (the shared helper, so the surface stays total); the
// paint path only needs its calls to be inert. Everything else is REAL, spied
// where a skip has to be proven.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
vi.mock("./turns.js", { spy: true });
vi.mock("./reconcile.js", { spy: true });
vi.mock("./fold-state.js", { spy: true });
vi.mock("./turn-rail.js", { spy: true });
vi.mock("./messages-blocks.js", { spy: true });

const store = await import("./store.js");
const turnsMod = await import("./turns.js");
const reconcileMod = await import("./reconcile.js");
const foldMod = await import("./fold-state.js");
const railMod = await import("./turn-rail.js");
const blocksMod = await import("./messages-blocks.js");
const messages = await import("./messages.js");

messages.mountChatView();
const messagesEl = document.getElementById("messages")!;

/** The active view's element — the paint root under the multiplexer. */
function viewRoot(): HTMLElement {
  return messages.activeTranscriptView() ?? (messagesEl as HTMLElement);
}

function user(id: string, content: string): Message {
  return { id, role: "user", ts: 1, content } as Message;
}

function assistant(id: string, text: string): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: text,
    blocks: [{ type: "text", text }],
  } as Message;
}

function assistantWithTool(id: string, tc: ToolCall): Message {
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: [{ type: "tool_use", tool_call_id: tc.id }],
    tool_calls: [tc],
  } as unknown as Message;
}

function session(id: string, msgs: Message[], thinking: boolean): Session {
  return {
    id,
    name: id,
    messages: msgs,
    message_count: msgs.length,
    has_more: false,
    thinking,
    working_label: "",
  } as unknown as Session;
}

/** Call counts on every seam a skipped pass must not touch. */
function seamCounts(): Record<string, number> {
  return {
    projectTurns: vi.mocked(turnsMod.projectTurns).mock.calls.length,
    reconcile: vi.mocked(reconcileMod.reconcile).mock.calls.length,
    isTurnOpen: vi.mocked(foldMod.isTurnOpen).mock.calls.length,
    observeTurns: vi.mocked(railMod.observeTurns).mock.calls.length,
    buildAssistantBody: vi.mocked(blocksMod.buildAssistantBody).mock.calls.length,
    updateAssistantBody: vi.mocked(blocksMod.updateAssistantBody).mock.calls.length,
  };
}

/** Mount `msgs` as a fresh chat and return the painted turn cards. */
function mount(chatID: string, msgs: Message[], thinking: boolean): HTMLElement[] {
  store.setSessions([session(chatID, msgs, thinking)]);
  store.setActive(chatID);
  return [...viewRoot().children] as HTMLElement[];
}

/** One microtask: the per-chat coalescer flushes, and the flush paints. */
async function flushed(): Promise<void> {
  await Promise.resolve();
}

describe("paint branches on the flushed render cause", () => {
  // Message ids are chat-prefixed THROUGHOUT: the reconcile is keyed by message
  // id, so reusing ids across this file's chats would make a chat switch UPDATE
  // a previous test's cards instead of mounting fresh ones.
  it(
    "a chunk flush runs zero projection, reconcile, fold or per-turn work",
    { timeout: 15_000 },
    async () => {
      const chat = "cause-chunk";
      const kids = mount(chat, [user("ck-u1", "hi"), assistant("ck-a1", "hello")], true);
      expect(kids.length).toBe(1);
      const before = seamCounts();

      store.appendChunk(chat, "ck-a1", " world", false, 0, "");
      await flushed();

      expect(store.renderCauseOf(chat).cause).toBe("chunk");
      expect(seamCounts()).toEqual(before);
      const after = [...viewRoot().children];
      expect(after.length).toBe(kids.length);
      expect(after.every((el, i) => el === kids[i])).toBe(true);
      // The skip lost nothing: the mounted block's own signal carries the text.
      // The reveal holds the live edge's tail back while the turn is thinking,
      // so END the turn — the fact flush finalizes and the reveal drains.
      store.setThinking(chat, false);
      await flushed();
      await vi.waitFor(
        () => {
          expect(viewRoot().querySelector(".message.assistant")?.textContent).toContain("world");
        },
        { timeout: 10_000 },
      );
    },
  );

  it("a tool flush refreshes only the owning message's card", async () => {
    const chat = "cause-tool";
    const tc = {
      id: "tc-1",
      title: "Run command",
      kind: "execute",
      status: "in_progress",
    } as unknown as ToolCall;
    const kids = mount(
      chat,
      [
        user("ct-u1", "one"),
        assistantWithTool("ct-a1", tc),
        user("ct-u2", "two"),
        assistant("ct-a2", "done"),
      ],
      false,
    );
    expect(kids.length).toBe(2);
    const before = seamCounts();
    const refreshBefore = vi.mocked(blocksMod.refreshMessageCard).mock.calls.length;

    store.upsertToolCall(chat, "ct-a1", { ...tc, status: "completed" } as ToolCall, 0);
    await flushed();

    expect(store.renderCauseOf(chat)).toEqual({ cause: "tool", msgID: "ct-a1" });
    // The keyed update ran for the one owning message, and it found its render.
    const refreshCalls = vi.mocked(blocksMod.refreshMessageCard).mock;
    expect(refreshCalls.calls.length).toBe(refreshBefore + 1);
    expect(refreshCalls.calls.at(-1)?.[0]).toBe("ct-a1");
    expect(refreshCalls.results.at(-1)?.value).toBe(true);
    // No projection, no reconcile, no fold pass — and zero FULL-PATH body
    // updates: the spy sees only cross-module calls, so the keyed update's own
    // internal path is exactly the one that does not show here.
    expect(seamCounts()).toEqual(before);
    // Sibling turn DOM identity preserved (nothing remounted anywhere).
    const after = [...viewRoot().children];
    expect(after.every((el, i) => el === kids[i])).toBe(true);
  });

  it("an old-turn same-length message_updated classifies shape and repaints that turn", async () => {
    const chat = "cause-shape";
    const kids = mount(
      chat,
      [
        user("cs-u1", "first ask"),
        assistant("cs-a1", "old answer"),
        user("cs-u2", "next"),
        assistant("cs-a2", "ok"),
      ],
      false,
    );
    const oldTurn = kids[0]!;
    expect(oldTurn.querySelector(".turn-req-text")?.textContent).toBe("first ask");
    const before = seamCounts();

    // Same LENGTH, different text: invisible to any identity- or length-based
    // change inference — only the store's declared cause can repaint it.
    store.upsertMessage(chat, user("cs-u1", "FIRST TASK"));
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("shape");
    // The full pass ran…
    expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBe(before["projectTurns"]! + 1);
    // …and repainted that turn IN PLACE with the new text.
    expect(viewRoot().children[0]).toBe(oldTurn);
    expect(oldTurn.querySelector(".turn-req-text")?.textContent).toBe("FIRST TASK");
  });

  it("a chunk flush over 500 mounted turns is bookkeeping-only", { timeout: 15_000 }, async () => {
    const chat = "cause-500";
    const msgs: Message[] = [];
    for (let i = 0; i < 500; i++) {
      msgs.push(
        user(`c5-u${String(i)}`, `ask ${String(i)}`),
        assistant(`c5-a${String(i)}`, `answer ${String(i)}`),
      );
    }
    const kids = mount(chat, msgs, true);
    expect(kids.length).toBe(500);
    const before = seamCounts();

    store.appendChunk(chat, "c5-a499", "!", false, 0, "");
    await flushed();

    // Bookkeeping only: zero projection, zero reconcile, zero fold-pass reads,
    // zero rail re-observation, zero per-turn updates…
    expect(seamCounts()).toEqual(before);
    // …and the 500 cards are the SAME 500 nodes (a rebuild would mint new ones).
    const after = [...viewRoot().children];
    expect(after.length).toBe(500);
    expect(after.every((el, i) => el === kids[i])).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// `.is-bodyless` on the turn card mirrors "the card ends with an empty body"
// for CSS (29-turns.css keys the header's bottom edge on it). buildTurn and
// updateTurn stamp it after every pass, so it must track both facts it
// encodes: the body's children AND whether a footer follows.
// ---------------------------------------------------------------------------

describe("the bodyless turn card is marked .is-bodyless", () => {
  it("marks a prompt-only turn and clears it when the reply lands", async () => {
    const chat = "bodyless-clear";
    const kids = mount(chat, [user("bl-u1", "hi")], true);
    expect(kids.length).toBe(1);
    expect(kids[0]?.classList.contains("is-bodyless")).toBe(true);

    store.upsertMessage(chat, assistant("bl-a1", "hello"));
    await flushed();
    const card = viewRoot().firstElementChild;
    expect(card?.querySelector(":scope > .turn-body")?.childElementCount).toBe(1);
    expect(card?.classList.contains("is-bodyless")).toBe(false);
  });

  it("paints a reserved slot as a marked row, end to end", async () => {
    // The msg-row half of the same CSS contract, through the REAL block
    // callbacks (initBlockCallbacks): an assistant message whose text block is
    // still empty paints a `.msg-row.is-empty` the stylesheet can hide.
    const chat = "bodyless-row";
    mount(chat, [user("br-u1", "go"), assistant("br-a1", "")], false);
    const row = viewRoot().querySelector(".msg-row");
    expect(row).not.toBeNull();
    expect(row?.classList.contains("is-empty")).toBe(true);

    store.appendChunk(chat, "br-a1", "landed", false, 0, "");
    await flushed();
    expect(viewRoot().querySelector(".msg-row")?.classList.contains("is-empty")).toBe(false);
  });

  it("clears it when a FOOTER arrives under a still-empty body", async () => {
    // A second prompt gives the first turn a footer while its body stays empty
    // (a rewind target, and an outcome to state). The body is no longer the
    // card's last child, so the header keeps its border and
    // `.turn-body:empty + .turn-footer` drops the footer's instead — marking
    // here would erase the only line between the two bands.
    //
    // `thinking: true` because a prompt-only turn at the TAIL is a turn whose
    // reply has not landed, and that is the only shape that reaches this state
    // in production. It is load-bearing rather than incidental: `deriveOutcome`
    // reads a settled tail turn with no assistant message as `unknown` (nothing
    // closed it), whose severity earns a footer of its own — so with the flag
    // off, neither prompt-only turn here is bodyless and the case cannot
    // describe the footer half at all. `isLive` is `thinking && last`, so only
    // the tail is live: turn 1 still settles the moment turn 2 arrives.
    const chat = "bodyless-footer";
    const kids = mount(chat, [user("bf-u1", "one")], true);
    expect(kids[0]?.classList.contains("is-bodyless")).toBe(true);

    store.upsertMessage(chat, user("bf-u2", "two"));
    await flushed();
    const cards = [...viewRoot().children] as HTMLElement[];
    expect(cards.length).toBe(2);
    expect(cards[0]?.querySelector(":scope > .turn-footer")).not.toBeNull();
    expect(cards[0]?.querySelector(":scope > .turn-body")?.childElementCount).toBe(0);
    expect(cards[0]?.classList.contains("is-bodyless")).toBe(false);
    // The new prompt-only turn takes the mark instead.
    expect(cards[1]?.classList.contains("is-bodyless")).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// A WORKFLOW STEP's delta needs no structural pass, because the dispatcher DROPS
// its blocks: there is nothing to mount and nothing to re-type. Nothing is mounted
// to carry the text either, so it lands in `appendChunk`'s signal-absent arm — the
// arm that otherwise schedules `shape`, which is a full projection plus reconcile
// per step delta on the very transcript the drop exists to make cheaper.
//
// The pairing is the whole assertion: the same delta with an EMPTY subtask id still
// classifies `shape`, so the exemption is keyed on the id rather than on the signal
// being absent.
// ---------------------------------------------------------------------------

describe("a dropped workflow step's delta is bookkeeping-only", () => {
  it("classifies chunk and runs zero projection, reconcile or per-turn work", async () => {
    const chat = "cause-wfstep";
    const kids = mount(
      chat,
      [user("wf-u1", "run the workflow"), assistant("wf-a1", "started")],
      true,
    );
    expect(kids.length).toBe(1);
    const before = seamCounts();

    // Block index 1: a NEW block of the same message, which is the structural case
    // — `newBlock` is what used to force `shape`.
    store.appendChunk(chat, "wf-a1", "step output", false, 1, "wf:wf_1:wf_1/build");
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("chunk");
    expect(seamCounts()).toEqual(before);
    const after = [...viewRoot().children];
    expect(after.length).toBe(kids.length);
    expect(after.every((el, i) => el === kids[i])).toBe(true);
    // And nothing of it is on screen, which is what makes the skip free.
    expect(viewRoot().textContent).not.toContain("step output");
  });

  it("still classifies shape for the same delta with no subtask id", async () => {
    const chat = "cause-nostep";
    mount(chat, [user("ns-u1", "hi"), assistant("ns-a1", "hello")], true);
    const before = seamCounts();

    store.appendChunk(chat, "ns-a1", "parent prose", false, 1, "");
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("shape");
    expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBe(before["projectTurns"]! + 1);
  });
});

// ---------------------------------------------------------------------------
// The same exemption on a step's TOOL CALL, which is the frame class that
// dominates: a workflow-heavy chat measured 2,250 of 2,367 tool calls as step
// content, each reporting two or three status transitions.
//
// The UPDATE arm is the one that carries the weight. No card is mounted for a step's
// call, so `ensureToolCallSig` is never reached for one and every `tool_call_update`
// lands in the signal-absent arm at the foot of `upsertToolCall` — where a `shape`
// runs a full projection plus reconcile to produce no DOM change at all.
//
// Two controls, and both are the point rather than symmetry. The same call with an
// EMPTY subtask id still classifies `shape`, so the exemption is keyed on the id.
// And a MALFORMED `wf:` id ALSO classifies `shape`, because the dispatcher keys its
// drop on the PARSE and renders that one as a delegate box — one predicate, one set
// of ids, checked from the store's side.
// ---------------------------------------------------------------------------

describe("a dropped workflow step's tool call is bookkeeping-only", () => {
  const STEP = "wf:wf_1:wf_1/build";

  function stepCall(status: string, subtask: string): ToolCall {
    return {
      id: "wf-tc-1",
      title: "Run command",
      kind: "execute",
      status,
      agent_subtask_id: subtask,
    } as unknown as ToolCall;
  }

  /** An assistant message ALREADY carrying `tc` as a `subtask`-stamped tool_use
   *  block, so the first `upsertToolCall` for it takes the update arm. */
  function withStepTool(id: string, tc: ToolCall, subtask: string): Message {
    return {
      id,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: [
        { type: "text", text: "started" },
        { type: "tool_use", tool_call_id: tc.id, agent_subtask_id: subtask },
      ],
      tool_calls: [tc],
    } as unknown as Message;
  }

  it("classifies chunk on the FIRST sighting inside a mounted message", async () => {
    const chat = "cause-wftool-new";
    const kids = mount(chat, [user("wt-u1", "run it"), assistant("wt-a1", "started")], true);
    expect(kids.length).toBe(1);
    const before = seamCounts();

    // Block index 1: a NEW block of an already-mounted message, which is the arm
    // that used to force `shape` for every one of a run's tool calls.
    store.upsertToolCall(chat, "wt-a1", stepCall("in_progress", STEP), 1);
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("chunk");
    expect(seamCounts()).toEqual(before);
    const after = [...viewRoot().children];
    expect(after.every((el, i) => el === kids[i])).toBe(true);
    // And no card of it is on screen, which is what makes the skip free.
    expect(viewRoot().textContent).not.toContain("Run command");
  });

  it("classifies chunk on an UPDATE, where no mounted card owns a signal", async () => {
    const chat = "cause-wftool-upd";
    const tc = stepCall("in_progress", STEP);
    mount(chat, [user("wu-u1", "run it"), withStepTool("wu-a1", tc, STEP)], true);
    // Leave the flushed cause at `shape` first, so the assertion below reads the
    // UPDATE's own classification rather than a leftover from the create.
    store.upsertMessage(chat, user("wu-u1", "RUN IT"));
    await flushed();
    expect(store.renderCauseOf(chat).cause).toBe("shape");
    const before = seamCounts();

    store.upsertToolCall(chat, "wu-a1", { ...tc, status: "completed" } as ToolCall, 1);
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("chunk");
    expect(seamCounts()).toEqual(before);
  });

  it("still classifies shape for the same call with no subtask id", async () => {
    const chat = "cause-wftool-none";
    mount(chat, [user("wn-u1", "run it"), assistant("wn-a1", "started")], true);
    const before = seamCounts();

    store.upsertToolCall(chat, "wn-a1", stepCall("in_progress", ""), 1);
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("shape");
    expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBe(before["projectTurns"]! + 1);
    // It mounted, which is why the pass was needed.
    expect(viewRoot().textContent).toContain("Run command");
  });

  it("still classifies shape for a MALFORMED step id, which the dispatcher renders", async () => {
    const chat = "cause-wftool-bad";
    mount(chat, [user("wb-u1", "run it"), assistant("wb-a1", "started")], true);
    const before = seamCounts();

    // `wf:` prefix, no second colon: `parseStepSubtask` returns null, so the
    // dispatcher takes its delegate-box fallback and the store must NOT skip.
    store.upsertToolCall(chat, "wb-a1", stepCall("in_progress", "wf:no-node-path"), 1);
    await flushed();

    expect(store.renderCauseOf(chat).cause).toBe("shape");
    expect(vi.mocked(turnsMod.projectTurns).mock.calls.length).toBe(before["projectTurns"]! + 1);
    expect(viewRoot().querySelector(".subagent-block")).not.toBeNull();
  });
});

// ---------------------------------------------------------------------------
// The projection's LIVENESS input, pinned at the composition site.
//
// It lives in this file rather than its own because the harness is exactly what the
// property needs and nothing else has it: the real store, the real `messages.ts`
// paint, a spy on `projectTurns`, and a mounted view to read the rendered card off.
//
// The defect: `thinking` is client memory that starts false, so between
// `GET /api/chats/{id}` painting and the HELD `turn_state` frame releasing, a turn
// whose reply is still in the server's in-memory buffer read "not running". The
// projection then derived `unknown` — "nothing closed this turn" — and mounted an
// outcome mark and a `.turn-notice` row for a turn the server knew was running.
//
// The store composes the two facts (`turnLive`); this asserts `messages.ts` passes
// the composition rather than the flag, and that the card goes quiet as a result.
// ---------------------------------------------------------------------------

/** A session carrying the server's own liveness statement. */
function liveSession(id: string, msgs: Message[], over: Partial<Session>): Session {
  return { ...session(id, msgs, false), ...over } as Session;
}

describe("the transcript passes composed liveness, not the thinking flag", () => {
  it("paints NO outcome mark for a carrier-less newest turn the SERVER says is open", async () => {
    // The mid-turn reload record: the prompt persisted, the reply still buffered, so
    // no assistant message and no carrier. `thinking` is false because no frame has
    // arrived yet — which is the whole window.
    const chat = "live-open";
    store.setSessions([liveSession(chat, [user("lo-u1", "do the thing")], { turn_open: true })]);
    store.setActive(chat);
    await flushed();

    const calls = vi.mocked(turnsMod.projectTurns).mock.calls;
    const last = calls.at(-1);
    expect(last?.[1], "the projection's liveness input is the composed answer").toBe(true);

    const card = viewRoot().querySelector(".turn");
    expect(card, "the turn painted").not.toBeNull();
    // The two surfaces the reported flash was measured on. `running`'s treatment
    // already is "no settled mark", so this needed no suppression rule — which is
    // why the fix is the derivation and not the renderer.
    expect(card?.querySelector(".turn-notice"), "no failure notice on a live turn").toBeNull();
    expect(
      card?.querySelector(".turn-footer .turn-ledger-glyph"),
      "no footer outcome glyph on a live turn",
    ).toBeNull();
  });

  it("still paints the neutral mark when the server says NO turn is open", async () => {
    // The direction the fix must not erase: after a server restart mid-turn no turn
    // is open, because the process died — so the newest turn genuinely is one nothing
    // closed, and its notice is honest.
    const chat = "live-closed";
    store.setSessions([liveSession(chat, [user("lc-u1", "do the thing")], { turn_open: false })]);
    store.setActive(chat);
    await flushed();

    expect(vi.mocked(turnsMod.projectTurns).mock.calls.at(-1)?.[1]).toBe(false);
    const card = viewRoot().querySelector(".turn");
    expect(card?.querySelector(".turn-notice"), "an unreadable end still says so").not.toBeNull();
  });
});
