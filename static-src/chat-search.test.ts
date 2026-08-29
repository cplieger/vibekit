// The server pre-pass that makes the DOM search honest: which URL it asks for,
// and what it does with the answer.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { SearchHit } from "./chat-search.js";

const apiGet = vi.fn<(url: string) => Promise<{ hits?: SearchHit[] } | null>>();
const openForSearch = vi.fn();
// The chat id is declared because fold-state.ts's clearSearchOpened takes one and
// the wrapper below forwards it; a nullary mock types its own call log as empty.
const clearSearchOpened = vi.fn((_chatID: string) => true);
const bumpMessages = vi.fn();

vi.mock("./api-client.js", () => ({ apiGet: (url: string) => apiGet(url) }));
vi.mock("./fold-state.js", () => ({
  openForSearch: (chatID: string, id: string) => openForSearch(chatID, id),
  clearSearchOpened: (chatID: string) => clearSearchOpened(chatID),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  apiGetTyped: vi.fn(),
}));
vi.mock("./store.js", () => ({ bumpMessages: (id: string) => bumpMessages(id) }));

const { runServerSearch, resetServerSearch, searchHitTurns, searchHitCount, searchHitTotal } =
  await import("./chat-search.js");

function hit(over: Partial<SearchHit> = {}): SearchHit {
  return {
    message_id: "a1",
    turn_message_id: "u1",
    excerpt: "…retry…",
    role: "assistant",
    turn: 2,
    offset: 0,
    ...over,
  };
}

beforeEach(() => {
  apiGet.mockReset();
  apiGet.mockResolvedValue({ hits: [] });
  resetServerSearch("c1");
});

describe("runServerSearch: the request", () => {
  it("asks the chat's own search endpoint with the encoded query", async () => {
    await runServerSearch("c 1", "why retry?");
    expect(apiGet).toHaveBeenCalledWith("/api/chats/c%201/search?q=why%20retry%3F");
  });

  it("omits the case parameter by default, so an unset flag keeps the old behaviour", async () => {
    await runServerSearch("c1", "retry");
    expect(apiGet.mock.calls[0]?.[0]).not.toContain("case=");
  });

  it("sends case=1 when the reader asked to match case", async () => {
    await runServerSearch("c1", "Retry", true);
    expect(apiGet).toHaveBeenCalledWith("/api/chats/c1/search?q=Retry&case=1");
  });

  it("does not call the server for an empty chat id or a blank query", async () => {
    await runServerSearch("", "retry");
    await runServerSearch("c1", "   ");
    expect(apiGet).not.toHaveBeenCalled();
  });
});

describe("runServerSearch: the reveal", () => {
  it("opens each hit's turn by its OPENING message id", async () => {
    apiGet.mockResolvedValue({
      hits: [hit({ turn_message_id: "u1" }), hit({ message_id: "a2", turn_message_id: "u3" })],
    });
    await runServerSearch("c1", "retry");
    expect(openForSearch).toHaveBeenCalledWith("c1", "u1");
    expect(openForSearch).toHaveBeenCalledWith("c1", "u3");
    // The renderer has to see the reveal before the DOM walker runs.
    expect(bumpMessages).toHaveBeenCalled();
  });

  it("records the hit turns and their counts for the rail and the folded rows", async () => {
    apiGet.mockResolvedValue({
      hits: [hit({ turn: 2 }), hit({ turn: 2, message_id: "a2" }), hit({ turn: 5 })],
    });
    await runServerSearch("c1", "retry");
    expect([...searchHitTurns()].sort((a, b) => a - b)).toEqual([2, 5]);
    expect(searchHitCount(2)).toBe(2);
    expect(searchHitCount(5)).toBe(1);
    expect(searchHitCount(9)).toBe(0);
  });

  it("leaves the previous reveal in place when the fetch fails", async () => {
    apiGet.mockResolvedValue({ hits: [hit({ turn: 2 })] });
    await runServerSearch("c1", "retry");
    apiGet.mockResolvedValue(null);
    const out = await runServerSearch("c1", "retry");
    // A failed fetch must not collapse turns out from under a reader mid-search:
    // the previous run's reveal and counts stay exactly as they were.
    expect(out).toEqual([]);
    expect(searchHitCount(2)).toBe(1);
    expect([...searchHitTurns()]).toEqual([2]);
  });

  it("drops the reveal and the counts on reset", async () => {
    apiGet.mockResolvedValue({ hits: [hit({ turn: 2 })] });
    await runServerSearch("c1", "retry");
    resetServerSearch("c1");
    expect([...searchHitTurns()]).toEqual([]);
    expect(searchHitCount(2)).toBe(0);
    expect(clearSearchOpened).toHaveBeenCalledWith("c1");
  });

  it("records the session-wide total, which is what the counter reports", async () => {
    // Every hit, not every turn: two hits in one turn are two the reader could be
    // looking for. This is the figure `formatCount` needs so the overlay can stop
    // printing "No matches" over content the walker merely could not reach.
    apiGet.mockResolvedValue({
      hits: [hit({ turn: 2 }), hit({ turn: 2 }), hit({ turn: 5 })],
    });
    await runServerSearch("c1", "x");
    expect(searchHitTotal()).toBe(3);
  });

  it("has no total before a search and none after a reset", async () => {
    expect(searchHitTotal()).toBe(0);
    apiGet.mockResolvedValue({ hits: [hit({ turn: 1 })] });
    await runServerSearch("c1", "x");
    expect(searchHitTotal()).toBe(1);
    resetServerSearch("c1");
    // Zero rather than stale: the counter falls back to the DOM number, which is
    // the honest answer once there is no server opinion standing.
    expect(searchHitTotal()).toBe(0);
  });

  it("clears the total for a blank query, which resets rather than searches", async () => {
    apiGet.mockResolvedValue({ hits: [hit({ turn: 1 })] });
    await runServerSearch("c1", "x");
    expect(searchHitTotal()).toBe(1);
    await runServerSearch("c1", "   ");
    expect(searchHitTotal()).toBe(0);
  });

  it("keeps the previous total when the fetch fails", async () => {
    apiGet.mockResolvedValue({ hits: [hit({ turn: 1 }), hit({ turn: 3 })] });
    await runServerSearch("c1", "x");
    apiGet.mockResolvedValue(null);
    await runServerSearch("c1", "x");
    // Same reasoning as the reveal: a transient failure must not collapse the
    // reader's search out from under them, and a total of 0 would re-arm the
    // "No matches" skin over content that is still there.
    expect(searchHitTotal()).toBe(2);
  });

  it("skips a hit the server could not resolve to a turn opener", async () => {
    apiGet.mockResolvedValue({ hits: [hit({ turn_message_id: "" })] });
    await runServerSearch("c1", "retry");
    expect(openForSearch).not.toHaveBeenCalled();
  });
});
