// The per-chat localStorage map two viewer-owned stores sit on (fold overrides,
// banner dismissals). What is pinned is the BOUND, because that is the part with
// no other guard: the cleanup calls that used to run on chat_deleted went with the
// global blob they existed for, and a retention purge removes a chat with no
// client involved at all, so nothing else will ever tell this module a chat is
// gone.
import { describe, it, expect, beforeEach } from "vitest";

import { readPerChat, writePerChat, MAX_TRACKED_CHATS } from "./per-chat-store.js";

const KEY = "vibekit.test-per-chat";

/** The only value shape these tests need: a list of codes, like a dismissal set. */
function validCodes(v: unknown): string[] | undefined {
  if (!Array.isArray(v)) {
    return undefined;
  }
  const out = v.filter((c): c is string => typeof c === "string" && c !== "");
  return out.length > 0 ? out : undefined;
}

function read(): Record<string, string[]> {
  return readPerChat(KEY, validCodes);
}

function write(chatID: string, value: string[] | undefined): void {
  writePerChat(KEY, read(), chatID, value);
}

beforeEach(() => {
  localStorage.clear();
});

describe("reading", () => {
  it("returns an empty map when nothing is stored", () => {
    expect(read()).toEqual({});
  });

  it("round-trips one chat's value", () => {
    write("c1", ["x", "y"]);
    expect(read()).toEqual({ c1: ["x", "y"] });
  });

  // Bytes nothing wrote must not take the app down: the honest failure is a
  // forgotten dismissal, not a blank transcript.
  it("survives bytes that are not JSON", () => {
    localStorage.setItem(KEY, "{not json");
    expect(read()).toEqual({});
  });

  it("survives a document that is not an object", () => {
    localStorage.setItem(KEY, JSON.stringify(["a", "b"]));
    expect(read()).toEqual({});
  });

  // Per ENTRY rather than per document, so one hand-edited chat does not cost
  // every other chat its state.
  it("drops only the entries whose shape is wrong", () => {
    localStorage.setItem(KEY, JSON.stringify({ good: ["x"], bad: 42, alsoBad: {} }));
    expect(read()).toEqual({ good: ["x"] });
  });

  it("drops an empty chat id", () => {
    localStorage.setItem(KEY, JSON.stringify({ "": ["x"], c1: ["y"] }));
    expect(read()).toEqual({ c1: ["y"] });
  });
});

describe("writing", () => {
  it("leaves other chats alone", () => {
    write("c1", ["x"]);
    write("c2", ["y"]);
    expect(read()).toEqual({ c1: ["x"], c2: ["y"] });
  });

  it("replaces a chat's value rather than merging it", () => {
    write("c1", ["x", "y"]);
    write("c1", ["z"]);
    expect(read()).toEqual({ c1: ["z"] });
  });

  // An undefined value is a DELETE. Keeping an empty record would spend a slot on
  // a chat with nothing to remember and evict one that has something.
  it("deletes the chat when the value is undefined", () => {
    write("c1", ["x"]);
    write("c2", ["y"]);
    write("c1", undefined);
    expect(read()).toEqual({ c2: ["y"] });
  });

  it("ignores an empty chat id", () => {
    write("", ["x"]);
    expect(read()).toEqual({});
  });
});

describe("the bound", () => {
  it("keeps at most MAX_TRACKED_CHATS chats", () => {
    for (let i = 0; i < MAX_TRACKED_CHATS + 20; i++) {
      write(`c${String(i)}`, ["x"]);
    }
    expect(Object.keys(read())).toHaveLength(MAX_TRACKED_CHATS);
  });

  it("evicts the oldest and keeps the newest", () => {
    for (let i = 0; i < MAX_TRACKED_CHATS + 1; i++) {
      write(`c${String(i)}`, ["x"]);
    }
    const kept = read();
    expect(kept["c0"]).toBeUndefined();
    expect(kept["c1"]).toEqual(["x"]);
    expect(kept[`c${String(MAX_TRACKED_CHATS)}`]).toEqual(["x"]);
  });

  // "Oldest" means least recently WRITTEN, not first ever seen, and the re-insert
  // on every write is what makes that true. Without it the eviction order would
  // be whatever the map happened to hold, so a chat the reader keeps coming back
  // to would age out while ones they never touch again survive.
  it("counts a rewrite as touching the chat, so it is not the next to go", () => {
    write("first", ["x"]);
    for (let i = 0; i < MAX_TRACKED_CHATS - 1; i++) {
      write(`c${String(i)}`, ["x"]);
    }
    // "first" is the oldest at this point; touching it moves it to the end.
    write("first", ["y"]);
    write("overflow", ["x"]);

    const kept = read();
    expect(kept["first"]).toEqual(["y"]);
    expect(kept["c0"]).toBeUndefined();
  });
});
