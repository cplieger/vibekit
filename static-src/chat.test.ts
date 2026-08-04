// @vitest-environment happy-dom
// Focused test for createPlannerSession — the ?agent=planner share shortcut's
// server-side handoff. chat.ts has a heavy import graph, so every direct
// dependency is mocked at the first hop; the store mock is stateful for
// activeId so getActiveId() returns the id createSession set. The assertion:
// createPlannerSession dispatches chat.set_mode with modeID "plan" for the new
// chat (mirrors role-picker's selectMode).
import { describe, it, expect, vi, beforeEach } from "vitest";

const { setModeDispatch } = vi.hoisted(() => ({
  setModeDispatch: vi.fn(),
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
vi.mock("./attachments.js", () => ({ addAttachment: vi.fn(), clearAttachments: vi.fn() }));
vi.mock("./session-context.js", () => ({ setCurrentModel: vi.fn(), getLastModel: () => "auto" }));
vi.mock("./model-switcher.js", () => ({ applyLocalModel: vi.fn() }));
vi.mock("./context-ui.js", () => ({ refreshContextUI: vi.fn() }));
vi.mock("./roles.js", () => ({ iconForMode: vi.fn(() => "") }));
vi.mock("./dom.js", () => ({ $: {} }));
vi.mock("./retention.js", () => ({ isRetentionEnabled: vi.fn(() => false) }));
vi.mock("./bus.js", () => ({ onBus: vi.fn(), BUS_ACTIVATE_CHAT: "activate-chat" }));
vi.mock("./actions/chat.js", () => ({
  closeChat: { dispatch: vi.fn() },
  deleteChat: { dispatch: vi.fn() },
  restoreChat: { dispatch: vi.fn() },
  setMode: { dispatch: setModeDispatch },
}));

import { createPlannerSession, openChatTab } from "./chat.js";
import { openTab } from "./tabs.js";
import { get, removeChat } from "./store.js";
import { isRetentionEnabled } from "./retention.js";
import { closeChat, deleteChat } from "./actions/chat.js";

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(get).mockReturnValue(undefined);
  vi.mocked(isRetentionEnabled).mockReturnValue(false);
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
