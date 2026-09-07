// ---------------------------------------------------------------------------
// Eviction and residency: the idle sweep that reclaims a background chat's
// message window, the five exemptions that each alone prevent it, the
// residency tri-state the next activation keys its refetch on, and the
// signal-leak half — removeChat and eviction must clear the per-message
// streaming signals that only teardownAll (last chat closed) used to drop.
//
// The sweep is driven with fake timers (Date is faked with them, so the idle
// clock and the interval agree), and the external exemptions go through the
// registration seam exactly as the composition root wires them — store.ts is a
// leaf and must not import tabs.ts or run-store.ts, so the seam IS the
// production shape, not a test convenience.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import {
  setSessions,
  setActive,
  get,
  appendMessage,
  appendChunk,
  upsertToolCall,
  setThinking,
  removeChat,
  evictChatMessages,
  registerEvictionExemption,
  startEvictionSweep,
  stopEvictionSweep,
  EVICT_SWEEP_MS,
  EVICT_IDLE_MS,
} from "./store.js";
import {
  blockTextSigs,
  blockThinkingSigs,
  blockKey,
  ensureBlockTextSig,
  ensureBlockThinkingSig,
  ensureStreamingSig,
  ensureReasoningSig,
  ensureToolCallSig,
  streamingTextSigs,
  streamingReasoningSigs,
  toolCallSigs,
  toolCallSigKey,
} from "./store-signals.js";
import type { Message, Session, ToolCall } from "./types.js";

function session(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: id,
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
    working_label: "Thinking",
    ...over,
  } as Session;
}

function msg(id: string): Message {
  return { id, role: "assistant", ts: 1, content: "hi" } as Message;
}

const unregisters: (() => void)[] = [];
function exempt(fn: (chatID: string) => boolean): void {
  unregisters.push(registerEvictionExemption(fn));
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  stopEvictionSweep();
  for (const un of unregisters.splice(0)) {
    un();
  }
  setSessions([]);
  setActive("");
  vi.useRealTimers();
});

/** Seed two chats, make `active` the active one, land one message on each
 *  (which stamps their activity), then age everything past the idle bound. */
function seedIdlePair(active: string, background: string): void {
  setSessions([session(active), session(background)]);
  setActive(active);
  appendMessage(active, msg(`m-${active}`));
  appendMessage(background, msg(`m-${background}`));
  vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
}

/** One sweep tick. */
function tick(): void {
  vi.advanceTimersByTime(EVICT_SWEEP_MS);
}

describe("the idle sweep", () => {
  it("evicts an idle background chat's window and leaves the session row", () => {
    seedIdlePair("c-act", "c-bg");
    startEvictionSweep();
    tick();

    const s = get("c-bg");
    expect(s, "the session ROW must survive eviction — header data stays").toBeDefined();
    expect(s?.messages).toEqual([]);
    expect(s?.residency).toBe("evicted");
    // The skeleton arm keys on messages.length === 0, so an evicted chat
    // re-opens onto the skeleton rather than a stale window.
    expect(s?.messages.length).toBe(0);
    // has_more re-derives from the server count so pagination stays honest.
    expect(s?.has_more).toBe(true);
    expect(s?.message_count).toBe(1);
    // Header data survives.
    expect(s?.name).toBe("c-bg");
  });

  it("does not run before the idle bound", () => {
    setSessions([session("c-act"), session("c-bg")]);
    setActive("c-act");
    appendMessage("c-bg", msg("m1"));
    startEvictionSweep();
    // Many sweep ticks, but the chat was active more recently than the bound.
    vi.advanceTimersByTime(EVICT_IDLE_MS - EVICT_SWEEP_MS);
    expect(get("c-bg")?.residency).toBeUndefined();
    expect(get("c-bg")?.messages).toHaveLength(1);
  });

  it("errs toward keeping a chat whose activity it never observed", () => {
    // A chat with resident messages but no recorded activity (seeded wholesale,
    // never stamped): the sweep must not treat unknown as ancient.
    setSessions([session("c-act"), session("c-unknown", { messages: [msg("m1")] })]);
    setActive("c-act");
    startEvictionSweep();
    vi.advanceTimersByTime(EVICT_IDLE_MS * 3);
    expect(get("c-unknown")?.messages).toHaveLength(1);
    expect(get("c-unknown")?.residency).toBeUndefined();
  });

  it("pauses while the document is hidden and resumes when visible", () => {
    seedIdlePair("c-act", "c-bg");
    const hidden = vi.spyOn(document, "hidden", "get").mockReturnValue(true);
    startEvictionSweep();
    tick();
    tick();
    expect(get("c-bg")?.residency, "a hidden tab must reclaim nothing").toBeUndefined();
    expect(get("c-bg")?.messages).toHaveLength(1);

    hidden.mockReturnValue(false);
    tick();
    expect(get("c-bg")?.residency).toBe("evicted");
  });
});

describe("the five exemptions, each alone", () => {
  it("never evicts the ACTIVE chat", () => {
    seedIdlePair("c-act", "c-bg");
    startEvictionSweep();
    tick();
    // The background sibling went (the sweep ran), the active one stayed.
    expect(get("c-bg")?.residency).toBe("evicted");
    expect(get("c-act")?.residency).toBeUndefined();
    expect(get("c-act")?.messages).toHaveLength(1);
  });

  it("never evicts a BUSY chat, however idle its clock reads", () => {
    seedIdlePair("c-act", "c-busy");
    setThinking("c-busy", true);
    // setThinking stamps activity, so age it past the bound again: the
    // exemption itself must hold, not the recency it implies.
    vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
    startEvictionSweep();
    tick();
    expect(get("c-busy")?.residency).toBeUndefined();
    expect(get("c-busy")?.messages).toHaveLength(1);
  });

  it("never evicts a chat a registered LIVE-RUN predicate names", () => {
    // The seam the composition root wires hasLiveRunForChat through; the
    // predicate's own behavior (event-fed, rebuilt, degrade rules) is
    // run-store.test.ts's subject.
    seedIdlePair("c-act", "c-run");
    exempt((chatID) => chatID === "c-run");
    startEvictionSweep();
    tick();
    expect(get("c-run")?.residency).toBeUndefined();
    expect(get("c-run")?.messages).toHaveLength(1);
  });

  it("never evicts a chat a registered PARKED-VIEW predicate names", () => {
    // Parked views land in a later task; the exemption is the injectable
    // predicate, defaulting to false when nothing registers (the sibling cases
    // above evict fine with no registration).
    seedIdlePair("c-act", "c-parked");
    exempt((chatID) => chatID === "c-parked");
    startEvictionSweep();
    tick();
    expect(get("c-parked")?.residency).toBeUndefined();
  });

  it("never evicts a chat a registered SUBAGENT-TAB predicate names", () => {
    seedIdlePair("c-act", "c-sub");
    exempt((chatID) => chatID === "c-sub");
    startEvictionSweep();
    tick();
    expect(get("c-sub")?.residency).toBeUndefined();
  });

  it("an unregistered exemption stops exempting", () => {
    seedIdlePair("c-act", "c-bg");
    const un = registerEvictionExemption((chatID) => chatID === "c-bg");
    startEvictionSweep();
    tick();
    expect(get("c-bg")?.residency).toBeUndefined();
    un();
    vi.advanceTimersByTime(EVICT_IDLE_MS + 1);
    tick();
    expect(get("c-bg")?.residency).toBe("evicted");
  });
});

describe("residency", () => {
  it("background ingest on an evicted chat marks it PARTIAL, never loaded", () => {
    setSessions([session("c1", { messages: [msg("m1")], message_count: 1 })]);
    evictChatMessages("c1");
    expect(get("c1")?.residency).toBe("evicted");

    // A remote append lands (SSE on a background chat).
    appendMessage("c1", msg("m2"));
    expect(get("c1")?.residency).toBe("partial");
    expect(get("c1")?.messages.map((m) => m.id)).toEqual(["m2"]);
  });

  it("a chunk that beats its message_created on an evicted chat marks PARTIAL too", () => {
    setSessions([session("c1", { messages: [msg("m1")], message_count: 1 })]);
    evictChatMessages("c1");
    appendChunk("c1", "m-live", "hello", false, 0, "");
    expect(get("c1")?.residency).toBe("partial");
  });

  it("a background tool_call creating its message on an evicted chat marks PARTIAL too", () => {
    setSessions([session("c1", { messages: [msg("m1")], message_count: 1 })]);
    evictChatMessages("c1");
    upsertToolCall(
      "c1",
      "m-tools",
      { id: "t1", kind: "execute", status: "pending" } as ToolCall,
      0,
    );
    expect(get("c1")?.residency).toBe("partial");
  });

  it("ingest on a chat that is not evicted claims nothing", () => {
    setSessions([session("c1")]);
    appendMessage("c1", msg("m1"));
    // Only a successful newest-page load may set `loaded`; plain ingest on a
    // never-loaded chat leaves the state absent.
    expect(get("c1")?.residency).toBeUndefined();
  });
});

describe("the signal leak", () => {
  it("eviction clears every per-message streaming signal the window minted", () => {
    const withTool: Message = {
      ...msg("m1"),
      tool_calls: [{ id: "t1", kind: "execute", status: "pending" } as ToolCall],
    };
    setSessions([session("c1", { messages: [withTool, msg("m2")], message_count: 2 })]);
    ensureBlockTextSig("m1", 0, "x");
    ensureBlockThinkingSig("m1", 1, "y");
    ensureBlockTextSig("m2", 0, "z");
    ensureStreamingSig("m1", "x");
    ensureReasoningSig("m1", "y");
    ensureToolCallSig("c1", "t1", { id: "t1" } as ToolCall);

    evictChatMessages("c1");

    expect(blockTextSigs.get(blockKey("m1", 0))).toBeUndefined();
    expect(blockThinkingSigs.get(blockKey("m1", 1))).toBeUndefined();
    expect(blockTextSigs.get(blockKey("m2", 0))).toBeUndefined();
    expect(streamingTextSigs.get("m1")).toBeUndefined();
    expect(streamingReasoningSigs.get("m1")).toBeUndefined();
    expect(toolCallSigs.get(toolCallSigKey("c1", "t1"))).toBeUndefined();
  });

  it("removeChat clears them too, without waiting for a render pass", () => {
    const withTool: Message = {
      ...msg("m1"),
      tool_calls: [{ id: "t1", kind: "execute", status: "pending" } as ToolCall],
    };
    setSessions([session("c1", { messages: [withTool] })]);
    ensureBlockTextSig("m1", 0, "x");
    ensureBlockThinkingSig("m1", 0, "y");
    ensureStreamingSig("m1", "x");
    ensureToolCallSig("c1", "t1", { id: "t1" } as ToolCall);

    removeChat("c1");

    expect(blockTextSigs.get(blockKey("m1", 0))).toBeUndefined();
    expect(blockThinkingSigs.get(blockKey("m1", 0))).toBeUndefined();
    expect(streamingTextSigs.get("m1")).toBeUndefined();
    expect(toolCallSigs.get(toolCallSigKey("c1", "t1"))).toBeUndefined();
  });

  it("clearing one message's signals leaves a sibling chat's alone", () => {
    setSessions([session("c1", { messages: [msg("m1")] }), session("c2", { messages: [] })]);
    ensureBlockTextSig("m1", 0, "x");
    ensureBlockTextSig("m-other", 0, "y");

    evictChatMessages("c1");

    expect(blockTextSigs.get(blockKey("m1", 0))).toBeUndefined();
    expect(blockTextSigs.get(blockKey("m-other", 0))).toBeDefined();
    blockTextSigs.clear(blockKey("m-other", 0));
  });
});
