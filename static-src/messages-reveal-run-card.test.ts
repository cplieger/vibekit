// ---------------------------------------------------------------------------
// `revealRunCard`: what the run sub-tab's "Open the conversation" link chains onto
// once the chat's tab is open.
//
// It is BEST-EFFORT by design, and the three cases below are the three answers it
// can honestly give: the card is mounted (scroll to it, true), the card's turn is
// resident but folded to a stub so no card exists (false — the reader is in the right
// conversation and nothing more was claimed), or this chat has no resident view at
// all (false, and nothing touched).
//
// It drives the REAL transcript rather than mocking it, because the thing under test
// is which card in which view — a mocked renderer would let a document-wide lookup
// pass. `turn-residency.test.ts` is the harness precedent.
// ---------------------------------------------------------------------------

import { describe, it, expect, vi, beforeEach } from "vitest";

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

// scroll.ts is a self-initialising singleton over a real scroller; the canonical mock
// is what every suite in this graph uses. `jumpTo` is a spy on it, which is the
// observable for "did it scroll to the card" — the module owns both halves of that
// decision (park the reader, decide whether the jump leaves the live edge), so
// asserting the CALL rather than a scrollTop is asserting the contract.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The graph's network edge, real except for the two GETs this graph makes on paint: a
// run's state and the rail's session-wide turn index. Both answer nothing, which is
// what a card with no fetched state renders its own loading row for.
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { mountChatView, revealRunCard, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive, bumpMessages, removeChat } = await import("./store.js");
const { setTurnOpen, _resetFoldStateForTest, TURNS_WARM } = await import("./fold-state.js");
const { resetTurnRail } = await import("./turn-rail.js");
const { scrollMock } = await import("./__test-helpers__/scroll-mock.js");

interface Msg {
  id: string;
  role: string;
  ts: number;
  content?: string;
  blocks?: unknown[];
  tool_calls?: unknown[];
}

function user(id: string): Msg {
  return { id, role: "user", ts: 1, content: `prompt ${id}` };
}

/** An assistant turn that LAUNCHED a run: `ToolCall.workflow_id` is what the
 *  dispatcher keys the run card on. */
function launcher(id: string, workflowID: string): Msg {
  const tc = `tc-${workflowID}`;
  return {
    id,
    role: "assistant",
    ts: 2,
    content: "",
    blocks: [{ type: "tool_use", tool_call_id: tc }],
    tool_calls: [
      {
        id: tc,
        title: "Run Workflow",
        kind: "other",
        status: "completed",
        workflow_id: workflowID,
      },
    ],
  };
}

function plain(id: string): Msg[] {
  return [user(`u${id}`), { id: `a${id}`, role: "assistant", ts: 2, content: `reply ${id}` }];
}

function activate(chatID: string, messages: Msg[]): void {
  setSessions([
    {
      id: chatID,
      name: "c",
      model: "",
      acp_session_id: "",
      current_mode_id: "",
      available_modes: [],
      available_models: [],
      usage: { context_size: 0 },
      message_count: messages.length,
      messages,
      has_more: false,
      thinking: false,
      working_label: "Thinking",
    },
  ] as never);
  setActive(chatID);
  // A same-id re-activation writes no signal the paint effect tracks; the bump is
  // what store-load.ts issues after every page mutation.
  bumpMessages(chatID);
}

let seq = 0;
/** A fresh chat id per case, so fold overrides and resident views cannot bleed. */
function chatID(): string {
  seq++;
  return `c-reveal-${String(seq)}`;
}

beforeEach(() => {
  mountChatView();
  localStorage.clear();
  _resetFoldStateForTest();
  resetTurnRail();
  scrollMock.jumpTo.mockClear();
  setSessions([]);
});

describe("revealRunCard", () => {
  it("scrolls to a mounted run card and says so", () => {
    const c = chatID();
    activate(c, [user("u1"), launcher("a1", "wf_1")]);
    const view = activeTranscriptView();
    const card = view?.querySelector<HTMLElement>('.run-card[data-run="wf_1"]');
    // The premise, or this case asserts nothing about scoping: the card is really
    // mounted inside THIS chat's view.
    expect(card).not.toBeNull();

    expect(revealRunCard(c, "wf_1")).toBe(true);
    expect(scrollMock.jumpTo).toHaveBeenCalledTimes(1);
    expect(scrollMock.jumpTo.mock.calls[0]?.[0]).toBe(card);
  });

  // The renderer fact this function rests on, and the reason it does not also unfold:
  // a turn past TURNS_WARM is a header/footer STUB with no `.turn-body`, so it holds
  // no run card at all and this answers false. The collapsed FACE used to mount a
  // duplicate card, which is what made a stub answer true; that duplicate is gone —
  // the composer band's run bar is the persistent surface for a live run now — and
  // the honest answer is that the reader is in the right conversation and the card is
  // one unfold away.
  it("answers false for a launching turn that folded to a stub", () => {
    const c = chatID();
    const messages: Msg[] = [user("u0"), launcher("a0", "wf_old")];
    // Push the launching turn well past the warm window.
    for (let i = 1; i <= TURNS_WARM + 2; i++) {
      messages.push(...plain(String(i)));
    }
    activate(c, messages);
    const view = activeTranscriptView();
    // The premise: the turn really is a stub, which is what takes its card with it.
    expect(view?.querySelector('[data-key="u0"] > .turn-body')).toBeNull();
    expect(view?.querySelector('.run-card[data-run="wf_old"]')).toBeNull();

    expect(revealRunCard(c, "wf_old")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // The honest false case, and the one item 6's state-3 copy is written against: the
  // window is PAGINATED, so a run launched far enough back is not resident and no
  // client-side unfold can reach it. Serving a step's transcript from KAS on demand is
  // the answer to that whole family (followups FU-1).
  it("answers false when the launching turn has been paged out", () => {
    const c = chatID();
    // A resident view whose window simply does not hold the launcher.
    activate(c, [...plain("1"), ...plain("2")]);
    expect(activeTranscriptView()).not.toBeNull();

    expect(revealRunCard(c, "wf_old")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // THE SCOPING CASE. `.run-card[data-run]` repeats once per RESIDENT view and
  // `document.querySelector` answers in document order, which can be a PARKED view's
  // card — so a document-wide lookup would scroll a reader to another conversation's
  // card and claim it landed.
  it("answers false, and touches nothing, for a chat with no resident view", () => {
    const c = chatID();
    activate(c, [user("u1"), launcher("a1", "wf_1")]);
    // The card really is in the DOCUMENT, under the chat that owns it.
    expect(document.querySelector('.run-card[data-run="wf_1"]')).not.toBeNull();

    expect(revealRunCard("c-never-opened", "wf_1")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // A run this conversation never launched has no turn to open either, so the walk
  // finds nothing and nothing is disturbed.
  it("answers false for a run this chat did not launch", () => {
    const c = chatID();
    activate(c, [user("u1"), launcher("a1", "wf_1")]);
    expect(revealRunCard(c, "wf_other")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  it("answers false for an empty run id", () => {
    const c = chatID();
    activate(c, [user("u1"), launcher("a1", "wf_1")]);
    expect(revealRunCard(c, "")).toBe(false);
    expect(scrollMock.jumpTo).not.toHaveBeenCalled();
  });

  // A chat whose window has been dropped keeps its view for a moment (the
  // multiplexer parks up to PARKED_VIEWS), so the store read is what stops the walk
  // rather than throwing on an absent session.
  it("answers false when the chat's window is gone", () => {
    const c = chatID();
    activate(c, [user("u1"), launcher("a1", "wf_1")]);
    setTurnOpen(c, "u1", false);
    removeChat(c);
    expect(revealRunCard(c, "wf_1")).toBe(false);
  });
});
