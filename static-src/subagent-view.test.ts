// ---------------------------------------------------------------------------
// The subagent-tab eviction exemption: an open subagent tab projects its
// chat's transcript (the page is a projection over the chat store's blocks),
// so evicting that chat would blank a surface someone deliberately opened.
//
// The predicate is answered from the RESIDENT blocks — the subtask ids
// reachable from this chat are the ones on its blocks, and a tab for a
// delegate whose turn is not resident was already rendering the not-resident
// notice, so eviction changes nothing it was showing.
// ---------------------------------------------------------------------------

import { describe, it, expect, beforeEach, vi } from "vitest";
import type { Message, Session } from "./types.js";

// tabs.ts's real graph reads the shared DOM registry at module scope, and
// `byId` throws on a missing element — so the hosts exist before any import.
for (const id of [
  "messages",
  "messages-wrap",
  "messages-wrap-outer",
  "chat-view",
  "scroll-bottom",
  "send-btn",
  "prompt-input",
  "tab-strip",
]) {
  const d = document.createElement(id === "prompt-input" ? "textarea" : "div");
  d.id = id;
  document.body.appendChild(d);
}

// A spy mock rather than a factory: the graph behind subagent-view imports a
// wide slice of tabs.ts's surface, and a hand-kept name list here would rot.
// The spy keeps every export real and lets the cases below steer `hasTab`.
vi.mock("./tabs.js", { spy: true });
vi.mock("./scroll.js", () => import("./__test-helpers__/scroll-mock.js").then((m) => m.scrollMock));

const store = await import("./store.js");
const { hasTab } = await import("./tabs.js");
const { subagentTabProjectsChat } = await import("./subagent-view.js");
const mockHasTab = vi.mocked(hasTab);

function session(id: string, messages: Message[]): Session {
  return {
    id,
    name: id,
    messages,
    message_count: messages.length,
    has_more: false,
    thinking: false,
    working_label: "",
  } as unknown as Session;
}

function delegateMsg(id: string, subtask: string): Message {
  return {
    id,
    role: "assistant",
    ts: 1,
    content: "",
    blocks: [
      { type: "text", text: "parent prose" },
      { type: "text", text: "delegate work", agent_subtask_id: subtask },
    ],
  } as Message;
}

beforeEach(() => {
  mockHasTab.mockReset();
  mockHasTab.mockReturnValue(false);
});

describe("subagentTabProjectsChat", () => {
  it("exempts a chat with an open subagent tab for one of its delegates", () => {
    store.setSessions([session("c1", [delegateMsg("m1", "st-1")])]);
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c1/st-1");

    expect(subagentTabProjectsChat("c1")).toBe(true);
    // The lookup is by the tab's own composite ref, chatID/subtaskID.
    expect(mockHasTab).toHaveBeenCalledWith("subagent", "c1/st-1");
  });

  it("exempts nothing when no subagent tab is open", () => {
    store.setSessions([session("c1", [delegateMsg("m1", "st-1")])]);
    expect(subagentTabProjectsChat("c1")).toBe(false);
  });

  it("exempts nothing for a chat whose blocks carry no delegate", () => {
    store.setSessions([
      session("c1", [
        { id: "m1", role: "assistant", ts: 1, content: "", blocks: [{ type: "text", text: "x" }] },
      ] as Message[]),
    ]);
    mockHasTab.mockReturnValue(true); // even with tabs open, no subtask to ask about
    expect(subagentTabProjectsChat("c1")).toBe(false);
    expect(mockHasTab).not.toHaveBeenCalled();
  });

  it("answers false for an unknown chat", () => {
    store.setSessions([]);
    expect(subagentTabProjectsChat("c-missing")).toBe(false);
  });

  it("does not cross chats: the ref carries the asking chat's id", () => {
    store.setSessions([
      session("c1", [delegateMsg("m1", "st-1")]),
      session("c2", [delegateMsg("m2", "st-1")]),
    ]);
    // A tab open for c2's delegate must not exempt c1, subtask id collision or
    // not — the ref is chat-scoped.
    mockHasTab.mockImplementation((kind, ref) => kind === "subagent" && ref === "c2/st-1");
    expect(subagentTabProjectsChat("c1")).toBe(false);
    expect(subagentTabProjectsChat("c2")).toBe(true);
  });
});
