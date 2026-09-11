// ---------------------------------------------------------------------------
// The subagent page: the eviction exemption its tab earns, and the repaint its
// prose depends on.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal, touch } from "@cplieger/reactive";
import type { Message, Session } from "./types.js";

// tabs.ts's real graph reads the shared DOM registry at module scope, and
// `byId` throws on a missing element — so the hosts exist before any import.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  "tab-strip",
  // The page's own host: `paint` bails silently without it, which would make the
  // streaming case below pass on an empty document.
  "subagent-body",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// A spy mock rather than a factory: the graph behind subagent-view imports a
// wide slice of tabs.ts's surface, and a hand-kept name list here would rot.
// The spy keeps every export real and lets the cases below steer `hasTab`.
vi.mock("./tabs.js", { spy: true });
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));
// Spied so the paint effect's re-runs are COUNTABLE. Every export stays real, which
// `blockShape` and `shapeExtends` need — they run on every body render.
vi.mock("./subagent-slice.js", { spy: true });
// The delegation target. Spied rather than replaced, so the module's other exports
// stay real for the graph behind subagent-view; the one case that drives it stubs
// the implementation, because the real refresh reaches the loader.
vi.mock("./chat.js", { spy: true });

const store = await import("./store.js");
const { hasTab, openSubagentRefs } = await import("./tabs.js");
const { subagentRef } = await import("./tab-materialize.js");
const { sliceSubagentGroup } = await import("./subagent-slice.js");
const { subagentTabProjectsChat, showSubagent, refreshSubagent } =
  await import("./subagent-view.js");
const { refreshChatView } = await import("./chat.js");
const { blockKey, blockTextSigs } = await import("./store-signals.js");
const { mountedWindow } = await import("./messages-blocks.js");
const mockHasTab = vi.mocked(hasTab);

// --- The page's DEMAND input ---
//
// `openSubagentRefs` is what the page's lifetime hangs on: the demand effect drops the
// mounted page once no open subagent tab names a member of the group it projects. Under
// `{ spy: true }` it is the REAL reader answering `[]` from a projection no case here
// ever mutates, so every scenario below would be mounting a page nothing wants — the
// 13 cases that predate this passed only because that projection never bumped, so the
// effect never re-ran. A controllable fake instead, TRACKED like production's so
// stating a different demand re-runs the effect.
let refs: readonly string[] = [];
const refsVersion = signal(0);

/** State the open subagent tabs. Bumps a version the fake reads, which is the whole
 *  subscription: production's reader subscribes to the tab projection's own
 *  `stateVersion` the same way. */
function setSubagentTabs(open: readonly string[]): void {
  refs = open;
  refsVersion.value += 1;
}

/** Open ONE subagent tab and activate it, in production's order: the row lands in the
 *  projection, then the activation reaches `showSubagent` through the tab's `onShow`. */
function show(chatID: string, subtaskID: string): void {
  setSubagentTabs([subagentRef(chatID, subtaskID)]);
  showSubagent(chatID, subtaskID);
}

/** Withdraw the demand entirely: the reader closed every subagent tab. */
function closeSubagentTabs(): void {
  setSubagentTabs([]);
}

function session(id: string, messages: Message[]): Session {
  return {
    id,
    name: id,
    messages,
    message_count: messages.length,
    has_more: false,
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

function delegateMsg(id: string, subtask: string): Message {
  return {
    id,
    role: "assistant",
    ts: 1,
    content: "",
    blocks: [
      { type: "text", text: "parent prose" },
      { type: "text", text: "delegate work", agent_subtask_id: subtask },
    ],
  } as Message;
}

/** The page's own host, and the note the detail pane shows for a node with nothing
 *  in it. */
function body(): HTMLElement {
  return document.getElementById("subagent-body") as HTMLElement;
}
function emptyNoteText(): string {
  return body().querySelector(".ev-d-empty")?.textContent ?? "";
}

/** Click a stage's row in the left-hand tree, the way a reader does. `.ev-row-main`
 *  is the row's own click target; the row element carries the path. */
function clickRow(path: string): void {
  const row = body().querySelector<HTMLElement>(`.ev-row[data-path="${path}"] .ev-row-main`);
  expect(row).not.toBeNull();
  row?.click();
}

const DRIVER = "d-1";
const PLAN_CALL = `invoke_subagent_${DRIVER}_stage_plan`;
const REVIEW_CALL = `invoke_subagent_${DRIVER}_stage_review`;
const PLAN = "st-plan";
const REVIEW = "st-review";

/** One `orchestrate_subagent` pipeline of two stages, in one assistant message: the
 *  driver, an invocation per stage, and each stage's own prose. This is the shape that
 *  produces the left-hand list — two selectable rows, only one of which the tab names. */
function pipelineSession(
  chatID: string,
  msgID: string,
  opts: { reviewStatus?: string; reviewText?: string } = {},
): Session {
  const call = (id: string, subtask: string, status: string) => ({
    id,
    title: "Sub-agent: reviewer",
    status,
    kind: "other",
    ts: 1,
    agent_subtask_id: subtask,
    input: { name: "reviewer" },
  });
  const blocks: Record<string, unknown>[] = [
    { type: "text", text: "parent prose" },
    { type: "tool_use", tool_call_id: DRIVER },
    { type: "tool_use", tool_call_id: PLAN_CALL, agent_subtask_id: PLAN },
    { type: "text", text: "the plan stage report", agent_subtask_id: PLAN },
    { type: "tool_use", tool_call_id: REVIEW_CALL, agent_subtask_id: REVIEW },
  ];
  const reviewText = opts.reviewText ?? "the review stage report";
  if (reviewText !== "") {
    blocks.push({ type: "text", text: reviewText, agent_subtask_id: REVIEW });
  }
  return session(chatID, [
    {
      id: msgID,
      role: "assistant",
      ts: 1,
      content: "",
      blocks,
      tool_calls: [
        {
          id: DRIVER,
          title: "Orchestrate Sub-agent",
          status: "in_progress",
          kind: "other",
          ts: 1,
          input: { task: "review the diff", stages: [{ name: "plan" }, { name: "review" }] },
        },
        call(PLAN_CALL, PLAN, "completed"),
        call(REVIEW_CALL, REVIEW, opts.reviewStatus ?? "completed"),
      ],
    } as unknown as Message,
  ]);
}

beforeEach(() => {
  mockHasTab.mockReset();
  mockHasTab.mockReturnValue(false);
  // Re-installed per test: the root config sets `mockReset: true`, which restores a
  // `{ spy: true }` export to the original reader.
  refs = [];
  vi.mocked(openSubagentRefs).mockImplementation(() => {
    touch(refsVersion);
    return [...refs];
  });
});

// An open subagent tab projects its chat's transcript, so evicting that chat would
// blank a surface someone deliberately opened. The predicate is answered from the
// RESIDENT blocks — the subtask ids reachable from this chat are the ones on its
// blocks, and a tab for a delegate whose turn is not resident was already rendering
// the not-resident notice, so eviction changes nothing it was showing.
describe("subagentTabProjectsChat", () => {
  it("exempts a chat with an open subagent tab for one of its delegates", () => {
    store.setSessions([session("c1", [delegateMsg("m1", "st-1")])]);
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c1/st-1");

    expect(subagentTabProjectsChat("c1")).toBe(true);
    // The lookup is by the tab's own composite ref, chatID/subtaskID.
    expect(mockHasTab).toHaveBeenCalledWith("subagent", "c1/st-1");
  });

  it("exempts nothing when no subagent tab is open", () => {
    store.setSessions([session("c1", [delegateMsg("m1", "st-1")])]);
    expect(subagentTabProjectsChat("c1")).toBe(false);
  });

  it("exempts nothing for a chat whose blocks carry no delegate", () => {
    store.setSessions([
      session("c1", [
        { id: "m1", role: "assistant", ts: 1, content: "", blocks: [{ type: "text", text: "x" }] },
      ] as Message[]),
    ]);
    mockHasTab.mockReturnValue(true); // even with tabs open, no subtask to ask about
    expect(subagentTabProjectsChat("c1")).toBe(false);
    expect(mockHasTab).not.toHaveBeenCalled();
  });

  it("answers false for an unknown chat", () => {
    store.setSessions([]);
    expect(subagentTabProjectsChat("c-missing")).toBe(false);
  });

  it("does not cross chats: the ref carries the asking chat's id", () => {
    store.setSessions([
      session("c1", [delegateMsg("m1", "st-1")]),
      session("c2", [delegateMsg("m2", "st-1")]),
    ]);
    // A tab open for c2's delegate must not exempt c1, subtask id collision or
    // not — the ref is chat-scoped.
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c2/st-1");
    expect(subagentTabProjectsChat("c1")).toBe(false);
    expect(subagentTabProjectsChat("c2")).toBe(true);
  });
});

// The page's ONE structural input. A text delta writes a per-block signal instead of
// bumping the chat's version, and this page reads those signals with `get` rather
// than `ensure` so it never silences the transcript's own repaint — which leaves it
// depending on the store's signal-absent fallback to bump the version for it. That
// fallback asks whether the block is MOUNTED, and this page files its sinks under a
// synthetic id over a re-indexed slice, so the question is a union over surfaces.
describe("a delegate's prose while the transcript holds no sink", () => {
  it("repaints on a delta to a block only this page has mounted", async () => {
    const chat = "c-stream";
    const msgID = "m-stream";
    store.setSessions([session(chat, [delegateMsg(msgID, "st-1")])]);
    const host = document.getElementById("subagent-body") as HTMLElement;

    show(chat, "st-1");
    await vi.waitFor(() => {
      expect(host.textContent).toContain("delegate work");
    });

    // The three premises that let the assertion below fail. No transcript view was
    // ever built here, so nothing files a sink under the store's own message id —
    // the background-chat shape. No per-block signal exists, so the page's own
    // subscription cannot carry the delta and the version bump is its only input.
    // And the page holds the delegate's block ALONE, so its own index for it is 0
    // where the store's is 1: a probe asking this render the store's index answers
    // "not mounted" over a block that is on screen.
    expect(mountedWindow(msgID)).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 1))).toBeUndefined();
    expect(host.textContent).not.toContain("parent prose");

    // Ends on a paragraph break, so the incremental markdown parser has no
    // trailing token to hold and the whole delta is on screen or none of it is.
    store.appendChunk(chat, msgID, " and then more\n\n", false, 1, "st-1");

    await vi.waitFor(() => {
      expect(host.textContent).toContain("and then more");
    });
  });
});

// A switch drops the previous delegate's page. It has to drop that page's RENDER with
// it, or the render outlives its DOM with its text sinks intact and the repaint gate
// keeps answering "still mounted" for blocks nobody can see — one full transcript pass
// per delta, which is the exact cost the gate exists to remove.
describe("switching delegates releases the page's render", () => {
  it("stops answering the repaint gate for the delegate the reader left", async () => {
    const chat = "c-switch";
    const msgID = "m-switch";
    store.setSessions([
      session(chat, [
        {
          id: msgID,
          role: "assistant",
          ts: 1,
          content: "",
          blocks: [
            { type: "text", text: "parent prose" },
            { type: "text", text: "first delegate", agent_subtask_id: "sw-A" },
            { type: "text", text: "second delegate", agent_subtask_id: "sw-B" },
          ],
        } as Message,
      ]),
    ]);
    const host = document.getElementById("subagent-body") as HTMLElement;

    // BOTH tabs stay open across the switch, because that is what a tab switch is —
    // and it is what keeps this a SUPERSEDE test. With only the incoming tab open the
    // demand effect would drop the outgoing page first, and the assertions below would
    // pass through a different mechanism than the one they name.
    setSubagentTabs([subagentRef(chat, "sw-A"), subagentRef(chat, "sw-B")]);
    showSubagent(chat, "sw-A");
    await vi.waitFor(() => {
      expect(host.textContent).toContain("first delegate");
    });

    // The SWITCH is the property. One delegate cannot express it: the release only asks
    // for the wrong pair where the page's own key and the visible subtask disagree,
    // which is true for exactly one paint after a switch.
    showSubagent(chat, "sw-B");
    await vi.waitFor(() => {
      expect(host.textContent).toContain("second delegate");
    });
    expect(host.textContent).not.toContain("first delegate");

    // Two premises, or the assertion below passes for someone else's reason. No
    // transcript view exists here, so nothing files a sink under the store's own message
    // id; and A's block has no per-block signal, so the gate is the only thing left that
    // can schedule a pass for it.
    expect(mountedWindow(msgID)).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 1))).toBeUndefined();

    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    store.appendChunk(chat, msgID, " more from A", false, 1, "sw-A");
    await Promise.resolve();

    expect(version.peek()).toBe(before);
  });
});

// The left-hand list draws every stage of the pipeline, so every row is a selectable
// node — and the page used to project only the member its TAB named, which left each
// sibling's transcript host permanently empty and answered the selection with a note
// saying to open that stage's own page. These cases pin the navigation that replaced
// it: a click renders the clicked stage IN PLACE.
describe("selecting a sibling stage in the tree", () => {
  it("renders that stage's transcript in the same sub-tab", async () => {
    const chat = "c-nav";
    store.setSessions([pipelineSession(chat, "m-nav")]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });

    clickRow(REVIEW);

    // Both hosts stay in the DOM — a delegate's blocks are persisted but a mounted
    // render is not free to rebuild — so which one is SHOWN is the observable.
    const review = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`);
    const plan = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${PLAN}"]`);
    expect(review?.hidden).toBe(false);
    expect(review?.textContent).toContain("the review stage report");
    expect(plan?.hidden).toBe(true);
    // No note at all, and in particular not the retired "this stage has its own page"
    // dead end: the transcript is here.
    expect(emptyNoteText()).not.toContain("own page");
    expect(body().querySelector<HTMLElement>(".ev-d-empty")?.hidden).toBe(true);
  });

  it("keeps the delegate the tab names mounted, so going back costs no rebuild", async () => {
    const chat = "c-back";
    store.setSessions([pipelineSession(chat, "m-back")]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    clickRow(PLAN);

    const plan = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${PLAN}"]`);
    expect(plan?.hidden).toBe(false);
    expect(plan?.textContent).toContain("the plan stage report");
    expect(body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`)?.hidden).toBe(
      true,
    );
  });

  // A sibling that has produced nothing yet is the one case the note is still FOR, and
  // its two sentences are the whole vocabulary: the projection covers the stage either
  // way, so the note answers "nothing yet" rather than "not here".
  it("says what an empty sibling is doing rather than sending the reader away", async () => {
    for (const [status, sentence] of [
      ["completed", "This delegate finished without producing any transcript."],
      ["in_progress", "Waiting for this delegate to produce output\u2026"],
    ] as const) {
      const chat = `c-empty-${status}`;
      store.setSessions([
        pipelineSession(chat, `m-empty-${status}`, { reviewText: "", reviewStatus: status }),
      ]);

      show(chat, PLAN);
      await vi.waitFor(() => {
        expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
      });
      clickRow(REVIEW);

      expect(emptyNoteText()).toBe(sentence);
      // Nothing is mounted for it, which is what leaves the note on screen.
      expect(body().querySelector(`.ev-d-body[data-path="${REVIEW}"]`)).toBeNull();
    }
  });

  // The routing property: a delta lands in the render of the member whose block it
  // names. The fixture's stages are SETTLED deliberately — `text-bubble.ts` builds a
  // reveal cursor only for a LIVE bubble, and that cursor is rAF-driven, so asserting
  // rendered text over one measures `reveal.ts`'s cadence under load rather than this
  // page's routing (measured: it times out cold under full-suite load, and passes warm).
  // The cadence has its own suite; what this needs is a synchronous write.
  it("applies a delta to the selected sibling's own render", async () => {
    const chat = "c-nav-stream";
    const msgID = "m-nav-stream";
    store.setSessions([pipelineSession(chat, msgID)]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    const review = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`);
    expect(review?.textContent).toContain("the review stage report");

    // The review stage's text block is index 5 of the message. Ends on a paragraph
    // break, so the markdown parser has no trailing token to hold.
    store.appendChunk(chat, msgID, " and one more finding\n\n", false, 5, REVIEW);
    // One microtask: the store coalesces per-delta bumps onto it, and the paint it
    // drives writes synchronously from there.
    await Promise.resolve();

    expect(review?.textContent).toContain("and one more finding");
    // And into the sibling's render, not the tab's own.
    expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)?.textContent).not.toContain(
      "and one more finding",
    );
  });

  // Every MOUNTED stage is brought up to date, not only the one on screen. A mounted
  // body is hidden rather than removed, so refreshing just the shown one would leave a
  // stage the reader visited frozen at the moment they left it — and going back would
  // show a stale snapshot until the next rebuild.
  it("keeps a mounted-but-hidden stage up to date", async () => {
    const chat = "c-bg";
    const msgID = "m-bg";
    store.setSessions([pipelineSession(chat, msgID)]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    // Mount the review stage, then leave it: it stays in the DOM, hidden.
    clickRow(REVIEW);
    clickRow(PLAN);
    const review = body().querySelector<HTMLElement>(`.ev-d-body[data-path="${REVIEW}"]`);
    expect(review?.hidden).toBe(true);

    store.appendChunk(chat, msgID, " a late finding\n\n", false, 5, REVIEW);
    await Promise.resolve();

    expect(review?.textContent).toContain("a late finding");
  });

  // The release property, extended to the multi-body map: a switch to another chat
  // disposes EVERY member's render, not just the one the old tab named. A render left
  // registered keeps answering the store's repaint gate for DOM that is gone.
  it("releases every mounted stage when the reader switches chat", async () => {
    const chat = "c-nav-drop";
    const msgID = "m-nav-drop";
    store.setSessions([
      pipelineSession(chat, msgID),
      session("c-other", [delegateMsg("m-other", "st-other")]),
    ]);

    // Both tabs open across the switch, for the reason the delegate-switch case above
    // states: leaving only the incoming tab open would have the demand effect drop the
    // outgoing page, which is not the release this case is about.
    setSubagentTabs([subagentRef(chat, PLAN), subagentRef("c-other", "st-other")]);
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    expect(body().querySelector(`.ev-d-body[data-path="${REVIEW}"]`)).not.toBeNull();

    showSubagent("c-other", "st-other");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("delegate work");
    });

    // Two premises, or the assertion below passes for someone else's reason: no
    // transcript view exists here, so nothing files a sink under the store's own
    // message id, and neither stage's block has a per-block signal, so the mounted-block
    // gate is the only thing left that could schedule a pass for one.
    expect(mountedWindow(msgID)).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 5))).toBeUndefined();

    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    store.appendChunk(chat, msgID, " more review", false, 5, REVIEW);
    await Promise.resolve();

    expect(version.peek()).toBe(before);
  });
});

// DEMAND IS AN INPUT. The page's lifetime used to end only when another subagent tab
// mounted over it, so closing the last one left the whole page and one detached render
// per member the reader had opened registered in `messages-blocks.ts` — where the
// repaint gate answers as a UNION over every registered render, so each of them kept
// claiming "still mounted" for DOM that is gone and bought a full transcript pass per
// delta. A second effect over the open-tab set closes it, and the membership test is
// what makes the shared-group case structural rather than a special case.
describe("demand for the mounted page", () => {
  /** The two premises every gate assertion below needs, or it passes for someone
   *  else's reason: no transcript view exists in this file, so nothing files a sink
   *  under the store's own message id, and neither stage's text block has a per-block
   *  signal, so the mounted-block gate is the only thing left that could schedule a
   *  pass for one. */
  function gateIsTheOnlyInput(msgID: string): void {
    expect(mountedWindow(msgID)).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 3))).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 5))).toBeUndefined();
  }

  /** Block 3 is PLAN's own prose and block 5 is REVIEW's, so this asks the store about
   *  BOTH members of the pipeline: a release that reached one render and not the other
   *  is exactly the shape a hand-written reset produces. */
  async function gateIsSilentForBothStages(chat: string, msgID: string): Promise<void> {
    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    store.appendChunk(chat, msgID, " more plan", false, 3, PLAN);
    store.appendChunk(chat, msgID, " more review", false, 5, REVIEW);
    await Promise.resolve();
    expect(version.peek()).toBe(before);
  }

  it("drops the page and every body it mounted when the last subagent tab closes", async () => {
    const chat = "c-close";
    const msgID = "m-close";
    store.setSessions([pipelineSession(chat, msgID)]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    // TWO bodies mounted, which is what makes "every body" a claim with content.
    clickRow(REVIEW);
    expect(body().querySelector(`.ev-d-body[data-path="${REVIEW}"]`)).not.toBeNull();

    closeSubagentTabs();

    // The page LEFT the host, so "no page mounted" and "the host holds no page" agree.
    expect(body().querySelector(".ev-page")).toBeNull();
    gateIsTheOnlyInput(msgID);
    await gateIsSilentForBothStages(chat, msgID);
  });

  // The case the retired deferral named as its blocker: two stage tabs of one pipeline
  // share ONE page, so a per-tab close handler would have to know whether any sibling
  // still wants it. The membership test answers that structurally — a `Map.has` against
  // the page's own projection — so nothing has to be tracked per tab.
  it("keeps the page when one of two stage tabs sharing its group closes", async () => {
    const chat = "c-sibling";
    const msgID = "m-sibling";
    store.setSessions([pipelineSession(chat, msgID)]);

    setSubagentTabs([subagentRef(chat, PLAN), subagentRef(chat, REVIEW)]);
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    const page = body().querySelector(".ev-page");
    expect(page).not.toBeNull();

    // PLAN's tab closes; REVIEW's stays open, and it names a member of the same group.
    setSubagentTabs([subagentRef(chat, REVIEW)]);

    // The SAME element, not a rebuilt one: the page was never dropped, so the bodies
    // mounted into it are still the reader's.
    expect(body().querySelector(".ev-page")).toBe(page);
    // And the gate still answers for a member whose OWN tab is the one that closed:
    // the page is the unit of demand, not the tab.
    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    store.appendChunk(chat, msgID, " still here\n\n", false, 3, PLAN);
    await vi.waitFor(() => {
      expect(version.peek()).not.toBe(before);
    });
  });

  // The chat compare in the membership test, and the id compare, each on their own. A
  // subtask id is only unique WITHIN a chat, which `subagentTabProjectsChat`'s own
  // cases already pin from the other side.
  it.each([
    {
      what: "a tab for the same subtask id in a different chat",
      chat: "c-outside-chat",
      msgID: "m-outside-chat",
      ref: subagentRef("c-elsewhere", PLAN),
    },
    {
      what: "a tab for a non-member subtask of the same chat",
      chat: "c-outside-member",
      msgID: "m-outside-member",
      ref: subagentRef("c-outside-member", "st-not-a-member"),
    },
  ])("does not count $what as demand", async ({ chat, msgID, ref }) => {
    store.setSessions([pipelineSession(chat, msgID)]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);

    setSubagentTabs([ref]);

    expect(body().querySelector(".ev-page")).toBeNull();
    gateIsTheOnlyInput(msgID);
    await gateIsSilentForBothStages(chat, msgID);
  });

  // Superseding is still the OTHER release path, and demand holding is not a keep rule:
  // the outgoing group's tabs are both still open here, and its page goes anyway. The
  // tab set does not move across the switch, so the demand effect provably does not run
  // and `mountPage`'s own release is what is under test.
  it("still supersedes: mounting another group's page releases the previous one", async () => {
    const chat = "c-supersede";
    const msgID = "m-supersede";
    store.setSessions([
      pipelineSession(chat, msgID),
      session("c-super-other", [delegateMsg("m-super-other", "st-other")]),
    ]);

    setSubagentTabs([
      subagentRef(chat, PLAN),
      subagentRef(chat, REVIEW),
      subagentRef("c-super-other", "st-other"),
    ]);
    showSubagent(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    const page = body().querySelector(".ev-page");

    showSubagent("c-super-other", "st-other");
    await vi.waitFor(() => {
      expect(body().textContent).toContain("delegate work");
    });

    expect(body().querySelector(".ev-page")).not.toBe(page);
    gateIsTheOnlyInput(msgID);
    await gateIsSilentForBothStages(chat, msgID);
  });

  // Why the drop writes `shown` rather than calling the release directly. The paint
  // effect's dependencies are `shown` and the launching chat's version, so a drop that
  // cleared only the mounted page leaves `shown` naming the closed delegate — and that
  // chat's next transcript delta re-runs the paint effect, re-projects, and mounts the
  // page again for a tab that no longer exists. The demand effect cannot notice,
  // because the tab set did not move.
  it("does not re-mount the page on the launching chat's next transcript delta", async () => {
    const chat = "c-resurrect";
    const msgID = "m-resurrect";
    store.setSessions([pipelineSession(chat, msgID)]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });
    clickRow(REVIEW);
    closeSubagentTabs();
    expect(body().querySelector(".ev-page")).toBeNull();

    // A REAL transcript event for the launching chat, and its bump is asserted as the
    // PREMISE: without it this test cannot fail.
    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    store.upsertToolCall(
      chat,
      "m-resurrect-later",
      { id: "tc-later", title: "Read File", kind: "read", status: "completed", ts: 2 },
      0,
    );
    expect(version.peek()).not.toBe(before);

    expect(body().querySelector(".ev-page")).toBeNull();
    gateIsTheOnlyInput(msgID);
    await gateIsSilentForBothStages(chat, msgID);
  });

  // Why demand is a SECOND effect rather than a read added to the paint effect: the
  // paint effect re-projects the group and repaints the page, so taking the tab set as
  // one of its dependencies would do that on every tab open, close, pin and reorder
  // anywhere in the app.
  it("does not re-project the group on a tab-set change that leaves the demand alone", async () => {
    const chat = "c-churn";
    const msgID = "m-churn";
    store.setSessions([pipelineSession(chat, msgID)]);

    show(chat, PLAN);
    await vi.waitFor(() => {
      expect(body().querySelector(`.ev-d-body[data-path="${PLAN}"]`)).not.toBeNull();
    });

    const projections = vi.mocked(sliceSubagentGroup).mock.calls.length;
    const page = body().querySelector(".ev-page");
    expect(page).not.toBeNull();

    // A new tab-set version carrying the SAME demand: the strip moved (a pin, a
    // reorder, a tab of another kind opening), this page's group did not.
    setSubagentTabs([subagentRef(chat, PLAN)]);

    expect(vi.mocked(sliceSubagentGroup).mock.calls.length).toBe(projections);
    expect(body().querySelector(".ev-page")).toBe(page);
  });
});

// ---------------------------------------------------------------------------
// The subagent kind's refresh. The page is a projection of the launching chat's
// blocks, so the chat's window is the only thing that can make it current.
// ---------------------------------------------------------------------------

describe("refreshSubagent delegates to the launching chat", () => {
  it("refreshes the launching chat and nothing else", () => {
    vi.mocked(refreshChatView).mockImplementation(() => undefined);

    refreshSubagent("c-launcher", "task-9");

    expect(refreshChatView).toHaveBeenCalledTimes(1);
    expect(refreshChatView).toHaveBeenCalledWith("c-launcher");
    expect(vi.mocked(sliceSubagentGroup)).not.toHaveBeenCalled();
  });
});
