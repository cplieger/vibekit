// A FAILED TURN STATES ITS REASON EXACTLY ONCE, IN ONE CARD, IN BOTH FOLD STATES.
//
// This is the composition half of the ownership rule, and it is the half no
// existing suite reaches. Four surfaces of one card could each carry the server's
// prose, and each is pinned somewhere on its own:
//
//   - the body's `.boundary` divider  -> messages-events.ts's `interrupted` entry
//     carries NO labelFn, pinned from the BUILDER's side in messages-events.test.ts
//   - the card-level `.turn-notice`   -> `turnFailureText`, pinned in turns.node.test.ts
//   - the folded card's `.turn-face`  -> `syncTurnFace` deliberately mounts none,
//     pinned by a COMMENT at messages.ts and by nothing else
//   - the footer's outcome lead       -> `OUTCOME_LEAD`, a fixed word per outcome
//
// Four fixtures, four suites, and nothing asserting the SUM. So a labelFn added
// back to the divider, or a reason re-added to the face, puts the sentence on
// screen twice with every existing test still green — which is exactly the defect
// `messages-events.ts` records as measured on a live chat, about 50px apart.
//
// The FOLDED case is the one with no other guard at all: the face is the surface a
// broken turn's reader sees after collapsing it, and `syncTurnFace`'s "No failure
// text here" is a comment rather than a test.
//
// TWO TURNS IN EVERY FIXTURE, and that is a requirement rather than realism:
// `isTurnOpen` returns true for the newest turn UNCONDITIONALLY, above the
// overrides, so a one-turn chat cannot be folded at all and a folded case built on
// one would assert against an open card while reading as though it had folded.
//
// Counted over the rendered card's own text, through the REAL paint, because the
// property is about what one card shows rather than about what any one builder
// returns.
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

// scroll.ts is a self-initialising singleton over a real scroller; the canonical
// mock is what every other suite in this graph uses.
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

// The graph's network edge. The rail's session-wide turn index is the one GET a
// paint issues, and it must not reach a real fetch.
vi.mock("./api-client.js", async () => ({
  ...(await vi.importActual<Record<string, unknown>>("./api-client.js")),
  apiGet: vi.fn(() => Promise.resolve(null)),
}));

const { mountChatView, activeTranscriptView } = await import("./messages.js");
const { setSessions, setActive } = await import("./store.js");
const { setTurnOpen, resetFoldState } = await import("./fold-state.js");

mountChatView();

/** The server's own sentence. Deliberately unlike anything else the card renders:
 *  the header carries the prompt, the footer carries a fixed outcome word, so a
 *  count over the card's text can only be counting this. */
const REASON = "ACP bridge exited";

/** The broken turn's opening message id, which IS its turn id — the key
 *  `setTurnOpen` records an override under. */
const BROKEN_TURN = "u1";

interface Msg {
  id: string;
  role: string;
  ts: number;
  content?: string;
  blocks?: unknown[];
  turn_outcome?: string;
  event_kind?: string;
}

/** A turn that broke, followed by one that did not. The first turn is the subject;
 *  the second exists so the first is not the newest and can therefore fold.
 *
 *  The broken turn's shape is the PERSISTED one: the prompt, a reply, the
 *  `interrupted` event row carrying the reason, and the outcome stamped on the
 *  carrier — `turnFailureText`'s first source is that event row's own `content`. */
function brokenThenClean(): Msg[] {
  return [
    { id: BROKEN_TURN, role: "user", ts: 1, content: "run the build" },
    {
      id: "a1",
      role: "assistant",
      ts: 2,
      content: "Building.",
      blocks: [{ type: "text", text: "Building." }],
      turn_outcome: "interrupted",
    },
    { id: "e1", role: "event", ts: 3, event_kind: "interrupted", content: REASON },
    { id: "u2", role: "user", ts: 4, content: "try again" },
    {
      id: "a2",
      role: "assistant",
      ts: 5,
      content: "Built.",
      blocks: [{ type: "text", text: "Built." }],
      turn_outcome: "completed",
    },
  ];
}

/** The same pair with the first turn ended cleanly. The NEGATIVE CONTROL: without
 *  it every count assertion below passes just as well for a card that renders no
 *  reason at all. */
function twoCleanTurns(): Msg[] {
  return brokenThenClean()
    .filter((m) => m.event_kind === undefined)
    .map((m) => (m.id === "a1" ? { ...m, turn_outcome: "completed" } : m));
}

/** Paint `msgs` as `chatID` and return that chat's turn cards, in order. */
function mount(chatID: string, msgs: Msg[]): HTMLElement[] {
  setSessions([
    {
      id: chatID,
      name: chatID,
      messages: msgs,
      message_count: msgs.length,
      has_more: false,
      thinking: false,
      working_label: "",
    } as never,
  ]);
  setActive(chatID);
  const root = activeTranscriptView();
  const cards = [...(root?.querySelectorAll<HTMLElement>(".turn") ?? [])];
  expect(cards.length, "both turns painted").toBe(2);
  return cards;
}

/** How many times `needle` occurs in `haystack`. `indexOf` rather than a regex,
 *  because the needle is upstream prose and may carry regex metacharacters. */
function occurrences(haystack: string, needle: string): number {
  let n = 0;
  for (let i = haystack.indexOf(needle); i !== -1; i = haystack.indexOf(needle, i + 1)) {
    n += 1;
  }
  return n;
}

describe("a failed turn's reason", () => {
  beforeEach(() => {
    resetFoldState();
  });

  it("appears exactly once on an OPEN card, and not on the divider", () => {
    const chat = "open-broken";
    setTurnOpen(chat, BROKEN_TURN, true);
    const [card] = mount(chat, brokenThenClean());
    if (card === undefined) {
      throw new Error("no card");
    }

    // The premise: this case is about an OPEN card, so it must be one.
    expect(card.hasAttribute("data-folded"), "the subject card is open").toBe(false);
    expect(occurrences(card.textContent ?? "", REASON), "one card, one sentence").toBe(1);

    // WHERE it is, so the count cannot be satisfied by the wrong surface.
    const notice = card.querySelector<HTMLElement>(":scope > .turn-notice");
    expect(notice, "the notice is the surface that carries it").not.toBeNull();
    expect(notice?.textContent).toBe(REASON);

    // And the divider is present, marking the boundary, WITHOUT the prose. Its
    // presence is what makes the absence meaningful: a card with no divider would
    // pass this trivially.
    const divider = card.querySelector<HTMLElement>(".turn-body .boundary");
    expect(divider, "the boundary divider is rendered").not.toBeNull();
    expect(divider?.textContent ?? "").toContain("Turn interrupted");
    expect(divider?.textContent ?? "", "the divider names the kind, not the reason").not.toContain(
      REASON,
    );
  });

  it("appears exactly once on a FOLDED card, where the face could repeat it", () => {
    const chat = "folded-broken";
    setTurnOpen(chat, BROKEN_TURN, false);
    const [card] = mount(chat, brokenThenClean());
    if (card === undefined) {
      throw new Error("no card");
    }

    // The premise, and the reason this fixture carries a second turn: an override
    // is the only thing that folds a BROKEN turn (the policy never auto-folds one),
    // and no override can fold the newest.
    expect(card.hasAttribute("data-folded"), "the subject card is folded").toBe(true);
    expect(occurrences(card.textContent ?? "", REASON), "one card, one sentence").toBe(1);
    expect(
      card.querySelector<HTMLElement>(":scope > .turn-notice"),
      "the notice survives the fold — that is why it owns the prose",
    ).not.toBeNull();

    const face = card.querySelector<HTMLElement>(":scope > .turn-face");
    if (face !== null) {
      expect(face.textContent ?? "", "the face renders the answer, never the reason").not.toContain(
        REASON,
      );
    }
  });

  it("renders no notice at all for a turn that ended cleanly", () => {
    // The negative control for both cases above.
    const chat = "clean";
    setTurnOpen(chat, BROKEN_TURN, true);
    const [card] = mount(chat, twoCleanTurns());
    if (card === undefined) {
      throw new Error("no card");
    }
    expect(card.querySelector(":scope > .turn-notice")).toBeNull();
    expect(occurrences(card.textContent ?? "", REASON)).toBe(0);
  });
});
