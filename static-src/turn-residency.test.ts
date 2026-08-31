// ---------------------------------------------------------------------------
// Three-tier turn residency over the fold policy (design D1).
//
// The fold policy stays the single authority for OPEN/CLOSED (fold-state.test.ts
// pins it, unmodified); these tests pin the MOUNTEDNESS axis layered on top:
//
//   mounted = foldOpen || distance-from-newest < TURNS_WARM
//
// Tier 1 (open) and tier 2 (warm) carry a real `.turn-body`; tier 3 is a
// header/footer stub whose body DOM does not exist until an interaction or a
// fold-pass transition builds it. Openness assertions here exist only to fence
// the two axes apart — a turn the fold holds open is mounted at ANY distance,
// and an explicit collapse of the newest turn folds it without unmounting it.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";
import { userEvent } from "@vitest/browser/context";

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
const { setTurnOpen, openForSearch, clearSearchOpened, _resetFoldStateForTest, TURNS_WARM } =
  await import("./fold-state.js");
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

/** `n` completed user+assistant turns; turn i's id is `u{i}` (1-based). */
function plainTurns(n: number): Msg[] {
  const out: Msg[] = [];
  for (let i = 1; i <= n; i++) {
    out.push(user(`u${String(i)}`), asst(`a${String(i)}`));
  }
  return out;
}

/** Like `plainTurns`, but every turn also ran a tool, so its fold HIDES
 *  something and the turn offers the toggle. Prose-only turns don't: their
 *  face equals their body, so they carry `data-no-fold` and stay open while
 *  warm. */
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

function activate(chatID: string, messages: Msg[], thinking = false): void {
  setSessions([
    {
      id: chatID,
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      available_modes: [],
      available_models: [],
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

// --- The derivation table ----------------------------------------------------

describe("the mounted derivation", () => {
  it("names the warm window's size as the constant the design pins", () => {
    // R3.1: tier sizes are named constants. The table below hardcodes its
    // distances; this is the one line that ties them to the name.
    expect(TURNS_WARM).toBe(5);
  });

  it("mounts the open tail and the warm window, and stubs everything older", () => {
    const id = chatID();
    activate(id, toolTurns(8));
    // 8 tool-bearing turns: distances 7..0. Warm = distance < 5 (indices 4..8
    // by 1-based turn number); the fold's tail keeps only the NEWEST turn open
    // — a turn auto-collapses when the next one starts.
    for (const [n, mounted, folded] of [
      [1, false, true],
      [2, false, true],
      [3, false, true],
      [4, true, true],
      [5, true, true],
      [6, true, true],
      [7, true, true],
      [8, true, false],
    ] as const) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} body`).toBe(mounted);
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} fold`).toBe(folded);
    }
  });

  it("a prose-only turn stays OPEN while warm — its fold would hide nothing", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    // Same distances, but every turn's face would equal its body (one prose
    // answer, no tools), so the warm window renders them open with no toggle:
    // an auto-fold there animates and changes nothing (user report,
    // 2026-08-31). Beyond the warm window they stub like any other turn — the
    // stub face IS the body's content for this class, so the swap is
    // invisible.
    for (const [n, mounted, folded] of [
      [1, false, true],
      [2, false, true],
      [3, false, true],
      [4, true, false],
      [5, true, false],
      [6, true, false],
      [7, true, false],
      [8, true, false],
    ] as const) {
      expect(hasBody(`u${String(n)}`), `turn ${String(n)} body`).toBe(mounted);
      expect(isFolded(`u${String(n)}`), `turn ${String(n)} fold`).toBe(folded);
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
    // A fourth arrives: u3 is no longer newest but is still no-fold, because
    // its fold would hide nothing.
    activate(id, [...toolTurns(2), ...plainTurns(4).slice(4)]);
    expect(card("u3").hasAttribute("data-no-fold")).toBe(true);
    expect(card("u4").hasAttribute("data-no-fold")).toBe(true);
  });

  it("a stub renders header and footer only, with no message rows and no body element", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    const stub = card("u1");
    expect(stub.querySelector(":scope > .turn-header")).not.toBeNull();
    expect(stub.querySelector(`[${KEY_ATTR}="a1"]`)).toBeNull();
    expect(stub.querySelectorAll(":scope > *").length).toBeLessThanOrEqual(3);
  });

  it("keeps a hand-opened turn mounted at any distance", () => {
    const id = chatID();
    setTurnOpen(id, "u1", true);
    activate(id, plainTurns(10));
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false);
  });

  it("keeps a search-opened turn mounted at any distance", () => {
    const id = chatID();
    openForSearch(id, "u1");
    activate(id, plainTurns(10));
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false);
  });

  it("folds and eventually unmounts a failed turn like any other — the face carries the error", () => {
    const id = chatID();
    const failed: Msg = { ...asst("a1"), refusal: { category: "safety" } };
    activate(id, [user("u1"), failed, ...plainTurns(10).slice(2)]);
    expect(hasBody("u1")).toBe(false);
    expect(isFolded("u1")).toBe(true);
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
    activate(id, [user("u1"), launcher, ...plainTurns(10).slice(2)]);
    expect(isFolded("u1")).toBe(true);
    // The collapsed face is a CARD-level child sitting where the body was —
    // above the ledger footer, never inside it, so the footer keeps its
    // open-state row (ledger + actions + Rewind) untouched.
    const face = card("u1").querySelector(":scope > .turn-face");
    expect(face).not.toBeNull();
    expect(face?.querySelector(".run-card")).not.toBeNull();
    expect(face?.nextElementSibling?.classList.contains("turn-footer")).toBe(true);
  });

  it("keeps the streaming turn mounted and open", () => {
    const id = chatID();
    activate(id, plainTurns(3), true);
    expect(hasBody("u3")).toBe(true);
    expect(isFolded("u3")).toBe(false);
  });

  it("keeps an explicitly collapsed turn inside the warm window mounted", () => {
    const id = chatID();
    setTurnOpen(id, "u7", false);
    activate(id, toolTurns(8));
    expect(hasBody("u7")).toBe(true);
    expect(isFolded("u7")).toBe(true);
  });
});

describe("the fold override outranks the tier", () => {
  it("the NEWEST turn ignores a recorded collapse — it cannot be folded", () => {
    // Its toggle is hidden (data-no-fold), so a recorded fold can only be a
    // leftover from an earlier build or from before a rewind made this turn
    // newest; honouring it would strand the tail closed with no control left
    // to reopen it (user report, 2026-08-31: the last turn's collapse must
    // stay disabled).
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

// --- Lifecycle: 1 → 2 → 3 -----------------------------------------------------

describe("the tier lifecycle", () => {
  it("keeps signals and containers through 1→2, and drops both at the 2→3 unmount", async () => {
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
    expect(isFolded("u1")).toBe(false); // tier 1: the open tail

    // The signal a streamed block would have minted, registered per message so
    // the unmount's clearBlockSigsFor can find it.
    ensureBlockTextSig("a1", 0, "parent prose");
    expect(blockTextSigs.get(blockKey("a1", 0))).toBeDefined();

    // Open the delegate box: the container registry now holds its subtask.
    (card("u1").querySelector(".subagent-header") as HTMLElement).click();
    expect(openContainerKeys().has("sub-leak")).toBe(true);

    // 1→2: three more turns fold the target but keep it warm.
    activate(id, [user("u1"), target, ...plainTurns(4).slice(2)]);
    expect(isFolded("u1")).toBe(true);
    expect(hasBody("u1")).toBe(true);
    expect(blockTextSigs.get(blockKey("a1", 0))).toBeDefined();
    expect(openContainerKeys().has("sub-leak")).toBe(true);

    // 2→3: enough turns to push the target past the warm window.
    activate(id, [user("u1"), target, ...plainTurns(8).slice(2)]);
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(false);
    });
    expect(card("u1").querySelector(`[${KEY_ATTR}="a1"]`)).toBeNull();
    expect(blockTextSigs.get(blockKey("a1", 0))).toBeUndefined();
    expect(openContainerKeys().has("sub-leak")).toBe(false);
  });
});

// --- The deferred, compensated unmount ----------------------------------------

describe("the 1→3 flip", () => {
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

    clearSearchOpened(id);
    bumpMessages(id, "shape");

    // Deferred: the reveal's body and its open state are both still there.
    expect(hasBody("u1")).toBe(true);
    expect(isFolded("u1")).toBe(false);
    expect(queue.length).toBe(1);

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
    activate(id, toolTurns(8));
    expect(hasBody("u1")).toBe(false);

    (card("u1").querySelector(".turn-fold-toggle") as HTMLButtonElement).click();
    await vi.waitFor(() => {
      expect(hasBody("u1")).toBe(true);
      expect(isFolded("u1")).toBe(false);
    });
    expect(card("u1").querySelector(`[${KEY_ATTR}="a1"]`)).not.toBeNull();
    // The click recorded the reader's own choice: the turn survives a repaint
    // open, however far back it is.
    bumpMessages(id);
    expect(isFolded("u1")).toBe(false);
    expect(hasBody("u1")).toBe(true);
  });

  it("keyboard activation on the stub header's toggle mounts it too", async () => {
    const id = chatID();
    activate(id, toolTurns(8));
    expect(hasBody("u2")).toBe(false);

    (card("u2").querySelector(".turn-fold-toggle") as HTMLButtonElement).focus();
    await userEvent.keyboard("{Enter}");
    await vi.waitFor(() => {
      expect(hasBody("u2")).toBe(true);
      expect(isFolded("u2")).toBe(false);
    });
  });

  it("a prose-only stub offers no toggle, and a click on it changes nothing", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    expect(hasBody("u1")).toBe(false);
    expect(card("u1").hasAttribute("data-no-fold")).toBe(true);

    (card("u1").querySelector(".turn-fold-toggle") as HTMLButtonElement).click();
    // No override recorded, no body build queued: the turn's face already
    // shows everything the body would.
    expect(hasBody("u1")).toBe(false);
    expect(isFolded("u1")).toBe(true);
  });

  it("a stub header keeps the disclosure contract while closed", () => {
    const id = chatID();
    activate(id, plainTurns(8));
    const header = card("u1").querySelector<HTMLElement>(":scope > .turn-header");
    const toggle = header?.querySelector(":scope > .turn-head-row > .turn-fold-toggle");
    expect(toggle?.getAttribute("aria-expanded")).toBe("false");
    // The state lives on the BUTTON. `.turn-header` is a plain div, and
    // `aria-expanded` on one is an ARIA violation rather than a redundancy.
    expect(header?.hasAttribute("aria-expanded")).toBe(false);
  });

  it("expanding a stub renders exactly what a warm turn renders, one copy of every block type", async () => {
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

    // Reference: the same turn mounted WARM (distance 2, folded, body built by
    // the ordinary paint).
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
      expect(warmBody.querySelector(sel), `warm ${sel}`).not.toBeNull();
    }
    const wantHTML = normalize(warmBody.innerHTML, warmChat).replaceAll("ab-v", "ab-s");

    // Subject: the same content past the warm window, expanded on demand.
    const stubChat = chatID();
    activate(stubChat, [user("uv", "the prompt"), everyBlock("s"), ...plainTurns(16).slice(2)]);
    expect(hasBody("uv")).toBe(false);
    await mountTurnBody(stubChat, "uv");
    const stubBody = card("uv").querySelector(":scope > .turn-body") as HTMLElement;
    expect(normalize(stubBody.innerHTML, stubChat)).toBe(wantHTML);
  });

  it("yields between block batches on a heavy cold build and still lands complete", async () => {
    const id = chatID();
    // 12 messages × 8 blocks = 96 blocks: several BUILD_BATCH_BLOCKS slices.
    const body: Msg[] = [];
    for (let i = 0; i < 12; i++) {
      body.push({
        id: `heavy-${String(i)}`,
        role: "assistant",
        ts: 2,
        content: "",
        blocks: Array.from({ length: 8 }, (_, b) => ({
          type: "text",
          text: `chunk ${String(i)}.${String(b)}`,
        })),
      });
    }
    activate(id, [user("u-heavy"), ...body, ...plainTurns(10)]);
    expect(hasBody("u-heavy")).toBe(false);

    const build = mountTurnBody(id, "u-heavy");
    // The first batch lands synchronously in the interaction...
    const partial = card("u-heavy").querySelectorAll(`:scope > .turn-body > [${KEY_ATTR}]`).length;
    expect(partial).toBeGreaterThan(0);
    expect(partial).toBeLessThan(12);
    // ...and the rest follow across yields.
    await build;
    expect(card("u-heavy").querySelectorAll(`:scope > .turn-body > [${KEY_ATTR}]`).length).toBe(12);
  });
});

// --- Navigation surfaces --------------------------------------------------------

describe("navigation onto a stub", () => {
  it("a rail jump mounts the stub turn it lands on", async () => {
    const id = chatID();
    activate(id, plainTurns(8));
    expect(hasBody("u1")).toBe(false);
    railTurns.turns = Array.from({ length: 8 }, (_, i) => ({
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
    // folded turn does today.
    expect(isFolded("u1")).toBe(true);
    expect(scrollMock.jumpTo).toHaveBeenCalled();
  });
});

// --- Pagination -------------------------------------------------------------------

describe("pagination prepends", () => {
  it("land as stubs, folded at birth even while the reader is reading", () => {
    const id = chatID();
    activate(
      id,
      plainTurns(3).map((m) => ({ ...m, id: `new-${m.id}` })),
    );
    expect(hasBody("new-u1")).toBe(true);

    // Reading: the fold pass would defer any state CHANGE — a born state is
    // not one, so the prepended stubs may not flash expanded first.
    scrollMock.readingState.mockReturnValue("reading");
    scrollMock.deferWhileReading.mockImplementation(() => {
      /* queue forever: nothing born may depend on this flushing */
    });
    const older = plainTurns(6);
    const current = plainTurns(3).map((m) => ({ ...m, id: `new-${m.id}` }));
    activate(id, [...older, ...current]);

    // 9 turns: distances 8..0. The six prepends sit at distance 8..3 — the two
    // at 4 and 3 are warm (body, folded at birth), the four older are stubs.
    for (const n of [1, 2, 3, 4]) {
      expect(hasBody(`u${String(n)}`), `u${String(n)} stub`).toBe(false);
      expect(isFolded(`u${String(n)}`), `u${String(n)} born folded`).toBe(true);
    }
    for (const n of [5, 6]) {
      expect(hasBody(`u${String(n)}`), `u${String(n)} warm`).toBe(true);
    }
  });
});
