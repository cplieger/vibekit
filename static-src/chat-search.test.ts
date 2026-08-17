// The server pre-pass that makes the DOM search honest: which URL it asks for,
// and what it does with the answer.
import { describe, it, expect, vi, beforeEach } from "vitest";
import type { SearchHit } from "./chat-search.js";

const apiGet = vi.fn<(url: string) => Promise<{ hits?: SearchHit[] } | null>>();
const openForSearch = vi.fn();
// The chat id is declared because fold-state.ts's clearSearchOpened takes one and
// the wrapper below forwards it; a nullary mock types its own call log as empty.
const clearSearchOpened = vi.fn((_chatID: string) => true);
const emitMessages = vi.fn();

vi.mock("./api-client.js", () => ({ apiGet: (url: string) => apiGet(url) }));
vi.mock("./fold-state.js", () => ({
  openForSearch: (chatID: string, id: string) => openForSearch(chatID, id),
  clearSearchOpened: (chatID: string) => clearSearchOpened(chatID),
}));
vi.mock("./store.js", () => ({ emitMessages: () => emitMessages() }));

const { runServerSearch, resetServerSearch, searchHitTurns, searchHitCount } =
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
    expect(emitMessages).toHaveBeenCalled();
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

  it("skips a hit the server could not resolve to a turn opener", async () => {
    apiGet.mockResolvedValue({ hits: [hit({ turn_message_id: "" })] });
    await runServerSearch("c1", "retry");
    expect(openForSearch).not.toHaveBeenCalled();
  });
});
