// Unit tests for failure-notice.ts — the bottom-right toast that replaced the
// send button's hover tooltip as the surface a model/agent failure reaches.
//
// The four properties worth pinning are the four ways the old surface failed a
// reader: it double-reported nothing (the toast must dedupe the two channels one
// failure arrives on), it never named a background chat, it had no way to be
// retracted when the failure turned out not to be one, and it said nothing at all
// when the server sent an empty message.
import { describe, it, expect, beforeEach, vi } from "vitest";

// Declared via vi.hoisted so they exist when the hoisted vi.mock factories run.
const { mockDismiss, mockToastError, mockErrorWithAction, mockActivateTab } = vi.hoisted(() => {
  const dismiss = vi.fn();
  return {
    mockDismiss: dismiss,
    mockToastError: vi.fn(
      (_message: string, _retry?: { label?: string; onClick: () => void }): (() => void) => dismiss,
    ),
    mockErrorWithAction: vi.fn(
      (_message: string, _action: { label?: string; onClick: () => void }): (() => void) => dismiss,
    ),
    mockActivateTab: vi.fn(),
  };
});
vi.mock("./toast.js", async () => ({
  ...(await import("./__test-helpers__/toast-mock.js")).toastMock(),
  error: mockToastError,
  errorWithAction: mockErrorWithAction,
}));

// The tab strip is the thing that decides "is this chat on screen", so it is
// mocked rather than driven: a real tab needs a DOM strip and a route.
//
// `tabIdFor` maps a (kind, ref) pair to the tab's OPAQUE server-minted id, which
// a test cannot construct. Returning the ref itself is the identity that keeps
// these assertions readable: the chat id doubles as its own tab id here, so
// `mockActiveTabId` can be compared against a chat id directly the way it was
// before ids became opaque.
const { mockActiveTabId, mockTabIdFor } = vi.hoisted(() => ({
  mockActiveTabId: vi.fn(() => ""),
  mockTabIdFor: vi.fn((_kind: string, ref = "") => ref),
}));
// Spreads the COMPLETE tab-store mock and overrides the three names this file
// observes. Listing only those three worked until the tab projection widened the
// import graph: Browser Mode links ESM for real, so a name any module in the graph
// reaches has to exist even when nothing here calls it, and the failure does not
// name the missing export. See __test-helpers__/tabs-mock.ts.
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
  activateTab: mockActivateTab,
  getActiveTabId: mockActiveTabId,
  tabIdFor: mockTabIdFor,
}));

import { reportFailure, clearFailure, _resetForTest } from "./failure-notice.js";
import { setSessions, setActive } from "./store.js";
import type { Session } from "./types.js";
function makeSession(id: string, name: string): Session {
  return {
    id,
    name,
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
  };
}

/** The last message handed to the toast, from either entry point, "" if none. */
function lastToast(): string {
  const plain = mockToastError.mock.calls.at(-1)?.[0];
  const acted = mockErrorWithAction.mock.calls.at(-1)?.[0];
  return acted ?? plain ?? "";
}

/** The action the last toast carried, undefined if it carried none. */
function lastAction(): { label?: string; onClick: () => void } | undefined {
  return mockErrorWithAction.mock.calls.at(-1)?.[1];
}

/** The retry the last sticky toast carried, undefined if it carried none. */
function lastStickyAction(): { label?: string; onClick: () => void } | undefined {
  return mockToastError.mock.calls.at(-1)?.[1];
}

/** How many toasts were raised, by either entry point. */
function toastCount(): number {
  return mockToastError.mock.calls.length + mockErrorWithAction.mock.calls.length;
}

beforeEach(() => {
  _resetForTest();
  mockToastError.mockClear();
  mockErrorWithAction.mockClear();
  mockDismiss.mockClear();
  mockActivateTab.mockClear();
  // The reader is looking at c1's transcript. The TAB is what decides this, not
  // the store's active chat — see `raise`.
  mockActiveTabId.mockReturnValue("c1");
  setSessions([makeSession("c1", "Fix the parser"), makeSession("c2", "Write the docs")]);
  setActive("c1");
});

describe("failure-notice reports the server's own prose", () => {
  it("shows the reason verbatim for the chat on screen", () => {
    reportFailure("c1", "Too many requests, please wait before trying again.");
    expect(toastCount()).toBe(1);
    expect(lastToast()).toBe("Too many requests, please wait before trying again.");
  });

  // The whole point of routing through the server's prose: a reader must be able
  // to tell a throttle from a capacity refusal, which the old generic "Turn
  // interrupted" could not.
  it("does not prefix or link the chat whose transcript is on screen", () => {
    reportFailure("c1", "at capacity");
    expect(lastToast()).toBe("at capacity");
    expect(lastAction()).toBeUndefined();
  });

  it("substitutes a pointer to the log when the server sent no message", () => {
    reportFailure("c1", "   ");
    expect(lastToast()).toContain("server log");
  });

  // 2 KiB of prose in a corner overlay is a wall. The untruncated reason is on the
  // turn's own divider, so the cap costs the reader nothing.
  it("truncates a very long reason", () => {
    reportFailure("c1", "x".repeat(4000));
    expect(lastToast().length).toBeLessThan(300);
    expect(lastToast().endsWith("\u2026")).toBe(true);
  });
});

describe("failure-notice says which chat it is about", () => {
  it("names and links a chat that is not on screen", () => {
    reportFailure("c2", "at capacity");
    expect(lastToast()).toBe("Write the docs: at capacity");
    expect(lastAction()?.label).toBe("Open chat");
  });

  it("the link activates that chat's tab", () => {
    reportFailure("c2", "at capacity");
    lastAction()?.onClick();
    expect(mockActivateTab).toHaveBeenCalledWith("c2");
  });

  // THE CASE THAT WAS BROKEN. `store.getActiveId()` keeps naming the last chat a
  // reader opened, and nothing clears it when they move to Settings, git, files, a
  // doc or an editor tab — so a failure on that very chat matched "this is the
  // active chat" and arrived with no chat named and nothing to click, on a screen
  // with no transcript in sight. The tab id is what actually answers the question.
  it("names the chat even when it is the active chat but not the active TAB", () => {
    mockActiveTabId.mockReturnValue("__settings__");
    reportFailure("c1", "at capacity");
    expect(lastToast()).toBe("Fix the parser: at capacity");
    expect(lastAction()?.label).toBe("Open chat");
  });

  it("names the chat while the reader is in an editor tab", () => {
    mockActiveTabId.mockReturnValue("editor:/workspace/main.go");
    reportFailure("c1", "at capacity");
    expect(lastToast()).toBe("Fix the parser: at capacity");
  });

  // A chat name is its first prompt truncated to 80 chars server-side, which is a
  // paragraph opener rather than a title, so the prefix takes the leading words.
  it("truncates a long chat name in the prefix", () => {
    setSessions([makeSession("c3", "y".repeat(80))]);
    mockActiveTabId.mockReturnValue("c1");
    reportFailure("c3", "at capacity");
    expect(lastToast().indexOf(": at capacity")).toBeLessThan(45);
    expect(lastToast().endsWith("at capacity")).toBe(true);
  });

  // A chat the store has never heard of (a failure racing chat_created) still
  // reports, and still links: an unnamed toast beats a silent one.
  it("links an unnamed chat without a prefix", () => {
    reportFailure("c-unknown", "at capacity");
    expect(lastToast()).toBe("at capacity");
    expect(lastAction()?.label).toBe("Open chat");
  });

  // activateTab no-ops on an id it does not hold, so a button for a chat with no
  // tab would be a control that does nothing.
  //
  // "No tab" is `tabIdFor` answering "", not `hasTab` answering false. The tab
  // collection became server-owned, so the client no longer asks whether a tab
  // exists and then separately what its id is: one lookup answers both, and its
  // empty string IS the no. Asserting through `hasTab` here would pass against a
  // production path that had stopped consulting it.
  it("offers no link when the chat has no tab", () => {
    mockTabIdFor.mockReturnValue("");
    reportFailure("c2", "at capacity");
    expect(lastToast()).toBe("Write the docs: at capacity");
    expect(lastAction()).toBeUndefined();
  });

  // A workspace-global command (no chat id) names no chat and links nowhere.
  it("neither names nor links a chatless failure", () => {
    reportFailure("", "the tools engine is unreachable");
    expect(lastToast()).toBe("the tools engine is unreachable");
    expect(lastAction()).toBeUndefined();
  });
});

describe("failure-notice dedupes the two channels one failure arrives on", () => {
  // A failed prompt is reported twice by design: the command POST answers 500
  // with the reason in its body AND the SSE error frame carries the identical
  // string. Both must keep reporting (a 400 or a network death carries no SSE
  // frame), so the dedupe is what stops one failure stacking two toasts.
  it("shows one toast when both channels report the same reason", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "at capacity");
    expect(toastCount()).toBe(1);
  });

  it("shows both when the same chat fails two different ways", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "too many requests");
    expect(toastCount()).toBe(2);
  });

  // Two chats failing identically are two failures. Keying the dedupe on the text
  // alone would silence the second.
  it("shows both when two chats fail with the same reason", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c2", "at capacity");
    expect(toastCount()).toBe(2);
  });

  it("shows a repeat once the window has passed", () => {
    vi.useFakeTimers();
    try {
      reportFailure("c1", "at capacity");
      vi.advanceTimersByTime(6_000);
      reportFailure("c1", "at capacity");
      expect(toastCount()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  // The latch has to survive the retraction that precedes every raise, or it only
  // ever covers a chat's FIRST failure: replacing a live toast drops the latch, so
  // latching before that leaves the second failure's twin free to raise a duplicate.
  it("dedupes both channels on a chat's SECOND distinct failure", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "too many requests");
    reportFailure("c1", "too many requests");
    expect(toastCount()).toBe(2);
  });

  // The latch is per chat because a failure's identity is: with one shared slot,
  // an unrelated chat failing in between un-latches the twin still to arrive on
  // the other channel, and the failure reports twice.
  it("dedupes both channels when another chat fails in between", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c2", "too many requests");
    reportFailure("c1", "at capacity");
    expect(toastCount()).toBe(2);
  });
});

describe("failure-notice carries the route's own remedy", () => {
  const remedy = { label: "Open custom instructions", onClick: () => undefined };

  // A routed error's remedy is offered nowhere else, so it takes the one action
  // slot from "Open chat" AND goes sticky. `error(msg, retry)` is toast.ts's sticky
  // path and `errorWithAction` its 12s one, so which mock fires IS the assertion.
  it("names a background chat and carries the remedy, stickily", () => {
    reportFailure("c2", "bad agent front matter", remedy);
    expect(mockErrorWithAction).not.toHaveBeenCalled();
    expect(lastToast()).toBe("Write the docs: bad agent front matter");
    expect(lastStickyAction()?.label).toBe("Open custom instructions");
  });

  it("carries it stickily for the chat that IS on screen, unprefixed", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    expect(mockErrorWithAction).not.toHaveBeenCalled();
    expect(lastToast()).toBe("bad agent front matter");
    expect(lastStickyAction()?.label).toBe("Open custom instructions");
  });

  // A sticky remedy is the only thing on screen saying the runtime is broken, so a
  // later failure on the same chat must not silently dismiss it — which the
  // per-chat replace would do if a remedy-bearing notice were registered as live.
  it("is not retracted by a later failure on the same chat", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    reportFailure("c1", "at capacity");
    expect(mockDismiss).not.toHaveBeenCalled();
    expect(toastCount()).toBe(2);
  });

  // Nothing expires a sticky notice, so a repeat past the dedupe window is the one
  // raise that must retract the copy it repeats: two identical remedies standing
  // side by side indefinitely crowd out the next real error.
  it("replaces its own repeat rather than standing beside it", () => {
    vi.useFakeTimers();
    try {
      reportFailure("c1", "bad agent front matter", remedy);
      vi.advanceTimersByTime(6_000);
      reportFailure("c1", "bad agent front matter", remedy);
      // Two raised, one retracted: one on screen.
      expect(toastCount()).toBe(2);
      expect(mockDismiss).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  // A broken agent file or a dead login is reported per chat, so one fault reaches
  // N chats as N notices. Each names its own chat, so none may retract another —
  // which is why the key carries the chat and not the reason alone. What stops N
  // growing without bound is the toast surface's own sticky cap, not this map.
  it("leaves another chat's remedy standing", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    reportFailure("c2", "bad agent front matter", remedy);
    expect(toastCount()).toBe(2);
    expect(mockDismiss).not.toHaveBeenCalled();
  });

  // The other direction of the same key: a chat's own repeat still replaces itself
  // after another chat has reported the identical failure.
  it("replaces its own repeat after another chat reported the same failure", () => {
    vi.useFakeTimers();
    try {
      reportFailure("c1", "bad agent front matter", remedy);
      reportFailure("c2", "bad agent front matter", remedy);
      vi.advanceTimersByTime(6_000);
      reportFailure("c1", "bad agent front matter", remedy);
      expect(toastCount()).toBe(3);
      expect(mockDismiss).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  // Which is why the handle is held per FAILURE and not per chat: two remedies on
  // one chat are two problems, and the first must still be able to replace its own
  // repeat after the second has arrived.
  it("replaces its own repeat after a different remedy came in between", () => {
    reportFailure("c1", "bad agent front matter", remedy);
    reportFailure("c1", "refresh token expired", { label: "Sign in", onClick: () => undefined });
    reportFailure("c1", "bad agent front matter", remedy);
    expect(toastCount()).toBe(3);
    expect(mockDismiss).toHaveBeenCalledTimes(1);
  });
});

describe("failure-notice retracts a notice that turned out to be wrong", () => {
  // The dead-POST-live-SSE rescue: the prompt POST can die while the turn it
  // started runs on, and actions/chat.ts discovers that up to two seconds later.
  it("dismisses the chat's live toast", () => {
    reportFailure("c1", "connection reset");
    clearFailure("c1");
    expect(mockDismiss).toHaveBeenCalledTimes(1);
  });

  it("is a no-op for a chat with no live toast", () => {
    clearFailure("c1");
    expect(mockDismiss).not.toHaveBeenCalled();
  });

  it("leaves another chat's toast standing", () => {
    reportFailure("c2", "at capacity");
    clearFailure("c1");
    expect(mockDismiss).not.toHaveBeenCalled();
  });

  // A retraction has to drop the dedupe latch too, or the retracted text keeps
  // suppressing itself and a genuine repeat inside the window is silent.
  it("lets the same reason report again after a retraction", () => {
    reportFailure("c1", "connection reset");
    clearFailure("c1");
    reportFailure("c1", "connection reset");
    expect(toastCount()).toBe(2);
  });

  it("replaces its own live toast rather than stacking a second", () => {
    reportFailure("c1", "at capacity");
    reportFailure("c1", "too many requests");
    expect(mockDismiss).toHaveBeenCalledTimes(1);
    expect(toastCount()).toBe(2);
  });
});
