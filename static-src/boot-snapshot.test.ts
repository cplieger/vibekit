// The boot snapshot: a bounded projection of what this screen was showing, held
// in IndexedDB so a resume paints before the network answers.
//
// Two properties carry the whole design and both are asserted end to end against
// REAL IndexedDB and the REAL store: a record that does not decode is rejected
// without throwing (a paint-time hint has no failure a caller could act on), and
// what it paints is superseded by the server's answer rather than competing with
// it. `tabs.js` is the one mocked collaborator — its import graph reaches the DOM
// strip, and what matters here is which subjects the snapshot hands it.

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { Message, Session, TabSubject, Usage } from "./types.js";

const m = vi.hoisted(() => ({
  openTabSubjects: vi.fn(),
  paintProvisionalTabs: vi.fn(),
  tabSetVersion: vi.fn(() => 0),
}));

vi.mock("./tabs.js", () => ({
  openTabSubjects: m.openTabSubjects,
  paintProvisionalTabs: m.paintProvisionalTabs,
  tabSetVersion: m.tabSetVersion,
}));

import {
  _resetForTest,
  captureBootSnapshot,
  clearBootSnapshot,
  paintBootSnapshot,
  readBootSnapshot,
  startBootSnapshot,
  type BootSnapshot,
} from "./boot-snapshot.js";
import {
  get,
  setActive,
  setSessions,
  transcriptStale,
  turnBaseOf,
  upsertMessage,
} from "./store.js";
import { projectTurns, type Turn, type TurnWindowBase } from "./turns.js";

const DB_NAME = "vibekit-boot";
const STORE_NAME = "snapshot";
const RECORD_KEY = "current";

const EMPTY_USAGE: Usage = {
  context_pct: 0,
  context_size: 0,
  credits: 0,
  turn_count: 0,
  last_turn_ms: 0,
  has_real_data: false,
};

function chatTab(id: string, ref: string): TabSubject {
  return { id, kind: "chat", ref, parent: "", pinned: false, owns: true };
}

function session(id: string, name: string, messages: Message[] = []): Session {
  return {
    id,
    name,
    model: "claude",
    acp_session_id: "acp-1",
    current_mode_id: "default",
    usage: EMPTY_USAGE,
    messages,
    message_count: messages.length,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

/** A snapshot window, defaulting to the session-start base a whole-session capture
 *  produces, so a case that is not about the base does not have to state one. */
function snapWindow(
  chatID: string,
  messages: Message[],
  base: TurnWindowBase = { offset: 0, closed: false },
): BootSnapshot["window"] {
  return { chat_id: chatID, messages, base };
}

/** The captured window, asserted PRESENT. A `?? []` fallback would let a null window
 *  satisfy an "these ids are gone" assertion for the wrong reason. */
function capturedWindow(): NonNullable<BootSnapshot["window"]> {
  const win = captureBootSnapshot().window;
  if (win === null) {
    throw new Error("captureBootSnapshot carried no window");
  }
  return win;
}

/** A session whose window is a PAGE: a tail of a longer transcript, plus the base
 *  `loadMessages` adopts from the window response's own left edge. */
function pagedSession(id: string, messages: Message[], base: TurnWindowBase): Session {
  return {
    ...session(id, "One", messages),
    // Older messages exist, which is what makes the base mean anything.
    message_count: messages.length + 20,
    has_more: true,
    turn_offset: base.offset,
    turn_segment_closed: base.closed,
  };
}

function stepRow(i: number): Message {
  return { id: `r${String(i)}`, role: "assistant", ts: 100 + i, content: "step" };
}

/** Which turn NUMBER each message lands in.
 *
 *  MESSAGE-keyed rather than turn-keyed, because the over-budget cut is inside a turn:
 *  the fragment's first message is not the parent turn's first, so `Turn.id` (that
 *  first message's id) legitimately differs while every carried card must still
 *  render the same number. */
function turnNumberByMessage(turns: Turn[]): Map<string, number> {
  const out = new Map<string, number>();
  for (const t of turns) {
    if (t.trigger !== undefined) {
      out.set(t.trigger.id, t.n);
    }
    for (const msg of t.body) {
      out.set(msg.id, t.n);
    }
  }
  return out;
}

/** The property this design exists for, expressed over the production projection
 *  rather than restated as literals: project the SESSION's window with the base the
 *  session holds, project the SNAPSHOT's with the base it carried, and every message
 *  present in both must land in the same numbered turn. */
function expectSameTurnNumbers(win: NonNullable<BootSnapshot["window"]>, s: Session): void {
  const carried = turnNumberByMessage(projectTurns(win.messages, false, win.base));
  const parent = turnNumberByMessage(projectTurns(s.messages, false, turnBaseOf(s)));
  const ids = [...carried.keys()];

  // Or the comparison below is vacuous.
  expect(ids.length).toBeGreaterThan(0);
  expect(ids.map((id) => carried.get(id))).toEqual(ids.map((id) => parent.get(id)));
}

/** The store row for a chat, asserted present. */
function row(id: string): Session {
  const s = get(id);
  if (s === undefined) {
    throw new Error(`no store row for ${id}`);
  }
  return s;
}

/** One user prompt and one closing assistant reply: a complete turn, which is the
 *  unit the capture's bound is expressed in. */
function turn(n: number): Message[] {
  return [
    { id: `u${String(n)}`, role: "user", ts: n * 10, content: `ask ${String(n)}` },
    {
      id: `a${String(n)}`,
      role: "assistant",
      ts: n * 10 + 1,
      content: `answer ${String(n)}`,
      turn_outcome: "completed",
    },
  ];
}

/** The module's own object store, opened separately so a test can plant a record
 *  the module would never write. Two connections are safe: the version never
 *  changes, so neither blocks the other. */
async function withStore<T>(
  mode: IDBTransactionMode,
  fn: (store: IDBObjectStore) => IDBRequest,
): Promise<T> {
  const db = await new Promise<IDBDatabase>((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1);
    req.onupgradeneeded = () => {
      req.result.createObjectStore(STORE_NAME);
    };
    req.onsuccess = () => {
      resolve(req.result);
    };
    req.onerror = () => {
      reject(new Error("open failed"));
    };
  });
  try {
    return await new Promise<T>((resolve, reject) => {
      const req = fn(db.transaction(STORE_NAME, mode).objectStore(STORE_NAME));
      req.onsuccess = () => {
        resolve(req.result as T);
      };
      req.onerror = () => {
        reject(new Error("request failed"));
      };
    });
  } finally {
    db.close();
  }
}

async function plantRecord(value: unknown): Promise<void> {
  await withStore("readwrite", (s) => s.put(value, RECORD_KEY));
}

beforeEach(async () => {
  vi.useFakeTimers();
  _resetForTest();
  setSessions([]);
  setActive("");
  m.openTabSubjects.mockReturnValue([]);
  await withStore("readwrite", (s) => s.clear());
});

afterEach(() => {
  vi.useRealTimers();
  _resetForTest();
});

describe("readBootSnapshot", () => {
  it("resolves null when this screen has never been captured", async () => {
    expect(await readBootSnapshot()).toBeNull();
  });

  it("rejects a corrupt record without throwing", async () => {
    await plantRecord("not an object at all");

    expect(await readBootSnapshot()).toBeNull();
  });

  it("rejects a record whose ELEMENTS are wrong, not just its container", async () => {
    // The container is the right shape and the arrays are arrays; one tab subject
    // is missing `pinned` and one message's role is not in the wire enum. A
    // container-only check would hand both to the paint.
    await plantRecord({
      tabs: [{ id: "t1", kind: "chat", ref: "c1", parent: "", owns: true }],
      chats: [
        {
          id: "c1",
          name: "One",
          model: "",
          current_mode_id: "",
          message_count: 0,
          usage: EMPTY_USAGE,
        },
      ],
      window: {
        chat_id: "c1",
        messages: [{ id: "m1", role: "narrator", ts: 1 }],
        base: { offset: 0, closed: false },
      },
    });

    expect(await readBootSnapshot()).toBeNull();
  });

  it("rejects a window whose BASE is half-present", async () => {
    // The half-present rule `store-load.ts` `adoptTurnBase` implements by FORGETTING;
    // here there is nothing yet to forget, so the honest answer is to refuse the
    // record rather than paint a tail numbered from an edge nobody stated.
    await plantRecord({
      tabs: [{ id: "t1", kind: "chat", ref: "c1", parent: "", pinned: false, owns: true }],
      chats: [
        {
          id: "c1",
          name: "One",
          model: "",
          current_mode_id: "",
          message_count: 2,
          usage: EMPTY_USAGE,
        },
      ],
      window: { chat_id: "c1", messages: turn(1), base: { closed: false } },
    });

    expect(await readBootSnapshot()).toBeNull();
  });
});

describe("the capture", () => {
  it("persists the tab set, the open chats and the active transcript", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "Refactor the boot")]);
    setActive("c1");
    for (const msg of turn(1)) {
      upsertMessage("c1", msg);
    }

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    const snap = await readBootSnapshot();
    expect(snap?.tabs).toEqual([chatTab("t1", "c1")]);
    expect(snap?.chats.map((c) => c.name)).toEqual(["Refactor the boot"]);
    expect(snap?.window?.chat_id).toBe("c1");
    expect(snap?.window?.messages.map((msg) => msg.id)).toEqual(["u1", "a1"]);
  });

  it("writes nothing until the projection has stood still", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(999);

    // A streaming turn moves the transcript version every frame; a write per frame
    // is a whole-record replace per frame.
    expect(await readBootSnapshot()).toBeNull();
  });

  it("carries the newest three turns and no more", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);
    setActive("c1");
    for (const n of [1, 2, 3, 4, 5]) {
      for (const msg of turn(n)) {
        upsertMessage("c1", msg);
      }
    }

    expect(capturedWindow().messages.map((msg) => msg.id)).toEqual([
      "u3",
      "a3",
      "u4",
      "a4",
      "u5",
      "a5",
    ]);
  });

  it("drops the record and stops capturing on a sign-out", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);
    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await readBootSnapshot()).not.toBeNull();

    await clearBootSnapshot();
    expect(await readBootSnapshot()).toBeNull();

    // And nothing writes it back: a login screen must not re-capture the workspace
    // it is covering. `setActive` is one of the three reads the capture watches, so
    // a live effect would schedule a write here.
    setActive("c1");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(await readBootSnapshot()).toBeNull();
  });

  it("does not resurrect the record when the page hides after a sign-out", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);
    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    await clearBootSnapshot();
    // The last event a backgrounded PWA gets. It flushes the projection, which is
    // exactly what must NOT happen once the user has signed out: the rows are still
    // in the store, so a live listener would write the record straight back.
    dispatchEvent(new Event("pagehide"));
    await vi.advanceTimersByTimeAsync(0);

    expect(await readBootSnapshot()).toBeNull();
  });

  it("cuts a turn's OLDEST rows rather than its trigger when one turn is over budget", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);
    setActive("c1");
    // One user prompt and 60 tool rows: past the 40-message cap inside a single
    // turn, which is the case the cap exists for.
    upsertMessage("c1", { id: "u1", role: "user", ts: 10, content: "ask" });
    for (let i = 0; i < 60; i++) {
      upsertMessage("c1", { id: `t${String(i)}`, role: "assistant", ts: 11 + i, content: "step" });
    }

    const ids = capturedWindow().messages.map((msg) => msg.id);

    expect(ids).toHaveLength(40);
    // The trigger survives: a body with no trigger renders as a card with no
    // header, which is what the turn bound exists to prevent.
    expect(ids[0]).toBe("u1");
    // And what went is the OLD end of the body.
    expect(ids[1]).toBe("t21");
    expect(ids.at(-1)).toBe("t59");
  });

  it("drops WHOLE turns when the newest three do not fit", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One")]);
    setActive("c1");
    // 30 + 15 + 10 = 55 messages against a 40-message cap. The two newest turns fit
    // (25); adding the oldest would not, so all 30 of it go. The sizes are uneven on
    // purpose: with three equal turns a tail slice of the flattened list happens to
    // land on a turn boundary and both rules agree.
    for (const [n, size] of [
      [1, 30],
      [2, 15],
      [3, 10],
    ] as const) {
      upsertMessage("c1", { id: `u${String(n)}`, role: "user", ts: n * 1000, content: "ask" });
      for (let i = 0; i < size - 1; i++) {
        upsertMessage("c1", {
          id: `a${String(n)}-${String(i)}`,
          role: "assistant",
          ts: n * 1000 + 1 + i,
          content: "step",
        });
      }
    }

    const ids = capturedWindow().messages.map((msg) => msg.id);

    // Two whole turns, not a 40-message tail: a tail slice would have kept 40, the
    // oldest 15 of them a headerless fragment of turn 1.
    expect(ids).toHaveLength(25);
    expect(ids[0]).toBe("u2");
    expect(ids).not.toContain("u1");
    // Turn 1 went WHOLE — its newest body row is as gone as its trigger.
    expect(ids).not.toContain("a1-28");
  });

  it("carries only the chats a tab names", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    // c2 is a closed chat whose row the store still holds. It is not what this
    // screen was showing.
    setSessions([session("c1", "Open"), session("c2", "Closed")]);

    expect(captureBootSnapshot().chats.map((c) => c.id)).toEqual(["c1"]);
  });

  it("numbers a PAGED window's turns the way the session numbers them", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([
      pagedSession("c1", [...turn(1), ...turn(2), ...turn(3), ...turn(4), ...turn(5)], {
        offset: 7,
        closed: true,
      }),
    ]);
    setActive("c1");

    expectSameTurnNumbers(capturedWindow(), row("c1"));
  });

  it("numbers them the same way through the OVER-BUDGET cut", () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    // A page that opens MID-TURN: 60 rows, no trigger, so the 40-message cap cuts
    // inside the turn — the one cut in this module that is not at a turn boundary. The
    // row the fragment opens on is an EVENT carrying no outcome, which is the shape a
    // wrongly-seeded `closed` lets open a spurious turn: an assistant row would open
    // one itself and reset the seed before it could cascade.
    setSessions([
      pagedSession(
        "c1",
        [
          ...Array.from({ length: 20 }, (_, i) => stepRow(i)),
          { id: "r20", role: "event", ts: 120, content: "checkpoint" },
          ...Array.from({ length: 39 }, (_, i) => stepRow(21 + i)),
        ],
        { offset: 7, closed: true },
      ),
    ]);
    setActive("c1");

    const win = capturedWindow();

    // The cut is where the fixture puts it, or the case below is a boundary cut
    // wearing this name.
    expect(win.messages).toHaveLength(40);
    expect(win.messages[0]?.id).toBe("r20");
    expectSameTurnNumbers(win, row("c1"));
  });

  it("round-trips a snapshot with no active chat, and paints rows with no transcript", async () => {
    m.openTabSubjects.mockReturnValue([chatTab("t1", "c1")]);
    setSessions([session("c1", "One", turn(1))]);
    setActive("");

    startBootSnapshot();
    await vi.advanceTimersByTimeAsync(1_000);

    const snap = await readBootSnapshot();
    // `null` decodes as the VALUE it is, so the rows and the strip still paint.
    expect(snap?.window).toBeNull();
    expect(paintBootSnapshot(snap)).toBe(true);
    expect(row("c1").messages).toEqual([]);
    expect(row("c1").turn_offset).toBeUndefined();
    expect(row("c1").turn_segment_closed).toBeUndefined();
  });
});

describe("paintBootSnapshot", () => {
  it("paints nothing when there is no snapshot", () => {
    expect(paintBootSnapshot(null)).toBe(false);
    expect(m.paintProvisionalTabs).not.toHaveBeenCalled();
  });

  it("paints nothing when the snapshot holds no tabs", () => {
    expect(paintBootSnapshot({ tabs: [], chats: [], window: null })).toBe(false);
    expect(m.paintProvisionalTabs).not.toHaveBeenCalled();
  });

  it("paints the chat rows, then the strip, then the transcript", () => {
    const painted = paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [
        {
          id: "c1",
          name: "Refactor the boot",
          model: "claude",
          current_mode_id: "default",
          message_count: 2,
          usage: EMPTY_USAGE,
        },
      ],
      window: snapWindow("c1", turn(1)),
    });

    expect(painted).toBe(true);
    // The rows go in BEFORE the strip: a chat tab's label is read from the store
    // while its row is built.
    expect(get("c1")?.name).toBe("Refactor the boot");
    expect(m.paintProvisionalTabs).toHaveBeenCalledWith([chatTab("t1", "c1")]);
    expect(get("c1")?.messages.map((msg) => msg.id)).toEqual(["u1", "a1"]);
  });

  it("claims no transcript residency, so the activation refetches the window", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [
        {
          id: "c1",
          name: "One",
          model: "",
          current_mode_id: "",
          message_count: 2,
          usage: EMPTY_USAGE,
        },
      ],
      window: snapWindow("c1", turn(1)),
    });

    const row = get("c1");
    expect(row).toBeDefined();
    // The mechanism that makes the hint self-superseding: `transcriptStale` is what
    // `activateChatView` keys its fetch on, and a painted window must not pass for
    // one the server answered.
    expect(row !== undefined && transcriptStale(row)).toBe(true);
  });

  it("numbers a PAGED window's turns absolutely, not from #1", () => {
    // The live defect: the row and its window used to be assembled by two calls, so
    // neither held both and the row got no base. `turnBaseOf` then fell back to
    // `WHOLE_SESSION` and the pre-network paint numbered a partial tail #1..#3 — both
    // the `.turn-n` text and each card's `turnAnchorID` — until the activation refetch
    // landed and every number moved.
    const base: TurnWindowBase = { offset: 9, closed: true };

    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [
        {
          id: "c1",
          name: "One",
          model: "",
          current_mode_id: "",
          message_count: 40,
          usage: EMPTY_USAGE,
        },
      ],
      window: snapWindow("c1", [...turn(1), ...turn(2), ...turn(3)], base),
    });

    const painted = row("c1");
    expect(turnBaseOf(painted)).toEqual(base);
    expect(projectTurns(painted.messages, false, turnBaseOf(painted)).map((t) => t.n)).toEqual([
      10, 11, 12,
    ]);
  });

  it("claims no more messages for a short chat carried WHOLE", () => {
    // The corollary the restructure makes unspellable: `has_more` was derived against
    // ZERO resident messages and then up to 40 of them arrived through a second call,
    // so a chat holding every message it has claimed older ones existed.
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [
        {
          id: "c1",
          name: "One",
          model: "",
          current_mode_id: "",
          message_count: 2,
          usage: EMPTY_USAGE,
        },
      ],
      window: snapWindow("c1", turn(1)),
    });

    expect(row("c1").has_more).toBe(false);
  });

  it("gives a base to the transcript chat and to no other row", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1"), chatTab("t2", "c2")],
      chats: [
        {
          id: "c1",
          name: "Active",
          model: "",
          current_mode_id: "",
          message_count: 40,
          usage: EMPTY_USAGE,
        },
        {
          id: "c2",
          name: "Open, not showing",
          model: "",
          current_mode_id: "",
          message_count: 12,
          usage: EMPTY_USAGE,
        },
      ],
      window: snapWindow("c1", turn(1), { offset: 4, closed: true }),
    });

    expect(turnBaseOf(row("c1"))).toEqual({ offset: 4, closed: true });
    // A base held against no messages would number the NEXT page from an edge nothing
    // in the row corresponds to, which is what `evictChatMessages` deletes it for.
    expect(row("c2").messages).toEqual([]);
    expect(row("c2").turn_offset).toBeUndefined();
    expect(row("c2").turn_segment_closed).toBeUndefined();
  });

  it("is replaced whole by the server's own chat list", () => {
    paintBootSnapshot({
      tabs: [chatTab("t1", "c1")],
      chats: [
        {
          id: "c1",
          name: "Stale name",
          model: "",
          current_mode_id: "",
          message_count: 2,
          usage: EMPTY_USAGE,
        },
      ],
      window: snapWindow("c1", turn(1)),
    });

    // What `loadList` does when it lands.
    setSessions([session("c1", "The name the server holds")]);

    expect(get("c1")?.name).toBe("The name the server holds");
  });
});
