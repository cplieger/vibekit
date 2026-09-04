// ---------------------------------------------------------------------------
// Turn residency over the fold policy: which turns carry a real `.turn-body`.
//
// The fold policy stays the single authority for OPEN/CLOSED (fold-state.test.ts
// pins it); these cases pin MOUNTEDNESS, which `block-window.ts` decides on a
// BLOCK and TOOL-CARD budget. The planner's own arithmetic is
// block-window.test.ts's; what is here is the renderer's half — a non-resident
// turn is a header/footer stub whose body DOM does not exist until an interaction
// or a fold-pass transition builds it, and a stub always offers the toggle that
// reveals it.
//
// WHY THE FIXTURES ARE BIG. Residency is measured in what a paint costs, so a
// case that wants a stub has to actually spend the budget: eight prose turns cost
// about sixteen blocks and are ALL resident, correctly. That is the same fact the
// old `TURNS_WARM = 5` window got wrong from the other side — it stubbed those
// eight cheap turns while mounting one 580-block turn whole.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { userEvent } from "vitest/browser";

// The render graph reaches the shared DOM registry, which throws on a missing app
// root. Every id has to exist before the imports below are evaluated.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
]) {
  const d = document.createElement("div");
  d.id = id;
  document.body.appendChild(d);
}

// scroll.ts is a self-initialising singleton over a real scroller; the canonical
// mock is what every other suite in this graph uses. Its compensation helpers
// run their mutation, so the fold pass applies immediately unless a case
// overrides them to hold the deferred queue open.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The graph's network edge, real except for the two GETs these cases drive: a
// run's state (the live-run fold input) and the rail's session-wide turn index.
const runStatus = new Map<string, string>();
const railTurns: { turns: unknown[] } = { turns: [] };
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn((path: string) => {
    const run = /^\/api\/runs\/([^/?]+)$/.exec(path);
    if (run !== null) {
      const id = decodeURIComponent(run[1] ?? "");
      const status = runStatus.get(id);
      return Promise.resolve(
        status === undefined ? null : { workflowId: id, state: { workflowId: id, status } },
      );
    }
    if (/^\/api\/chats\/[^/]+\/turns$/.test(path)) {
      return Promise.resolve(railTurns);
    }
    return Promise.resolve(null);
  }),
}));

const { mountChatView, mountTurnBody, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages } = await import("./store.js");
const { setTurnOpen, openForSearch, clearSearchOpened, _resetFoldStateForTest } =
  await import("./fold-state.js");
const { RESIDENT_BLOCKS, RESIDENT_TOOL_CALLS } = await import("./block-window.js");
const { blockTextSigs, ensureBlockTextSig, blockKey } = await import("./store-signals.js");
const { openContainerKeys } = await import("./messages-blocks.js");
const { invalidateRun } = await import("./run-store.js");
const { loadTurnRail, resetTurnRail } = await import("./turn-rail.js");
const { scrollMock } = await import("./__test-helpers__/scroll-mock.js");
const { KEY_ATTR } = await import("./reconcile.js");

const messagesEl = document.getElementById("messages") as HTMLElement;

// --- Fixtures ---------------------------------------------------------------

interface Msg {
  id: string;
  role: string;
  ts: number;
  content?: string;
  blocks?: unknown[];
  tool_calls?: unknown[];
  refusal?: Record<string, unknown>;
}

function user(id: string, text = `prompt ${id}`): Msg {
  return { id, role: "user", ts: 1, content: text };
}

function asst(id: string, text = `reply ${id}`): Msg {
  return { id, role: "assistant", ts: 2, content: text, blocks: [{ type: "text", text }] };
}

/** `n` completed user+assistant turns; turn i's id is `u{i}` (1-based). Two
 *  blocks each, so a chat of them is nowhere near the budget. */
function plainTurns(n: number): Msg[] {
  const out: Msg[] = [];
  for (let i = 1; i <= n; i++) {
    out.push(user(`u${String(i)}`), asst(`a${String(i)}`));
  }
  return out;
}

/** Like `plainTurns`, but every turn also ran a tool, so its fold HIDES
 *  something and the turn offers the toggle. Prose-only turns don't: their
 *  face equals their body, so they carry `data-no-fold` and stay open. */
function toolTurns(n: number): Msg[] {
  const out: Msg[] = [];
  for (let i = 1; i <= n; i++) {
    const tc = `tc${String(i)}`;
    out.push(user(`u${String(i)}`), {
      id: `a${String(i)}`,
      role: "assistant",
      ts: 2,
      content: `reply u${String(i)}`,
      blocks: [
        { type: "tool_use", tool_call_id: tc },
        { type: "text", text: `reply u${String(i)}` },
      ],
      tool_calls: [{ id: tc, title: "Read file", kind: "read", status: "completed" }],
    });
  }
  return out;
}

/** One turn costing `blocks` blocks, spread over rows of 8 so the builder has
 *  several slices to take. The `id` prefixes the turn's opening message. */
function heavyTurn(id: string, blocks: number): Msg[] {
  const rows = Math.ceil(blocks / 8);
  const out: Msg[] = [user(id, `prompt ${id}`)];
  for (let r = 0; r < rows; r++) {
    out.push({
      id: `${id}-a${String(r)}`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: Array.from({ length: 8 }, (_, b) => ({
        type: "text",
        text: `chunk ${String(r)}.${String(b)}`,
      })),
    });
  }
  return out;
}

/** One turn holding `tools` tool cards in a single message — cheap in BLOCKS
 *  relative to the block budget, expensive in cards. */
function toolHeavyTurn(id: string, tools: number): Msg[] {
  const ids = Array.from({ length: tools }, (_, i) => `${id}-tc${String(i)}`);
  return [
    user(id, `prompt ${id}`),
    {
      id: `${id}-a`,
      role: "assistant",
      ts: 2,
      content: "",
      blocks: ids.map((tc) => ({ type: "tool_use", tool_call_id: tc })),
      tool_calls: ids.map((tc) => ({
        id: tc,
        title: "Read file",
        kind: "read",
        status: "completed",
      })),
    },
  ];
}

function activate(chatID: string, messages: Msg[], thinking = false): void {
  setSessions([
    {
      id: chatID,
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      supervised_mode: false,
      effort: "",
      effort_levels: [],
      effort_active: "",
      usage: { context_size: 0 },
      message_count: messages.length,
      messages,
      has_more: false,
      thinking,
      working_label: "Thinking",
    },
  ] as never);
  setActive(chatID);
  // A same-id re-activation writes no signal the paint effect tracks; the bump
  // is what store-load.ts issues after every page mutation.
  bumpMessages(chatID);
}

function card(turnID: string): HTMLElement {
  // The card walk roots at the ACTIVE transcript view: the multiplexer holds
  // one view per resident chat, and this suite mints a fresh chat per case.
  const root = activeTranscriptView() ?? messagesEl;
  for (const child of root.children) {
    if (child.getAttribute(KEY_ATTR) === turnID) {
      return child as HTMLElement;
    }
  }
  throw new Error(`no card for turn ${turnID}`);
}

function hasBody(turnID: string): boolean {
  return card(turnID).querySelector(":scope > .turn-body") !== null;
}

function isFolded(turnID: string): boolean {
  return card(turnID).hasAttribute("data-folded");
}

function rowsIn(turnID: string): number {
  return card(turnID).querySelectorAll(`:scope > .turn-body > [${KEY_ATTR}]`).length;
}

let seq = 0;
/** A fresh chat id per case, so fold overrides and search reveals cannot bleed. */
function chatID(): string {
  seq++;
  return `c-res-${String(seq)}`;
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  _resetFoldStateForTest();
  runStatus.clear();
  resetTurnRail();
  // Tear the previous case's transcript down through the renderer's own door
  // (an empty active id runs teardownAll), so per-message state cannot leak
  // between cases.
  setSessions([] as never);
  setActive("");
});

// --- What residency is measured in --------------------------------------------

describe("the mounted derivation", () => {
  it("mounts every turn of a cheap chat, however many there are", () => {
    const id = chatID();
    activate(id, toolTurns(8));
    // 8 turns is more than the old 5-turn window, and 8 tool-bearing turns cost
    // 24 blocks against a 320-block budget. Distance is not a cost.
    for (let n = 1; n <= 8; n++) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} body`).toBe(true);
    }
  });

  it("stubs a SINGLE over-budget turn, which is the shape the turn count could not reach", () => {
    const id = chatID();
    activate(id, heavyTurn("big", RESIDENT_BLOCKS + 64));
    expect(hasBody("big")).toBe(false);
    expect(isFolded("big")).toBe(true);
  });

  it("stubs the older turns behind an over-budget newest one, contiguously", () => {
    const id = chatID();
    activate(id, [...toolTurns(3), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    expect(hasBody("big")).toBe(false);
    for (const n of [1, 2, 3]) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} stub`).toBe(false);
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} born folded`).toBe(true);
    }
  });

  it("stubs a turn the TOOL budget rejects and the block budget would not", () => {
    const id = chatID();
    // Its blocks are one per tool card, well inside the block budget; the cards
    // are what a paint pays for.
    expect(RESIDENT_TOOL_CALLS + 1).toBeLessThan(RESIDENT_BLOCKS);
    activate(id, toolHeavyTurn("tools", RESIDENT_TOOL_CALLS + 1));
    expect(hasBody("tools")).toBe(false);
  });

  it("keeps the RUNNING turn resident whatever it costs", () => {
    const id = chatID();
    // A live turn's body arrives one block at a time, so it never costs a cold
    // build — and the reader is watching it.
    activate(id, heavyTurn("live", RESIDENT_BLOCKS * 2), true);
    expect(hasBody("live")).toBe(true);
    expect(isFolded("live")).toBe(false);
  });

  it("keeps a hand-opened turn resident whatever it costs, across a repaint", () => {
    const id = chatID();
    setTurnOpen(id, "big", true);
    activate(id, [...heavyTurn("big", RESIDENT_BLOCKS + 64), ...plainTurns(2)]);
    expect(hasBody("big")).toBe(true);
    expect(isFolded("big")).toBe(false);
    bumpMessages(id, "shape");
    expect(hasBody("big")).toBe(true);
    expect(isFolded("big")).toBe(false);
  });

  it("keeps a search-opened turn resident whatever it costs", () => {
    const id = chatID();
    openForSearch(id, "big");
    activate(id, [...heavyTurn("big", RESIDENT_BLOCKS + 64), ...plainTurns(2)]);
    expect(hasBody("big")).toBe(true);
  });

  it("does not pin a turn the reader FOLDED — a recorded fold is not a request for its body", () => {
    const id = chatID();
    setTurnOpen(id, "big", false);
    activate(id, [...heavyTurn("big", RESIDENT_BLOCKS + 64), ...plainTurns(2)]);
    expect(hasBody("big")).toBe(false);
  });

  it("a stub renders header and footer only, with no message rows and no body element", () => {
    const id = chatID();
    activate(id, heavyTurn("big", RESIDENT_BLOCKS + 64));
    const stub = card("big");
    expect(stub.querySelector(":scope > .turn-header")).not.toBeNull();
    expect(stub.querySelector(`[${KEY_ATTR}="big-a0"]`)).toBeNull();
    expect(stub.querySelectorAll(":scope > *").length).toBeLessThanOrEqual(3);
  });
});

// --- Disclosure, which residency does not decide -------------------------------

describe("the fold policy over residency", () => {
  it("folds every resident turn but the newest when its fold hides something", () => {
    const id = chatID();
    activate(id, toolTurns(8));
    for (let n = 1; n <= 7; n++) {
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} folded`).toBe(true);
    }
    expect(isFolded("u8")).toBe(false);
  });

  it("leaves a prose-only turn OPEN while resident — its fold would hide nothing", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    // One prose answer per turn, so a face would equal its body: an auto-fold
    // there animates and changes nothing (user report, 2026-08-31).
    for (let n = 1; n <= 8; n++) {
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} open`).toBe(false);
      expect(card(`u${String(n)}`).hasAttribute("data-no-fold")).toBe(true);
    }
  });

  it("stamps data-no-fold on the newest turn and on hides-nothing turns only", () => {
    const id = chatID();
    activate(id, toolTurns(2));
    // Both turns ran tools. u2 is newest, so it offers no fold; u1 does.
    expect(card("u1").hasAttribute("data-no-fold")).toBe(false);
    expect(card("u2").hasAttribute("data-no-fold")).toBe(true);
    // A third (prose-only) turn arrives: u2 gains its toggle, u3 offers none —
    // newest AND nothing to hide.
    activate(id, [...toolTurns(2), ...plainTurns(3).slice(4)]);
    expect(card("u2").hasAttribute("data-no-fold")).toBe(false);
    expect(card("u3").hasAttribute("data-no-fold")).toBe(true);
  });

  it("offers the toggle on EVERY stub, the newest included", () => {
    const id = chatID();
    activate(id, heavyTurn("big", RESIDENT_BLOCKS + 64));
    // The newest turn normally offers no fold — there is nothing after it to get
    // back to. On a stub the toggle is the only route to a body that does not
    // exist yet, so residency outranks that rule; without this the reader would
    // be stranded looking at a claim line.
    expect(card("big").hasAttribute("data-no-fold")).toBe(false);
  });

  it("folds a turn holding a live workflow run — the face carries a duplicate card", async () => {
    const id = chatID();
    runStatus.set("wf-live", "running");
    invalidateRun("wf-live");
    // One macrotask for the mocked fetch to land in the run cell.
    await new Promise((r) => setTimeout(r, 0));
    const launcher: Msg = {
      id: "a1",
      role: "assistant",
      ts: 2,
      content: "",
      blocks: [{ type: "tool_use", tool_call_id: "tc-run" }],
      tool_calls: [
        {
          id: "tc-run",
          title: "Run Workflow",
          kind: "other",
          status: "completed",
          workflow_id: "wf-live",
        },
      ],
    };
    activate(id, [user("u1"), launcher, ...plainTurns(4).slice(2)]);
    expect(isFolded("u1")).toBe(true);
    // The collapsed face is a CARD-level child sitting where the body was —
    // above the ledger footer, never inside it, so the footer keeps its
    // open-state row (ledger + actions + Rewind) untouched.
    const face = card("u1").querySelector(":scope > .turn-face");
    expect(face).not.toBeNull();
    expect(face?.querySelector(".run-card")).not.toBeNull();
    expect(face?.nextElementSibling?.classList.contains("turn-footer")).toBe(true);
  });

  it("the NEWEST turn ignores a recorded collapse — it cannot be folded", () => {
    // Its toggle is hidden (data-no-fold), so a recorded fold can only be a
    // leftover from an earlier build or from before a rewind made this turn
    // newest; honouring it would strand the tail closed with no control left
    // to reopen it (user report, 2026-08-31).
    const id = chatID();
    setTurnOpen(id, "u8", false);
    activate(id, toolTurns(8));
    expect(isFolded("u8")).toBe(false);
    expect(hasBody("u8")).toBe(true);
    expect(card("u8").hasAttribute("data-no-fold")).toBe(true);
    // The same override applies once the turn stops being newest.
    activate(id, [...toolTurns(8), ...plainTurns(9).slice(16)]);
    expect(isFolded("u8")).toBe(true);
    expect(hasBody("u8")).toBe(true);
  });
});

// --- Lifecycle: resident → stub -------------------------------------------------

describe("the residency lifecycle", () => {
  it("keeps signals and containers while resident, and drops both at the unmount", async () => {
    const id = chatID();
    // The target turn carries a delegate box so the container registry has an
    // entry to lose, and a minted block signal so the signal maps do too.
    const target: Msg = {
      id: "a1",
      role: "assistant",
      ts: 2,
      content: "parent",
      blocks: [
        { type: "text", text: "parent prose" },
        { type: "tool_use", tool_call_id: "tc-inv", agent_subtask_id: "sub-leak" },
        { type: "text", text: "delegate prose", agent_subtask_id: "sub-leak" },
      ],
      tool_calls: [
        {
          id: "tc-inv",
          title: "Sub-agent: helper",
          kind: "other",
          status: "completed",
          agent_subtask_id: "sub-leak",
        },
      ],
    };
    activate(id, [user("u1"), target]);
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false); // the open tail

    // The signal a streamed block would have minted, registered per message so
    // the unmount's clearBlockSigsFor can find it.
    ensureBlockTextSig("a1", 0, "parent prose");
    expect(blockTextSigs.get(blockKey("a1", 0))).toBeDefined();

    // Open the delegate box: the container registry now holds its subtask.
    (card("u1").querySelector(".subagent-header") as HTMLElement).click();
    expect(openContainerKeys().has("sub-leak")).toBe(true);

    // Resident and folded: three more turns fold the target, and nothing about
    // its body or its state changes.
    activate(id, [user("u1"), target, ...plainTurns(4).slice(2)]);
    expect(isFolded("u1")).toBe(true);
    expect(hasBody("u1")).toBe(true);
    expect(blockTextSigs.get(blockKey("a1", 0))).toBeDefined();
    expect(openContainerKeys().has("sub-leak")).toBe(true);

    // Resident → stub: a newest turn big enough to spend the whole budget.
    activate(id, [user("u1"), target, ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(false);
    });
    expect(card("u1").querySelector(`[${KEY_ATTR}="a1"]`)).toBeNull();
    expect(blockTextSigs.get(blockKey("a1", 0))).toBeUndefined();
    expect(openContainerKeys().has("sub-leak")).toBe(false);
  });

  it("defers the unmount while reading and applies it height-compensated", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    openForSearch(id, "u1");
    bumpMessages(id, "shape");
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false);

    // The reader is mid-transcript: the fold pass must queue, not apply.
    const queue: (() => void)[] = [];
    scrollMock.deferWhileReading.mockImplementation((mutate: () => void) => {
      queue.push(mutate);
    });
    scrollMock.preserveReadingPosition.mockClear();

    // The reveal goes away AND the budget is spent, so u1 loses both its pin and
    // its place: a search close alone leaves a cheap turn resident now.
    clearSearchOpened(id);
    setSessions([] as never);
    activate(id, [...plainTurns(8), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);

    // Deferred: the body and its open state are both still there.
    expect(hasBody("u1")).toBe(true);
    expect(queue.length).toBeGreaterThan(0);

    // The reader returns: the queued flip runs inside the compensation helper.
    for (const fn of queue) {
      fn();
    }
    expect(scrollMock.preserveReadingPosition).toHaveBeenCalledWith(
      expect.any(Function),
      "content-growth",
    );
    expect(hasBody("u1")).toBe(false);
    expect(isFolded("u1")).toBe(true);
  });
});

// --- The on-demand build -------------------------------------------------------

describe("expanding a stub", () => {
  it("a fold-toggle click mounts the body and opens the turn in the same interaction", async () => {
    const id = chatID();
    activate(id, [...toolTurns(1), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    expect(hasBody("u1")).toBe(false);

    (card("u1").querySelector(".turn-fold-toggle") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
      expect(isFolded("u1")).toBe(false);
    });
    expect(card("u1").querySelector(`[${KEY_ATTR}="a1"]`)).not.toBeNull();
    // The click recorded the reader's own choice, which also PINS the turn
    // resident: it survives a repaint open, with the budget still spent.
    bumpMessages(id);
    expect(isFolded("u1")).toBe(false);
    expect(hasBody("u1")).toBe(true);
  });

  it("keyboard activation on the stub header's toggle mounts it too", async () => {
    const id = chatID();
    activate(id, [...toolTurns(1), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    expect(hasBody("u1")).toBe(false);

    (card("u1").querySelector(".turn-fold-toggle") as HTMLButtonElement).focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
      expect(isFolded("u1")).toBe(false);
    });
  });

  it("a stub header keeps the disclosure contract while closed", () => {
    const id = chatID();
    activate(id, [...toolTurns(1), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    const header = card("u1").querySelector<HTMLElement>(":scope > .turn-header");
    const toggle = header?.querySelector(":scope > .turn-head-row > .turn-fold-toggle");
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    // The state lives on the BUTTON. `.turn-header` is a plain div, and
    // `aria-expanded` on one is an ARIA violation rather than a redundancy.
    expect(header?.hasAttribute("aria-expanded")).toBe(false);
  });

  it("expanding a stub renders exactly what a resident turn renders, one copy of every block type", async () => {
    // One message carrying every block surface: text bubble, reasoning trace,
    // plain tool card, todo checklist, delegate box, run card.
    const everyBlock = (suffix: string): Msg => ({
      id: `ab-${suffix}`,
      role: "assistant",
      ts: 2,
      content: "the reply",
      blocks: [
        { type: "thinking", thinking: "considering" },
        { type: "text", text: "the reply" },
        { type: "tool_use", tool_call_id: "tc-read" },
        { type: "tool_use", tool_call_id: "tc-todo" },
        { type: "tool_use", tool_call_id: "tc-inv", agent_subtask_id: "sub-par" },
        { type: "text", text: "delegate words", agent_subtask_id: "sub-par" },
        { type: "tool_use", tool_call_id: "tc-run" },
      ],
      tool_calls: [
        { id: "tc-read", title: "Read file", kind: "read", status: "completed", output: "ok" },
        {
          id: "tc-todo",
          title: "todo_list",
          kind: "other",
          status: "completed",
          input: { todos: [{ content: "step one", status: "completed" }] },
        },
        {
          id: "tc-inv",
          title: "Sub-agent: helper",
          kind: "other",
          status: "completed",
          agent_subtask_id: "sub-par",
        },
        {
          id: "tc-run",
          title: "Run Workflow",
          kind: "other",
          status: "completed",
          workflow_id: "wf-par",
        },
      ],
    });

    // Reference: the same turn mounted RESIDENT and folded by the ordinary paint.
    //
    // Identity artifacts are normalized on BOTH sides before comparing; none of
    // them is turn shape. The chat-switch stagger style is the switch's entry
    // animation (an on-demand build mounts silently, like pagination); the
    // disclosure ids come off a page-global counter; the delegate's Open link
    // carries its own chat's id.
    const normalize = (html: string, chat: string): string =>
      html
        .replaceAll(/ style="--stagger-index: \d+;"/g, "")
        .replaceAll(/uip-disclosure-\d+/g, "uip-disclosure-N")
        .replaceAll(chat, "CHAT");
    const warmChat = chatID();
    activate(warmChat, [user("uv", "the prompt"), everyBlock("v"), ...plainTurns(4).slice(0, 4)]);
    expect(isFolded("uv")).toBe(true);
    const warmBody = card("uv").querySelector(":scope > .turn-body") as HTMLElement;
    for (const sel of [
      ".reasoning-block",
      ".message.assistant",
      ".tool-call",
      ".todo-list",
      ".subagent-block",
      ".run-card",
    ]) {
      expect(warmBody.querySelector(sel), `resident ${sel}`).not.toBeNull();
    }
    const wantHTML = normalize(warmBody.innerHTML, warmChat).replaceAll("ab-v", "ab-s");

    // Subject: the same content behind a budget-spending newest turn, expanded
    // on demand.
    const stubChat = chatID();
    activate(stubChat, [
      user("uv", "the prompt"),
      everyBlock("s"),
      ...heavyTurn("big", RESIDENT_BLOCKS + 64),
    ]);
    expect(hasBody("uv")).toBe(false);
    await mountTurnBody(stubChat, "uv");
    const stubBody = card("uv").querySelector(":scope > .turn-body") as HTMLElement;
    expect(normalize(stubBody.innerHTML, stubChat)).toBe(wantHTML);
  });

  it("yields between block batches on a heavy reveal and still lands complete", async () => {
    const id = chatID();
    // 12 rows x 8 blocks = 96 blocks: several BUILD_BATCH_BLOCKS slices.
    activate(id, [...heavyTurn("u-heavy", 96), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    expect(hasBody("u-heavy")).toBe(false);

    const build = mountTurnBody(id, "u-heavy");
    // The first batch lands synchronously in the interaction...
    const partial = rowsIn("u-heavy");
    expect(partial).toBeGreaterThan(0);
    expect(partial).toBeLessThan(12);
    // ...and the rest follow across yields.
    await build;
    expect(rowsIn("u-heavy")).toBe(12);
  });
});

// --- One builder for the cold paint too ----------------------------------------

describe("the cold build", () => {
  it("takes one slice inside the paint and finishes the rest off the frame", async () => {
    const id = chatID();
    // Resident because the reader opened it, and far past one slice: the paint
    // that creates the card may not also build 40 rows into it.
    setTurnOpen(id, "u-heavy", true);
    activate(id, heavyTurn("u-heavy", 320));
    expect(hasBody("u-heavy")).toBe(true);
    const afterPaint = rowsIn("u-heavy");
    expect(afterPaint).toBeGreaterThan(0);
    expect(afterPaint).toBeLessThan(40);
    await vi.waitFor(() => {
      expect(rowsIn("u-heavy")).toBe(40);
    });
  });

  it("builds a within-budget turn whole in the paint — one slice covers it", () => {
    const id = chatID();
    activate(id, plainTurns(3));
    // Nothing to yield for: three prose turns are one row each.
    expect(rowsIn("u1")).toBe(1);
    expect(rowsIn("u3")).toBe(1);
  });

  it("stops taking slices on the frame once the pass has spent its block allowance", async () => {
    const id = chatID();
    // Twelve turns the reader opened by hand. Pins are exempt from the residency
    // budget, so all twelve mount — and without a pass-wide allowance all twelve
    // would take a slice in the paint frame.
    for (let n = 1; n <= 12; n++) {
      setTurnOpen(id, `h${String(n)}`, true);
    }
    const messages: Msg[] = [];
    for (let n = 1; n <= 12; n++) {
      messages.push(...heavyTurn(`h${String(n)}`, 40));
    }
    activate(id, messages);

    // A slice is 32 blocks (4 of these 8-block rows) and the pass may mount 320,
    // so exactly ten bodies get one; the last two are born empty.
    const started = [];
    for (let n = 1; n <= 12; n++) {
      const rows = rowsIn(`h${String(n)}`);
      expect(hasBody(`h${String(n)}`), `h${String(n)} resident`).toBe(true);
      expect([0, 4]).toContain(rows);
      if (rows > 0) {
        started.push(n);
      }
    }
    expect(started).toHaveLength(10);

    // Nothing is lost: the drain finishes all twelve off the frame.
    await vi.waitFor(() => {
      for (let n = 1; n <= 12; n++) {
        expect(rowsIn(`h${String(n)}`), `h${String(n)} complete`).toBe(5);
      }
    });
  });

  it("takes an evicted turn's owed slices with it instead of rebuilding the body", async () => {
    const id = chatID();
    // Resident by the reader's own hand and far past one slice, so the paint owes
    // a cold build; the newest turn has already spent the budget, so that pin is
    // the only thing keeping this turn mounted.
    setTurnOpen(id, "u-heavy", true);
    activate(id, [...heavyTurn("u-heavy", 320), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    const started = rowsIn("u-heavy");
    expect(started).toBeGreaterThan(0);
    expect(started).toBeLessThan(40);

    // The reader folds it again. The pin goes with the fold, so the fold pass
    // unmounts the body — and it calls the drain on the line after.
    setTurnOpen(id, "u-heavy", false);
    bumpMessages(id, "shape");
    expect(hasBody("u-heavy")).toBe(false);

    // The owed build must not come back and rebuild 40 rows under a folded card,
    // which is the work residency exists to refuse.
    await new Promise((r) => setTimeout(r, 60));
    expect(hasBody("u-heavy")).toBe(false);
  });
});

// --- Navigation surfaces --------------------------------------------------------

describe("navigation onto a stub", () => {
  it("a rail jump mounts the stub turn it lands on", async () => {
    const id = chatID();
    activate(id, [...plainTurns(2), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    expect(hasBody("u1")).toBe(false);
    railTurns.turns = Array.from({ length: 2 }, (_, i) => ({
      id: `u${String(i + 1)}`,
      n: i + 1,
      outcome: "completed",
      ts: (i + 1) * 60_000,
    }));
    await loadTurnRail(id);
    const marker = document.querySelector<HTMLButtonElement>(".turn-rail .rail-marker");
    expect(marker?.textContent).toBe("1");
    marker?.click();
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
    });
    // A jump navigates; it does not open. The turn lands exactly as a resident
    // folded turn does.
    expect(isFolded("u1")).toBe(true);
    expect(scrollMock.jumpTo).toHaveBeenCalled();
  });

  it("keeps the jumped-to turn resident across the next paint", async () => {
    // The jump records no fold and no reveal, so nothing in `fold-state.ts` speaks
    // for it — and the budget is already spent by the newest turn. Without a pin
    // the next paint evicts the body the jump just built, which is both a wasted
    // build and a turn the reader was sent to going hollow under them.
    const id = chatID();
    // Tool-bearing turns, so a fold HIDES something: this case reads the fold
    // state after a repaint, and a prose-only turn legitimately re-opens there.
    activate(id, [...toolTurns(2), ...heavyTurn("big", RESIDENT_BLOCKS + 64)]);
    railTurns.turns = Array.from({ length: 2 }, (_, i) => ({
      id: `u${String(i + 1)}`,
      n: i + 1,
      outcome: "completed",
      ts: (i + 1) * 60_000,
    }));
    await loadTurnRail(id);
    document.querySelector<HTMLButtonElement>(".turn-rail .rail-marker")?.click();
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
    });

    bumpMessages(id, "shape");
    expect(hasBody("u1")).toBe(true);
    // The pin is residency only: the jump still leaves the turn folded.
    expect(isFolded("u1")).toBe(true);
  });
});

// --- Pagination -------------------------------------------------------------------

describe("pagination prepends", () => {
  it("land as stubs, folded at birth even while the reader is reading", () => {
    const id = chatID();
    const current = heavyTurn("big", RESIDENT_BLOCKS + 64);
    activate(id, current);
    expect(hasBody("big")).toBe(false);

    // Reading: the fold pass would defer any state CHANGE — a born state is
    // not one, so the prepended stubs may not flash expanded first.
    scrollMock.readingState.mockReturnValue("reading");
    scrollMock.deferWhileReading.mockImplementation(() => {
      /* queue forever: nothing born may depend on this flushing */
    });
    activate(id, [...plainTurns(6), ...current]);

    // The newest turn already spent the budget, so every prepend is a stub.
    for (const n of [1, 2, 3, 4, 5, 6]) {
      expect(hasBody(`u${String(n)}`), `u${String(n)} stub`).toBe(false);
      expect(isFolded(`u${String(n)}`), `u${String(n)} born folded`).toBe(true);
    }
  });
});
