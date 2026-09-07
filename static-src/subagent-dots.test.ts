// ---------------------------------------------------------------------------
// The activity dot on a SUBAGENT tab's row.
//
// Five rules carry the feature and each fails silently: every OPEN subagent tab
// gets a dot painted from its own invocation; a `tool_call_update` repaints it with
// NO tab mutation behind it (the launching chat's transcript signal is the
// dependency, and it is the one that turns a spinning delegate into a settled one);
// a tab that lands AFTER its chat's messages paints on the tab-set bump alone; a
// delegate with nothing resident paints "" rather than claiming a state; and an
// unchanged status must still REACH the writer, because a row rebuilt since the
// last pass has to be repainted and `recordDotStatus` is what suppresses the
// redundant `dotVersion` bump — not the caller.
//
// It mocks `./tabs.js` and uses the REAL store, which is a deliberate departure
// from the brief's "mock ./store.js": the plan's own requirement is to exercise the
// real `subagentStatusFor` rather than re-implement the mapping in a fake, and the
// dependency under test is production's own version bump. A hand-bumped fake signal
// would assert the fake. So `setSessions` installs the transcript and
// `upsertToolCall` delivers the update through the same function
// `handlers/messages.ts` calls, which is what makes the repaint case mean anything.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import { signal } from "@cplieger/reactive";
import { setSessions, upsertToolCall, get } from "./store.js";
import { subagentRef } from "./tab-materialize.js";
import type { Message, Session, ToolCall, ToolStatus } from "./types.js";

const m = {
  painted: [] as { id: string; status: string }[],
  /** The subagent refs the projection holds, in order, as `openSubagentRefs`
   *  answers them. */
  refs: [] as string[],
};

/** The tab SET's version, bumped by the fake exactly where a committed tab
 *  mutation bumps the real one — the dependency that paints a row landing after
 *  its chat's messages. */
const tabsVersion = signal(0);

vi.mock("./tabs.js", () => ({
  // A TRACKED read, like production's: the effect's re-run on a tab mutation is
  // half of what is under test.
  openSubagentRefs: vi.fn(() => {
    void tabsVersion.value;
    return [...m.refs];
  }),
  // The projection's own lookup. The fake keeps a readable id so the assertions
  // below name something a reader can follow, and it answers ONLY for a ref the
  // set holds — which is what makes "a closed tab is not painted" observable.
  tabIdFor: vi.fn((_kind: string, ref: string) => (m.refs.includes(ref) ? `sub:${ref}` : "")),
  setTabStatus: vi.fn((id: string, status: string) => {
    m.painted.push({ id, status });
  }),
}));

const { installSubagentDotSubscriber } = await import("./subagent-dots.js");

/** Bump the mocked tab set the way a committed tab mutation does. */
function tabsChanged(): void {
  tabsVersion.value = tabsVersion.value + 1;
}

/** Let the store's microtask coalescer flush. A macrotask, so every pending
 *  microtask has drained by the time it resolves. */
function tick(): Promise<void> {
  return new Promise((r) => setTimeout(r, 0));
}

function invocation(subtaskID: string, status: ToolStatus, id = `tc-${subtaskID}`): ToolCall {
  return {
    id,
    // One of the four titles `roles.ts` `isSubagentInvocation` accepts. The dot
    // resolves through that predicate, so a nested call of the same delegate must
    // not be mistaken for its invocation.
    title: "Sub-agent: introspect",
    kind: "other",
    status,
    agent_subtask_id: subtaskID,
    ts: 1,
  };
}

function chatWith(chatID: string, calls: readonly ToolCall[]): Session {
  const messages: Message[] = [
    {
      id: `m-${chatID}`,
      role: "assistant",
      ts: 1,
      content: "",
      blocks: calls.map((tc) => ({
        type: "tool_use" as const,
        tool_call_id: tc.id,
        agent_subtask_id: tc.agent_subtask_id ?? "",
      })),
      tool_calls: [...calls],
    },
  ];
  return {
    id: chatID,
    name: chatID,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: { context_pct: 0, context_size: 0, credits: 0, turns: 0, last_turn_ms: 0 },
    messages,
    message_count: messages.length,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  } as unknown as Session;
}

let installed = false;

beforeEach(() => {
  m.painted.length = 0;
  m.refs.length = 0;
  setSessions([]);
  if (!installed) {
    installSubagentDotSubscriber();
    installed = true;
  }
  tabsChanged();
  m.painted.length = 0;
});

describe("a delegate's tab paints its own invocation's state", () => {
  it.each([
    ["in_progress", "working"],
    ["pending", "working"],
    ["completed", "done"],
    ["failed", "failed"],
  ])("paints %s as %s", (status, want) => {
    setSessions([chatWith("c1", [invocation("task-1", status as ToolStatus)])]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({ id: `sub:${subagentRef("c1", "task-1")}`, status: want });
  });

  it("paints nothing at all when no invocation is resident", () => {
    // The chat is open and its window holds no invocation for this delegate — a
    // boot-restored tab whose chat has not been fetched, or a turn evicted from
    // the paginated window. "" is the honest answer: not knowing is different from
    // knowing nothing is happening, and the reserved slot stays invisible.
    setSessions([chatWith("c1", [])]);
    m.refs.push(subagentRef("c1", "task-missing"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c1", "task-missing")}`,
      status: "",
    });
  });

  it("ignores a nested call of the same delegate, so a running tool cannot mask a finished one", () => {
    // Every call the delegate MADE shares its subtask id; only the invocation
    // carries an invocation title. Without the predicate the dot would report
    // whichever call happened to come first in the array.
    const nested: ToolCall = {
      id: "tc-nested",
      title: "Read file",
      kind: "read",
      status: "in_progress",
      agent_subtask_id: "task-1",
      ts: 1,
    };
    setSessions([chatWith("c1", [nested, invocation("task-1", "completed")])]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)?.status).toBe("done");
  });
});

describe("the launching chat's transcript is the repaint dependency", () => {
  it("repaints in_progress to failed on a tool_call_update, with no tab mutation", async () => {
    setSessions([chatWith("c1", [invocation("task-1", "in_progress")])]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)?.status).toBe("working");
    const before = m.painted.length;

    // The real ingest path, through the same function `handlers/messages.ts` calls
    // for a `tool_call_update` frame. Nothing touches the tab set here, which is
    // the whole case: without the per-chat version read the dot would sit on
    // `working` until some unrelated tab mutation happened along.
    upsertToolCall("c1", "m-c1", invocation("task-1", "failed"), 0);
    await tick();

    expect(m.painted.length).toBeGreaterThan(before);
    expect(m.painted.at(-1)?.status).toBe("failed");
  });

  it("repaints a delegate in a chat the reader is not looking at", async () => {
    // The property the whole feature rests on: `upsertToolCall` is called for any
    // chat with a store row, so a background delegate's dot is correct. Two chats,
    // and the one that moves is not the one that was painted first.
    setSessions([
      chatWith("c1", [invocation("task-1", "completed")]),
      chatWith("c2", [invocation("task-2", "in_progress")]),
    ]);
    m.refs.push(subagentRef("c1", "task-1"), subagentRef("c2", "task-2"));
    tabsChanged();
    m.painted.length = 0;

    upsertToolCall("c2", "m-c2", invocation("task-2", "completed"), 0);
    await tick();

    expect(m.painted).toContainEqual({ id: `sub:${subagentRef("c2", "task-2")}`, status: "done" });
  });

  it("paints a tab that landed AFTER its chat's messages, on the tab-set bump alone", () => {
    // The ordering every door except a deep link has: the card's footer link is
    // only there because the delegate's blocks are resident, so the transcript is
    // in place and the tab arrives a round trip later. No transcript change follows
    // it, so the tab-set dependency is the only thing that can paint the row.
    setSessions([chatWith("c1", [invocation("task-1", "completed")])]);
    expect(m.painted).toEqual([]);

    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c1", "task-1")}`,
      status: "done",
    });
  });

  it("re-applies an unchanged status, because a rebuilt row has to be repainted", async () => {
    // `setTabStatus` must be REACHED on every pass. A caller that skipped an
    // unchanged value would leave a row rebuilt since the last write showing
    // whatever the factory painted; suppressing the redundant `dotVersion` bump is
    // `recordDotStatus`'s job, one layer down.
    setSessions([chatWith("c1", [invocation("task-1", "in_progress")])]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    const after = m.painted.length;

    // A transcript change that leaves the delegate's own status alone.
    upsertToolCall("c1", "m-c1", invocation("task-1", "in_progress"), 0);
    await tick();

    expect(m.painted.length).toBeGreaterThan(after);
    expect(m.painted.at(-1)?.status).toBe("working");
  });
});

describe("the tab set bounds the work, so nothing has to be swept", () => {
  it("stops painting a closed tab", () => {
    setSessions([chatWith("c1", [invocation("task-1", "in_progress")])]);
    m.refs.push(subagentRef("c1", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)?.status).toBe("working");

    m.refs.length = 0;
    tabsChanged();
    m.painted.length = 0;
    // A later transcript change for the same chat must reach no row at all: the
    // pass enumerates the tab set, so a closed tab is not visited and there is no
    // tracked set left holding it.
    tabsChanged();
    expect(m.painted).toEqual([]);
  });

  it("paints every open tab of one chat, not just the first", () => {
    setSessions([
      chatWith("c1", [invocation("task-1", "in_progress"), invocation("task-2", "failed")]),
    ]);
    m.refs.push(subagentRef("c1", "task-1"), subagentRef("c1", "task-2"));
    tabsChanged();
    expect(m.painted).toContainEqual({
      id: `sub:${subagentRef("c1", "task-1")}`,
      status: "working",
    });
    expect(m.painted).toContainEqual({
      id: `sub:${subagentRef("c1", "task-2")}`,
      status: "failed",
    });
  });
});

describe("a ref this client cannot resolve degrades rather than throwing", () => {
  it("paints nothing and throws nothing for a malformed ref", () => {
    // A ref arrives from the PERSISTED collection, so a bad one can reach any
    // device. `parseSubagentRef` answers two empty halves and the pass skips it —
    // the row keeps the factory's fallback name and its invisible reserved slot.
    setSessions([chatWith("c1", [invocation("task-1", "completed")])]);
    m.refs.push("no-separator-here");
    expect(() => tabsChanged()).not.toThrow();
    expect(m.painted).toEqual([]);
  });

  it("paints nothing for a WORKFLOW STEP's subtask id", () => {
    // A hand-crafted deep link can name `wf:<workflowId>:<nodePath>`. The launch
    // call is `run_workflow`, which `isSubagentInvocation` rejects, so the scan
    // resolves nothing and the dot degrades to "" rather than reporting a run's
    // state on a row that names a step.
    const step: ToolCall = {
      id: "tc-run",
      title: "run_workflow",
      kind: "other",
      status: "in_progress",
      agent_subtask_id: "wf:wf_1:root/iter-1",
      ts: 1,
    };
    setSessions([chatWith("c1", [step])]);
    m.refs.push(subagentRef("c1", "wf:wf_1:root/iter-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c1", "wf:wf_1:root/iter-1")}`,
      status: "",
    });
  });

  it("paints nothing for a chat this client holds no row for", () => {
    // A deep link to a delegate in a conversation that is not open here. The
    // effect must still visit the ref, or the row would never paint when that
    // chat's messages arrive.
    expect(get("c-unknown")).toBeUndefined();
    m.refs.push(subagentRef("c-unknown", "task-1"));
    tabsChanged();
    expect(m.painted.at(-1)).toEqual({
      id: `sub:${subagentRef("c-unknown", "task-1")}`,
      status: "",
    });
  });
});
