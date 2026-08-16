// @vitest-environment happy-dom
// Focused test for createPlannerSession — the ?agent=planner share shortcut's
// server-side handoff. chat.ts has a heavy import graph, so every direct
// dependency is mocked at the first hop; the store mock is stateful for
// activeId so getActiveId() returns the id createSession set. The assertion:
// createPlannerSession dispatches chat.set_mode with modeID "plan" for the new
// chat (mirrors role-picker's selectMode).
import { describe, it, expect, vi, beforeEach } from "vitest";

const { setModeDispatch, forkDispatch, submitPromptMock, messagesEl } = vi.hoisted(() => ({
  setModeDispatch: vi.fn(),
  forkDispatch: vi.fn(),
  submitPromptMock: vi.fn(),
  messagesEl: document.createElement("div"),
}));

let activeId = "";

vi.mock("./store.js", () => ({
  getActiveId: () => activeId,
  getActive: vi.fn(() => undefined),
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  setActive: vi.fn((id: string) => {
    activeId = id;
  }),
  upsertHeader: vi.fn(),
  markGhostChat: vi.fn(),
  isGhostChat: vi.fn(() => false),
  contextSizeFor: vi.fn(() => 0),
  defaultUsage: vi.fn(() => ({
    context_pct: 0,
    context_size: 0,
    credits: 0,
    turn_count: 0,
    last_turn_ms: 0,
    has_real_data: false,
  })),
  activeSession: { value: undefined },
  removeChat: vi.fn(),
}));
vi.mock("./store-load.js", () => ({ loadList: vi.fn(), loadMessages: vi.fn() }));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("./tabs.js", () => ({
  openTab: vi.fn(),
  activateTab: vi.fn(),
  hasTab: vi.fn(() => false),
  getActiveTabId: vi.fn(() => ""),
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  setTabIcon: vi.fn(),
  TAB_VIEWS: { chat: "#chat-view" },
}));
vi.mock("./skeleton.js", () => ({ chatSkeleton: vi.fn(() => document.createElement("div")) }));
vi.mock("@cplieger/ui-primitives/skeleton", () => ({
  skeletonTiming: vi.fn(() => ({ commit: vi.fn(), cancel: vi.fn() })),
}));
vi.mock("./picker.js", () => ({ showModelPicker: vi.fn(), hideModelPicker: vi.fn() }));
vi.mock("./messages.js", () => ({
  mountChatView: vi.fn(),
  setLoadMore: vi.fn(),
  scrollToBottom: vi.fn(),
  // Needed even though no case below reaches it: activateChatView's success
  // branch calls it, so omitting it leaves a TypeError waiting for whichever
  // future test does exercise that path.
  loadTurnRail: vi.fn(),
}));
vi.mock("./attachments.js", () => ({ addAttachment: vi.fn() }));
// The composer's per-chat state owns real DOM (the textarea) and a debounced
// action dispatch; chat.ts only has to call its save/restore pair in the right
// order, which the mock records.
vi.mock("./composer-state.js", () => ({
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  seedComposerDraft: vi.fn(),
  flushComposerDraft: vi.fn(),
  dropComposerState: vi.fn(),
}));
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./model-switcher.js", () => ({ applyLocalModel: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./roles.js", () => ({ iconForMode: vi.fn(() => "") }));
vi.mock("./submit.js", () => ({ submitPrompt: submitPromptMock }));
// $.messages is a real element so a listener registered on it could be driven.
// Nothing in chat.ts registers one any more — see "no transcript context menu"
// below, which is what that element is here to witness.
vi.mock("./dom.js", () => ({
  $: { messages: messagesEl, promptInput: { focus: () => undefined } },
}));
vi.mock("./retention.js", () => ({ isRetentionEnabled: vi.fn(() => false) }));
vi.mock("./bus.js", () => ({ onBus: vi.fn(), BUS_ACTIVATE_CHAT: "activate-chat" }));
vi.mock("./actions/chat.js", () => ({
  closeChat: { dispatch: vi.fn() },
  deleteChat: { dispatch: vi.fn() },
  restoreChat: { dispatch: vi.fn() },
  setMode: { dispatch: setModeDispatch },
  forkChat: { dispatch: forkDispatch },
}));

import * as chatModule from "./chat.js";
import { createPlannerSession, openChatTab, openTangentChat, openPreviousSession } from "./chat.js";
import { openTab } from "./tabs.js";
import { get, removeChat, upsertHeader, markGhostChat, isGhostChat } from "./store.js";
import { loadMessages } from "./store-load.js";
import { seedComposerDraft } from "./composer-state.js";
import { isRetentionEnabled } from "./retention.js";
import { closeChat, deleteChat } from "./actions/chat.js";

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(get).mockReturnValue(undefined);
  vi.mocked(isRetentionEnabled).mockReturnValue(false);
  vi.mocked(isGhostChat).mockReturnValue(false);
  vi.mocked(loadMessages).mockResolvedValue(true);
  activeId = "";
});

describe("createPlannerSession", () => {
  it("dispatches chat.set_mode with modeID 'plan' for the newly created chat", () => {
    createPlannerSession();
    expect(setModeDispatch).toHaveBeenCalledTimes(1);
    const arg = setModeDispatch.mock.calls[0]?.[0] as { chatID: string; modeID: string };
    expect(arg.modeID).toBe("plan");
    // createSession() sets the new chat active synchronously, so the dispatch
    // targets a real (non-empty) chat id.
    expect(arg.chatID).not.toBe("");
  });
});

// ---------------------------------------------------------------------------
// Activating a chat with NO messages. It still takes the model-picker branch, so
// it never called loadMessages — and the draft rides that single-chat GET, on
// purpose (it must travel on neither the list nor a chat_updated frame). A chat
// can be PERSISTED with zero messages, because set_mode and set_effort both
// auto-create the record before the first prompt, so a mode pick plus half a
// typed message plus a reload came back to an empty box with the draft sitting on
// the server: the exact case server-side drafts exist for.
// ---------------------------------------------------------------------------

describe("the draft of a chat with no messages", () => {
  /** A persisted zero-message chat. `model` is empty so the context-size branch
   *  above it (which calls setModel) stays out of the way. */
  function emptyChat(): never {
    return {
      id: "c-empty",
      model: "",
      message_count: 0,
      messages: [],
      usage: { context_size: 0 },
    } as never;
  }

  /** Activate through the History row path, the one exported caller that reaches
   *  activateChatView for a chat the store already holds. */
  function activate(): void {
    openPreviousSession({ chat_id: "c-empty", session_id: "s1", title: "t", updated_at: 1 });
  }

  it("fetches the record so the stored draft can be adopted", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    activate();
    await vi.waitFor(() => {
      expect(seedComposerDraft).toHaveBeenCalledWith("c-empty");
    });
    expect(loadMessages).toHaveBeenCalledWith("c-empty");
  });

  it("asks nothing about a ghost chat, whose id the server has never seen", async () => {
    // Every New chat click lands here, and a GET on a client-minted id 404s.
    vi.mocked(get).mockReturnValue(emptyChat());
    vi.mocked(isGhostChat).mockReturnValue(true);
    activate();
    await Promise.resolve();
    expect(loadMessages).not.toHaveBeenCalled();
    expect(seedComposerDraft).not.toHaveBeenCalled();
  });

  it("seeds nothing when the fetch fails", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    vi.mocked(loadMessages).mockResolvedValue(false);
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
    expect(seedComposerDraft).not.toHaveBeenCalled();
  });

  it("seeds nothing once the user has moved to another chat", async () => {
    // The composer is shared, so a seed landing after a switch would write the
    // outgoing chat's draft into the incoming chat's box.
    vi.mocked(get).mockReturnValue(emptyChat());
    vi.mocked(loadMessages).mockImplementation(async () => {
      activeId = "c-other";
      return true;
    });
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
    expect(seedComposerDraft).not.toHaveBeenCalled();
  });

  it("marks a client-minted chat as a ghost when it is seeded", () => {
    createPlannerSession();
    const id = (vi.mocked(upsertHeader).mock.calls.at(-1)?.[0] as { id: string }).id;
    expect(markGhostChat).toHaveBeenCalledWith(id);
  });
});

describe("openChatTab onClose retention gate", () => {
  // Capture the onClose callback openChatTab hands to the tab store.
  function captureOnClose(id: string): () => void {
    openChatTab(id, "chat");
    const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { onClose?: () => void };
    return spec.onClose ?? ((): void => undefined);
  }

  it("persists nothing on close when retention is ENABLED (N>0), but kills the work", () => {
    // There is no archive step: "archived" is computed from the chat's age
    // against the retention window, so the chat FILE stays exactly as it was.
    // The PROCESSES do not (user decision: the x kills the turn, the chat's
    // runs, the bridge) — that is close_chat, fired before the local removal.
    vi.mocked(get).mockReturnValue({ message_count: 3 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(true);
    captureOnClose("c-closed")();
    expect(closeChat.dispatch).toHaveBeenCalledWith("c-closed");
    expect(removeChat).toHaveBeenCalledWith("c-closed");
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });

  it("deletes a non-empty chat permanently on close when retention is DISABLED (0 = no retention)", () => {
    vi.mocked(get).mockReturnValue({ message_count: 3 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(false);
    captureOnClose("c-ephemeral")();
    // 0 = ephemeral: closing loses the chat by design (not a data-loss bug).
    expect(deleteChat.dispatch).toHaveBeenCalledWith("c-ephemeral");
  });

  it("removes a zero-message chat locally regardless of retention (never persisted)", () => {
    vi.mocked(get).mockReturnValue({ message_count: 0 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(false);
    captureOnClose("c-empty")();
    expect(removeChat).toHaveBeenCalledWith("c-empty");
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The tangent: a real chat opened as a sub-tab of the one it came from, whose
// context is the parent's REAL context via a session fork.
// ---------------------------------------------------------------------------

function lastSpec(): {
  id: string;
  parentId?: string;
  owns?: boolean;
  kind: string;
  onClose?: () => void;
} {
  return vi.mocked(openTab).mock.calls.at(-1)?.[0] as never;
}

describe("openTangentChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    activeId = "";
    vi.mocked(get).mockReturnValue({ model: "parent-model" } as never);
  });

  it("opens the new chat as a sub-tab of its parent", () => {
    openTangentChat("c-parent");
    const spec = lastSpec();
    expect(spec.parentId).toBe("c-parent");
    expect(spec.kind).toBe("chat");
    expect(spec.id).not.toBe("c-parent");
  });

  // The fork is what persists the chat AND what carries the context, so it must
  // fire for the NEW chat naming the parent. Without the parent id the server
  // has nothing to fork.
  it("dispatches chat.fork for the new chat, naming its parent", () => {
    openTangentChat("c-parent");
    expect(forkDispatch).toHaveBeenCalledTimes(1);
    const arg = forkDispatch.mock.calls[0]?.[0] as { chatID: string; parentChatID: string };
    expect(arg.chatID).toBe(lastSpec().id);
    expect(arg.parentChatID).toBe("c-parent");
  });

  // A selection chooses nothing once the whole conversation is inherited, so
  // there is no seeded prompt any more. This is the assertion that fails if the
  // old selection-seeding path is reintroduced.
  it("seeds no prompt: the fork carries the context, not a quoted phrase", () => {
    openTangentChat("c-parent");
    expect(submitPromptMock).not.toHaveBeenCalled();
  });

  // It OWNS its bridge, so the tab keeps the default `owns` and its close tears
  // the tangent down like any other chat tab. `owns: false` would be wrong — a
  // forked session is this tab's own work, not a view over another chat's.
  it("owns its bridge", () => {
    openTangentChat("c-parent");
    expect(lastSpec().owns).toBeUndefined();
    expect(lastSpec().onClose).toBeTypeOf("function");
  });

  // Model rides the local seed so the tab and picker read right immediately; mode
  // and effort are the SERVER's to copy off the parent record, which is why no
  // set_mode is dispatched here any more.
  it("seeds the parent's model locally and leaves mode to the server", () => {
    vi.mocked(get).mockReturnValue({ model: "parent-model", current_mode_id: "plan" } as never);
    openTangentChat("c-parent");
    expect(vi.mocked(upsertHeader).mock.calls.at(-1)?.[0]).toMatchObject({
      model: "parent-model",
    });
    expect(setModeDispatch).not.toHaveBeenCalled();
  });

  it("does nothing when the parent is unknown", () => {
    vi.mocked(get).mockReturnValue(undefined);
    openTangentChat("c-parent");
    expect(openTab).not.toHaveBeenCalled();
    expect(forkDispatch).not.toHaveBeenCalled();
  });

  it("does nothing for an empty parent id", () => {
    openTangentChat("");
    expect(openTab).not.toHaveBeenCalled();
    expect(forkDispatch).not.toHaveBeenCalled();
  });
});

// The transcript's right-click entry is GONE with the selection it read. A
// tangent inherits the whole conversation, so "I selected these words" does not
// mean "branch this conversation", and a menu entry that silently inherited
// everything from a phrase-scoped gesture was misleading. The `+` menu is the
// door.
describe("no transcript context menu", () => {
  it("exports no initTranscriptContextMenu", () => {
    expect(chatModule).not.toHaveProperty("initTranscriptContextMenu");
  });

  // The listener is what the export wired, so its absence is the behavioural
  // half: a right-click on the transcript is the native menu's again. Asserting
  // on defaultPrevented rather than on a mock, because there is no longer a
  // context-menu module for this file to mock.
  it("leaves a transcript right-click to the native menu", () => {
    activeId = "c-active";
    const e = new MouseEvent("contextmenu", { bubbles: true, cancelable: true });
    messagesEl.dispatchEvent(e);
    expect(e.defaultPrevented).toBe(false);
  });
});
