// ---------------------------------------------------------------------------
// "Is everything this chat started actually over?"
//
// The predicate exists because a chat's TURN ending is not the same fact as a chat's
// WORK ending: `run_workflow` returns as soon as the run is created, so the launching
// turn concludes, `thinking` clears, and the run carries on. Every case below is one
// of the three reasons a cue must wait, plus the one property `agent-finished-cue.ts`
// hangs its whole release on — that every read is TRACKED, so an effect over
// `chatSettled` re-runs when the last outstanding thing ends.
//
// The dock is mocked down to `hasPendingDecision` (its own suite owns the queue), but
// the mock keeps a real signal behind it: what is under test here is that
// `chatOutstanding` subscribes to whatever that read touches, and a plain boolean
// would silently pass a version of the predicate that had stopped subscribing.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal, touch, effect } from "@cplieger/reactive";
import type { Session } from "./types.js";

// run-store fetches through the API client on invalidation; nothing here invalidates,
// so the mock exists only to keep a real `fetch` off the wire.
vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(async () => null),
  apiGetTyped: vi.fn(async () => null),
  // The run store's own read. Status 0 is a read that never left, which is what a null
  // answered before the store started spending a failed read's status.
  apiGetOrError: vi.fn(async () => ({ ok: false, status: 0, data: null, error: "" })),
}));

const pendingChats = new Set<string>();
const pendingVersion = signal(0);
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: (chatID: string) => {
    touch(pendingVersion);
    return pendingChats.has(chatID);
  },
}));

function setPending(chatID: string, on: boolean): void {
  if (on) {
    pendingChats.add(chatID);
  } else {
    pendingChats.delete(chatID);
  }
  pendingVersion.value = pendingVersion.peek() + 1;
}

const { chatOutstanding, chatSettled } = await import("./chat-settled.js");
const store = await import("./store.js");
const runStore = await import("./run-store.js");

// A row with `provisional` unset and `turn_open` absent: `statesNoLiveness` reads a
// provisional row as LIVE, so a fixture that left it out would report every chat's
// turn as running and no case could tell the turn term from the others.
function makeSession(id: string): Session {
  return {
    id,
    name: "test",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    supervised_mode: false,
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    turn_open: false,
    working_label: "Thinking",
  };
}

const runIDs = ["wf-a", "wf-b", "wf-parentless"];

beforeEach(() => {
  for (const id of runIDs) {
    runStore.noteRunSettled(id);
  }
  pendingChats.clear();
  store.setSessions([makeSession("c1"), makeSession("c2")]);
});

describe("chatSettled", () => {
  it("is true for an idle chat with nothing outstanding", () => {
    expect(chatSettled("c1")).toBe(true);
  });

  it("is false while a run this chat launched is EXECUTING", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    expect(chatSettled("c1")).toBe(false);
  });

  // The case the whole predicate exists for. `hasExecutingRunForChat` answers no here
  // (it is the store-eviction question), so a predicate reading that instead would
  // report a chat waiting on a person as finished.
  it("is false while a run this chat launched is PARKED", () => {
    runStore.noteRunLive("wf-a", "c1", false);
    expect(chatSettled("c1")).toBe(false);
  });

  it("is false while a decision sits in this chat's dock queue", () => {
    setPending("c1", true);
    expect(chatSettled("c1")).toBe(false);
  });

  it("is false while the chat's own turn is live", () => {
    store.setThinking("c1", true);
    expect(chatSettled("c1")).toBe(false);
  });

  // `turnLive` reads a row that states NO liveness as running, so a provisional row is
  // outstanding too — a boot-snapshot hint must not be read as "the turn is over".
  it("is false for a row that states no liveness at all", () => {
    const provisional: Session = { ...makeSession("c3"), provisional: true };
    store.setSessions([provisional]);
    expect(chatSettled("c3")).toBe(false);
  });

  it("becomes true once the run terminates", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    expect(chatSettled("c1")).toBe(false);
    runStore.noteRunSettled("wf-a");
    expect(chatSettled("c1")).toBe(true);
  });

  it("is unmoved by another chat's live run", () => {
    runStore.noteRunLive("wf-b", "c2", true);
    expect(chatSettled("c1")).toBe(true);
    expect(chatSettled("c2")).toBe(false);
  });

  // A manual or scheduled launch is parentless, so its lease names no chat and it may
  // not hold anyone's cue. Its own outcome travels on the run's tab dot and its own
  // push, not on a chat's.
  it("is unmoved by a PARENTLESS run", () => {
    runStore.noteRunLive("wf-parentless", "", true);
    expect(chatSettled("c1")).toBe(true);
  });

  // An unknown chat has no session, no live run and no queue, so a caller needs no
  // second existence check before asking.
  it("is true for a chat that does not exist", () => {
    expect(chatSettled("nope")).toBe(true);
  });

  it("is true for the empty id, which is not a chat", () => {
    expect(chatSettled("")).toBe(true);
  });
});

describe("chatOutstanding names the reason", () => {
  it("reports each term independently", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    runStore.noteRunLive("wf-b", "c1", false);
    setPending("c1", true);
    store.setThinking("c1", true);

    expect(chatOutstanding("c1")).toEqual({ turn: true, runs: 2, asks: true });
  });

  it("reports a run-only hold as a run-only hold", () => {
    runStore.noteRunLive("wf-a", "c1", true);
    expect(chatOutstanding("c1")).toEqual({ turn: false, runs: 1, asks: false });
  });

  it("reports nothing outstanding for an idle chat", () => {
    expect(chatOutstanding("c1")).toEqual({ turn: false, runs: 0, asks: false });
  });
});

// THE PROPERTY THE RELEASE DEPENDS ON. `agent-finished-cue.ts` re-fires a parked cue
// from a plain effect rather than a second event wire, which only works because every
// term here is a tracked read. An untracked `get()` for the turn term, or a dock
// predicate that stopped touching its queue version, leaves the cue parked forever
// with nothing observable at the raise site.
describe("every term is a tracked read", () => {
  function watch(chatID: string): { settled: boolean[]; dispose: () => void } {
    const settled: boolean[] = [];
    const dispose = effect(() => {
      settled.push(chatSettled(chatID));
    });
    return { settled, dispose };
  }

  it("re-runs when a run starts and when it settles", () => {
    const w = watch("c1");
    expect(w.settled).toEqual([true]);

    runStore.noteRunLive("wf-a", "c1", true);
    expect(w.settled.at(-1)).toBe(false);

    runStore.noteRunSettled("wf-a");
    expect(w.settled.at(-1)).toBe(true);
    w.dispose();
  });

  it("re-runs when an ask arrives and when it is answered", () => {
    const w = watch("c1");
    setPending("c1", true);
    expect(w.settled.at(-1)).toBe(false);
    setPending("c1", false);
    expect(w.settled.at(-1)).toBe(true);
    w.dispose();
  });

  it("re-runs when the chat's own turn state moves", () => {
    const w = watch("c1");
    store.setThinking("c1", true);
    expect(w.settled.at(-1)).toBe(false);
    store.setThinking("c1", false);
    expect(w.settled.at(-1)).toBe(true);
    w.dispose();
  });

  // The empty-id case has no early return in production on purpose: a branch that
  // skipped the three reads would leave a calling effect subscribed to nothing, so a
  // later state change could never wake it. Asserted through the inventory, whose
  // version the empty-id path still touches.
  it("subscribes even for a chat with nothing to read", () => {
    const w = watch("");
    const before = w.settled.length;
    runStore.noteRunLive("wf-a", "c1", true);
    expect(w.settled.length).toBeGreaterThan(before);
    w.dispose();
  });
});
