// ---------------------------------------------------------------------------
// The subagent page: the eviction exemption its tab earns, and the repaint its
// prose depends on.
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
  // The page's own host: `paint` bails silently without it, which would make the
  // streaming case below pass on an empty document.
  "subagent-body",
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
const { subagentTabProjectsChat, showSubagent } = await import("./subagent-view.js");
const { blockKey, blockTextSigs } = await import("./store-signals.js");
const { mountedWindow } = await import("./messages-blocks.js");
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

// An open subagent tab projects its chat's transcript, so evicting that chat would
// blank a surface someone deliberately opened. The predicate is answered from the
// RESIDENT blocks — the subtask ids reachable from this chat are the ones on its
// blocks, and a tab for a delegate whose turn is not resident was already rendering
// the not-resident notice, so eviction changes nothing it was showing.
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

// The page's ONE structural input. A text delta writes a per-block signal instead of
// bumping the chat's version, and this page reads those signals with `get` rather
// than `ensure` so it never silences the transcript's own repaint — which leaves it
// depending on the store's signal-absent fallback to bump the version for it. That
// fallback asks whether the block is MOUNTED, and this page files its sinks under a
// synthetic id over a re-indexed slice, so the question is a union over surfaces.
describe("a delegate's prose while the transcript holds no sink", () => {
  it("repaints on a delta to a block only this page has mounted", async () => {
    const chat = "c-stream";
    const msgID = "m-stream";
    store.setSessions([session(chat, [delegateMsg(msgID, "st-1")])]);
    const host = document.getElementById("subagent-body") as HTMLElement;

    showSubagent(chat, "st-1");
    await vi.waitFor(() => {
      expect(host.textContent).toContain("delegate work");
    });

    // The three premises that let the assertion below fail. No transcript view was
    // ever built here, so nothing files a sink under the store's own message id —
    // the background-chat shape. No per-block signal exists, so the page's own
    // subscription cannot carry the delta and the version bump is its only input.
    // And the page holds the delegate's block ALONE, so its own index for it is 0
    // where the store's is 1: a probe asking this render the store's index answers
    // "not mounted" over a block that is on screen.
    expect(mountedWindow(msgID)).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 1))).toBeUndefined();
    expect(host.textContent).not.toContain("parent prose");

    // Ends on a paragraph break, so the incremental markdown parser has no
    // trailing token to hold and the whole delta is on screen or none of it is.
    store.appendChunk(chat, msgID, " and then more\n\n", false, 1, "st-1");

    await vi.waitFor(() => {
      expect(host.textContent).toContain("and then more");
    });
  });
});

// A switch drops the previous delegate's page. It has to drop that page's RENDER with
// it, or the render outlives its DOM with its text sinks intact and the repaint gate
// keeps answering "still mounted" for blocks nobody can see — one full transcript pass
// per delta, which is the exact cost the gate exists to remove.
describe("switching delegates releases the page's render", () => {
  it("stops answering the repaint gate for the delegate the reader left", async () => {
    const chat = "c-switch";
    const msgID = "m-switch";
    store.setSessions([
      session(chat, [
        {
          id: msgID,
          role: "assistant",
          ts: 1,
          content: "",
          blocks: [
            { type: "text", text: "parent prose" },
            { type: "text", text: "first delegate", agent_subtask_id: "sw-A" },
            { type: "text", text: "second delegate", agent_subtask_id: "sw-B" },
          ],
        } as Message,
      ]),
    ]);
    const host = document.getElementById("subagent-body") as HTMLElement;

    showSubagent(chat, "sw-A");
    await vi.waitFor(() => {
      expect(host.textContent).toContain("first delegate");
    });

    // The SWITCH is the property. One delegate cannot express it: the release only asks
    // for the wrong pair where the page's own key and the visible subtask disagree,
    // which is true for exactly one paint after a switch.
    showSubagent(chat, "sw-B");
    await vi.waitFor(() => {
      expect(host.textContent).toContain("second delegate");
    });
    expect(host.textContent).not.toContain("first delegate");

    // Two premises, or the assertion below passes for someone else's reason. No
    // transcript view exists here, so nothing files a sink under the store's own message
    // id; and A's block has no per-block signal, so the gate is the only thing left that
    // can schedule a pass for it.
    expect(mountedWindow(msgID)).toBeUndefined();
    expect(blockTextSigs.get(blockKey(msgID, 1))).toBeUndefined();

    const version = store.messagesVersionOf(chat);
    const before = version.peek();
    store.appendChunk(chat, msgID, " more from A", false, 1, "sw-A");
    await Promise.resolve();

    expect(version.peek()).toBe(before);
  });
});
