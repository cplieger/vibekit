// The server pre-pass that makes the DOM search honest: which URL it asks for,
// and what it does with the answer.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { Hit, SearchResult } from "./wire/types.gen.js";

const apiGetTyped = vi.fn<(url: string, decode: (v: unknown) => unknown) => Promise<unknown>>();
const openForSearch = vi.fn();
// The chat id is declared because fold-state.ts's clearSearchOpened takes one and
// the wrapper below forwards it; a nullary mock types its own call log as empty.
const clearSearchOpened = vi.fn((_chatID: string) => true);
// Both arguments captured: the reveal and the re-fold declare their render
// cause explicitly (`shape`), and the assertion needs to see it.
const bumpMessages = vi.fn((_chatID: string, _cause?: string) => undefined);

vi.mock("./api-client.js", () => ({
  apiGetTyped: (url: string, decode: (v: unknown) => unknown) => apiGetTyped(url, decode),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and this name is imported somewhere in it. No case here calls it.
  apiGet: vi.fn(),
}));
vi.mock("./fold-state.js", () => ({
  openForSearch: (chatID: string, id: string) => openForSearch(chatID, id),
  clearSearchOpened: (chatID: string) => clearSearchOpened(chatID),
}));
vi.mock("./store.js", () => ({
  bumpMessages: (id: string, cause?: string) => bumpMessages(id, cause),
}));

const {
  runServerSearch,
  resetServerSearch,
  revealHitTurn,
  searchHitTurns,
  searchHitCount,
  initSearchRevealBuilder,
} = await import("./chat-search.js");

function hit(over: Partial<Hit> = {}): Hit {
  return {
    message_id: "a1",
    turn_message_id: "u1",
    excerpt: "…retry…",
    role: "assistant",
    segment_kind: "content",
    turn: 2,
    offset: 0,
    segment_len: 24,
    ...over,
  };
}

/** A server reply carrying these hits. `matched` defaults to the list's own
 *  length, which is the uncut answer; a case staging a cut sets it higher. */
function reply(hits: Hit[], over: Partial<SearchResult> = {}): SearchResult {
  return { matches: hits, scanned: 4, matched: hits.length, truncated: false, ...over };
}

/** Stage what the fetch answers. A body goes through the caller's OWN decoder,
 *  so a fixture drifting from the wire shape fails here rather than passing on a
 *  cast; `null` is the failed fetch. */
function answer(body: SearchResult | null): void {
  apiGetTyped.mockImplementation((_url, decode) =>
    Promise.resolve(body === null ? null : decode(body)),
  );
}

/** The three injected surfaces, declared once because they are MODULE state in
 *  chat-search.ts: a case that armed its own would leak it into the next. The
 *  suite's `mockReset` restores each implementation before every test. */
const reveal = vi.fn((_chatID: string, _turnID: string, _messageID: string, _blockIndex?: number) =>
  Promise.resolve(),
);
const forWalk = vi.fn((_chatID: string, _turnID: string) => Promise.resolve());
const endWalk = vi.fn((_chatID: string) => undefined);

beforeEach(() => {
  apiGetTyped.mockReset();
  answer(reply([]));
  initSearchRevealBuilder(reveal, forWalk, endWalk);
  resetServerSearch();
  endWalk.mockClear();
});

describe("runServerSearch: the request", () => {
  it("asks the chat's own search endpoint with the encoded query", async () => {
    await runServerSearch("c 1", "why retry?");
    expect(apiGetTyped).toHaveBeenCalledWith(
      "/api/chats/c%201/search?q=why%20retry%3F",
      expect.any(Function),
    );
  });

  it("omits the case parameter by default, so an unset flag keeps the old behaviour", async () => {
    await runServerSearch("c1", "retry");
    expect(apiGetTyped.mock.calls[0]?.[0]).not.toContain("case=");
  });

  it("sends case=1 when the reader asked to match case", async () => {
    await runServerSearch("c1", "Retry", true);
    expect(apiGetTyped).toHaveBeenCalledWith(
      "/api/chats/c1/search?q=Retry&case=1",
      expect.any(Function),
    );
  });

  it("does not call the server for an empty chat id or a blank query", async () => {
    await runServerSearch("", "retry");
    await runServerSearch("c1", "   ");
    expect(apiGetTyped).not.toHaveBeenCalled();
  });
});

describe("runServerSearch: the reveal", () => {
  it("opens each hit's turn by its OPENING message id", async () => {
    answer(
      reply([hit({ turn_message_id: "u1" }), hit({ message_id: "a2", turn_message_id: "u3" })]),
    );
    await runServerSearch("c1", "retry");
    expect(openForSearch).toHaveBeenCalledWith("c1", "u1");
    expect(openForSearch).toHaveBeenCalledWith("c1", "u3");
    // The renderer has to see the reveal before the DOM walker runs, and a
    // reveal changes which turns are open and mounted: a stated shape change.
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });

  it("builds each revealed turn's body once, before the repaint", async () => {
    // Two hits inside ONE turn and one in another: the on-demand build runs per
    // TURN, not per hit — a turn's body only exists once — and every build
    // lands before the bump so the walker's re-run sees the rows.
    answer(
      reply([
        hit({ turn_message_id: "u1" }),
        hit({ message_id: "a2", turn_message_id: "u1" }),
        hit({ message_id: "a3", turn_message_id: "u3" }),
      ]),
    );
    const order: string[] = [];
    forWalk.mockImplementation((_chatID: string, turnID: string) => {
      order.push(`build:${turnID}`);
      return Promise.resolve();
    });
    bumpMessages.mockImplementation(() => {
      order.push("bump");
    });
    await runServerSearch("c1", "retry");
    expect(order).toEqual(["build:u1", "build:u3", "bump"]);
    expect(forWalk).toHaveBeenCalledWith("c1", "u1");
    expect(forWalk).toHaveBeenCalledWith("c1", "u3");
    // Through the WALK's entry point, not the navigation one: one pin slot cannot
    // serve a loop over N turns, so the loop would do N builds to keep one.
    expect(reveal).not.toHaveBeenCalled();
  });

  it("does not build for a hit the server could not resolve to a turn opener", async () => {
    answer(reply([hit({ turn_message_id: "" })]));
    await runServerSearch("c1", "retry");
    expect(forWalk).not.toHaveBeenCalled();
  });

  it("records the hit turns and their counts for the rail and the folded rows", async () => {
    answer(reply([hit({ turn: 2 }), hit({ turn: 2, message_id: "a2" }), hit({ turn: 5 })]));
    await runServerSearch("c1", "retry");
    expect([...searchHitTurns()].sort((a, b) => a - b)).toEqual([2, 5]);
    expect(searchHitCount(2)).toBe(2);
    expect(searchHitCount(5)).toBe(1);
    expect(searchHitCount(9)).toBe(0);
  });

  it("leaves the previous reveal in place when the fetch fails", async () => {
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    answer(null);
    const out = await runServerSearch("c1", "retry");
    // A failed fetch must not collapse turns out from under a reader mid-search:
    // the previous run's reveal and counts stay exactly as they were.
    expect(out).toBeNull();
    expect(searchHitCount(2)).toBe(1);
    expect([...searchHitTurns()]).toEqual([2]);
  });

  it("answers the server's envelope whole, tally included", async () => {
    // The caller's counter and note read `matched` and `scanned` off the answer it
    // adopts, so the reply travels as one value rather than a list beside two
    // accessors that could describe a different answer. A cut is what `matched`
    // exceeding the list says, and nothing here re-derives it.
    answer(reply([hit(), hit({ message_id: "a2" })], { scanned: 24, matched: 347 }));
    expect(await runServerSearch("c1", "retry")).toEqual({
      matches: [hit(), hit({ message_id: "a2" })],
      scanned: 24,
      matched: 347,
      truncated: false,
    });
  });

  it("answers null when the fetch failed, so a caller can keep what it had", async () => {
    // The distinction the caller's whole standing answer rests on: an empty
    // envelope is "this query matched nothing", and the failed request never said
    // that. One here replaced the reader's navigable set with nothing, marked it
    // owned for a query the server never answered, and left the note describing
    // the previous answer beside a zero-length list.
    answer(null);
    expect(await runServerSearch("c1", "retry")).toBeNull();
  });

  it("answers an empty envelope for an empty question, because that IS an answer", async () => {
    // No chat and a blank query are not failures: nothing was asked, so nothing was
    // read, nothing matched and nothing was cut, and a caller adopting this is
    // adopting the truth.
    const nothing = { matches: [], scanned: 0, matched: 0, truncated: false };
    expect(await runServerSearch("", "retry")).toEqual(nothing);
    expect(await runServerSearch("c1", "   ")).toEqual(nothing);
    expect(apiGetTyped).not.toHaveBeenCalled();
  });

  it("drops the reveal and the counts on reset, declaring the re-fold's shape", async () => {
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    bumpMessages.mockClear();
    resetServerSearch();
    expect([...searchHitTurns()]).toEqual([]);
    expect(searchHitCount(2)).toBe(0);
    expect(clearSearchOpened).toHaveBeenCalledWith("c1");
    // The re-fold un-mounts what the reveal pinned past the block budget: a
    // shape change, stated at the branch that knows.
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });

  it("releases the walk's grants on reset even when nothing was left to re-fold", async () => {
    // The grants outlive the loop that took them, so the reveal's END is what ends
    // them — and it must not depend on another question's answer. `clearSearchOpened`
    // returns false whenever the set is already empty (`resetFoldState`, a second
    // reset), which would leave every grant standing with no gesture left to end it.
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    clearSearchOpened.mockReturnValue(false);
    endWalk.mockClear();
    resetServerSearch();
    expect(endWalk).toHaveBeenCalledExactlyOnceWith("c1");
  });

  it("undoes the reveal in the chat the SEARCH ran in, whatever is active at close", async () => {
    // The close path runs after a tab change has already moved the ACTIVE chat, so a
    // teardown keyed on that re-folded nothing: the searched chat kept 48 granted ordinals at
    // every hit turn's head AND its search-opened turns stayed open, with no search running.
    // The function takes no chat argument now, so no caller can name the wrong one; what
    // this pins is that all THREE effects name the chat the search ran in.
    answer(reply([hit({ turn: 2 })]));
    await runServerSearch("c1", "retry");
    endWalk.mockClear();
    clearSearchOpened.mockClear();
    bumpMessages.mockClear();

    resetServerSearch();

    expect(endWalk).toHaveBeenCalledExactlyOnceWith("c1");
    expect(clearSearchOpened).toHaveBeenCalledExactlyOnceWith("c1");
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });

  it("skips a hit the server could not resolve to a turn opener", async () => {
    answer(reply([hit({ turn_message_id: "" })]));
    await runServerSearch("c1", "retry");
    expect(openForSearch).not.toHaveBeenCalled();
  });
});

describe("revealHitTurn: the per-hit reveal navigation runs before selecting", () => {
  it("opens the hit's turn, builds its body, and then declares the shape change", async () => {
    const order: string[] = [];
    openForSearch.mockImplementation((_chatID: string, id: string) => {
      order.push(`open:${id}`);
    });
    reveal.mockImplementation((_chatID: string, turnID: string) => {
      order.push(`build:${turnID}`);
      return Promise.resolve();
    });
    bumpMessages.mockImplementation(() => {
      order.push("bump");
    });
    await revealHitTurn("c1", hit({ turn_message_id: "u7" }));
    // Same ordering contract as the search-wide reveal: the body must exist
    // before the repaint that unfolds it, and the bump is the stated `shape`.
    expect(order).toEqual(["open:u7", "build:u7", "bump"]);
    expect(bumpMessages).toHaveBeenCalledWith("c1", "shape");
  });

  it("names the hit's own BLOCK, so the build can centre on it rather than the head", async () => {
    // The turn-block ordinal is a fact of the residency projection, which is on the
    // other side of this injection: what crosses is the block's identity, and the
    // consumer converts. Without it every hit in a 700-block turn builds the head and
    // the reader lands on `could not be shown`.
    await revealHitTurn("c1", hit({ turn_message_id: "u7", message_id: "a4", block_index: 311 }));
    expect(reveal).toHaveBeenCalledExactlyOnceWith("c1", "u7", "a4", 311);
  });

  it("passes no block for a hit that names none, which is the message's own row", async () => {
    // A `message`-kind hit and a legacy blockless one both resolve to the ROW, and
    // `turnOrdinalOf` answers the message's FIRST ordinal for an absent index.
    await revealHitTurn(
      "c1",
      hit({ turn_message_id: "u7", message_id: "a4", segment_kind: "message" }),
    );
    expect(reveal).toHaveBeenCalledExactlyOnceWith("c1", "u7", "a4", undefined);
  });

  it("does nothing for a hit with no turn opener", async () => {
    // The beforeEach reset already bumped once; this test asserts the CALL
    // BELOW adds nothing.
    bumpMessages.mockClear();
    await revealHitTurn("c1", hit({ turn_message_id: "" }));
    expect(openForSearch).not.toHaveBeenCalled();
    expect(reveal).not.toHaveBeenCalled();
    expect(bumpMessages).not.toHaveBeenCalled();
  });

  it("does nothing without an active chat", async () => {
    bumpMessages.mockClear();
    await revealHitTurn("", hit());
    expect(openForSearch).not.toHaveBeenCalled();
    expect(reveal).not.toHaveBeenCalled();
    expect(bumpMessages).not.toHaveBeenCalled();
  });
});
