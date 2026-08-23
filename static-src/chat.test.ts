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
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  setModel: undefined,
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
  // The real predicate, transcribed: the model-picker branch keys on it, so a
  // stub returning a constant would send every fixture down one arm.
  isEmptyChat: (s: { message_count: number; messages: unknown[] } | undefined) =>
    s === undefined || (s.message_count === 0 && s.messages.length === 0),
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
  // The dot's seed at tab creation, plus the clear chat.ts no longer calls. The
  // mock keeps `clearTurnDone` so the absence assertion below has something to
  // assert against; the module is mocked wholesale, so both are inert here.
  tabStatusFor: vi.fn(() => ""),
  clearTurnDone: vi.fn(),
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
  setTabTooltip: vi.fn(),
  TAB_VIEWS: { chat: "#chat-view" },
}));
// The chat tab's activity dot asks the dock whether this chat holds an
// unanswered decision. Mocked so the suite does not pull in the three card
// builders behind the real module for a boolean.
vi.mock("./decision-dock.js", () => ({
  hasPendingDecision: vi.fn(() => false),
  dropDecisions: vi.fn(),
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
vi.mock("./roles.js", () => ({
  iconForMode: vi.fn(() => ""),
  labelForMode: vi.fn((id: string) => (id === "plan" ? "Plan" : id)),
}));
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
import {
  createPlannerSession,
  openChatTab,
  openTangentChat,
  openPreviousSession,
  installStoreSubscribers,
} from "./chat.js";
import { openTab, hasTab, setTabTooltip } from "./tabs.js";
import { dropDecisions } from "./decision-dock.js";
import {
  get,
  getSessions,
  removeChat,
  upsertHeader,
  markGhostChat,
  isGhostChat,
  clearTurnDone,
} from "./store.js";
import { loadList, loadMessages } from "./store-load.js";
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

  // The chat's unanswered asks go with the tab. Closing cancels the turn and the
  // chat's runs server-side, so nothing left here is live — and because the dock's
  // queue is keyed by chat id, a queue left behind was RESURRECTED by reopening
  // the same id: the card came back and the tab dot said the chat needed a
  // decision that no longer existed.
  // A `done` dot is NOT settled by opening the chat, and this pins the absence.
  // Until 2026-08 activation cleared it, because the mark meant "finished while you
  // were away" — paired with a latch that skipped the watched chat, which together
  // made the dot fall back to hollow `idle` at the exact moment a turn completed in
  // front of the reader. The mark now means "the last turn finished" and stands
  // until the next one, matching web-terminal-kiro's engine-side latch. What keeps a
  // read chat out of the title count is attention.ts's acknowledgement pass, which
  // does not touch the store.
  it("does not settle the finished-turn mark when the chat is activated", () => {
    openChatTab("c-open", "chat");
    const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { onShow?: () => void };
    spec.onShow?.();
    expect(clearTurnDone).not.toHaveBeenCalled();
  });

  it("drops the chat's unanswered asks on close, in every retention mode", () => {
    for (const retention of [true, false]) {
      vi.clearAllMocks();
      vi.mocked(get).mockReturnValue({ message_count: 3 } as never);
      vi.mocked(isRetentionEnabled).mockReturnValue(retention);
      captureOnClose("c-closed")();
      expect(dropDecisions).toHaveBeenCalledWith("c-closed");
    }
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

// The activity dot took the slot the per-mode role glyph used to hold, and for a
// BACKGROUND chat that glyph was the only place a role read out at all — the mode
// pill and its picker are active-chat only. The tooltip is where the role went:
// no element, no width, no second visual vocabulary in the 9px column. It is
// pointer-only, so it is a convenience rather than a full restoration.
describe("the chat tab's tooltip carries the mode as well as the activity", () => {
  function driveEffect(over: Record<string, unknown>): void {
    const s = { id: "c1", name: "Fix the parser", available_modes: [], ...over };
    vi.mocked(getSessions).mockReturnValue([s as never]);
    vi.mocked(hasTab).mockReturnValue(true);
    installStoreSubscribers();
  }

  it("composes the mode and what the agent says it is doing", () => {
    driveEffect({ current_mode_id: "plan", agent_status_text: "reading the parser" });
    expect(setTabTooltip).toHaveBeenCalledWith("c1", "Plan · reading the parser");
  });

  it("gives the mode alone when the agent has declared nothing", () => {
    // The separator is emitted only when both halves exist, so a quiet chat's
    // tooltip is a mode rather than a mode with a dangling middot.
    driveEffect({ current_mode_id: "plan" });
    expect(setTabTooltip).toHaveBeenCalledWith("c1", "Plan");
  });

  it("gives the activity alone before the chat has a session", () => {
    // A chat with no bridge yet has no mode id, which is every brand-new chat.
    driveEffect({ current_mode_id: "", agent_status_text: "reading the parser" });
    expect(setTabTooltip).toHaveBeenCalledWith("c1", "reading the parser");
  });

  it("clears the tooltip when there is neither", () => {
    driveEffect({ current_mode_id: "" });
    expect(setTabTooltip).toHaveBeenCalledWith("c1", "");
  });
});

describe("a superseded activation paints no failure", () => {
  // The History page activates a chat TWICE — openChatTab activates the tab
  // (onShow → activateChatView) and openPreviousSession activates it again — and
  // store-load keys its abort controller by chat id, so the second fetch aborts
  // the first. loadMessages reports that abort the same way it reports a real
  // failure, so the superseded activation used to append its retry box and the
  // user opening a previous chat got "Failed to load messages." sitting beside a
  // transcript that had loaded fine (reconcile leaves unkeyed siblings alone, so
  // it stayed there).
  function loadedChat(): never {
    return {
      id: "c-loaded",
      model: "",
      message_count: 3,
      messages: [{ id: "m1" }],
      usage: { context_size: 0 },
      has_more: false,
    } as never;
  }

  beforeEach(() => {
    messagesEl.replaceChildren();
  });

  it("shows no retry box when a newer activation superseded the fetch", async () => {
    vi.mocked(get).mockReturnValue(loadedChat());
    // First fetch aborted (false), second one fine — what the two activations
    // produce in production.
    vi.mocked(loadMessages).mockResolvedValueOnce(false).mockResolvedValueOnce(true);

    const row = { chat_id: "c-loaded", session_id: "s1", title: "t", updated_at: 1 };
    openPreviousSession(row);
    openPreviousSession(row);

    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledTimes(2);
    });
    await Promise.resolve();
    expect(messagesEl.textContent).not.toContain("Failed to load messages");
  });

  it("still shows the retry box when the load genuinely failed", async () => {
    // The guard must not swallow a real failure: one activation, one failure.
    vi.mocked(get).mockReturnValue(loadedChat());
    vi.mocked(loadMessages).mockResolvedValue(false);

    openPreviousSession({ chat_id: "c-loaded", session_id: "s1", title: "t", updated_at: 1 });

    await vi.waitFor(() => {
      expect(messagesEl.textContent).toContain("Failed to load messages");
    });
  });
});

describe("restore: opening a closed conversation from History", () => {
  // The reported symptom was a blank chat page, so the assertion is that the
  // transcript is actually FETCHED and the tab actually opens. Every row the
  // History page offers now carries a chat_id (the server lists a session only
  // when a vibekit chat owns it), so this one path is the whole restore.
  function closedChat(): never {
    return {
      id: "c-closed",
      name: "Yesterday's work",
      model: "",
      message_count: 12,
      messages: [],
      usage: { context_size: 0 },
      has_more: true,
    } as never;
  }

  const row = {
    chat_id: "c-closed",
    session_id: "sess_closed",
    title: "Yesterday's work",
    updated_at: 1,
  };

  beforeEach(() => {
    messagesEl.replaceChildren();
  });

  it("opens the tab and fetches the transcript", async () => {
    vi.mocked(get).mockReturnValue(closedChat());
    openPreviousSession(row);

    // The store already holds the row, so no list refetch is needed.
    expect(loadList).not.toHaveBeenCalled();
    // The tab is what makes it reachable again; its id IS the chat id.
    const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { id: string; name: string };
    expect(spec.id).toBe("c-closed");
    // The store's name wins over the row's title: the chat record is the
    // authority on its own name, and KAS's copy can be a stale derivation.
    expect(spec.name).toBe("Yesterday's work");
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-closed");
    });
    expect(messagesEl.textContent).not.toContain("Failed to load messages");
  });

  it("fetches the chat list first when this device dropped the store row", async () => {
    // The ordinary case for this page: closing a tab calls removeChat, so a chat
    // closed in this session is absent from the store while its file survives.
    // activateChatView returns early on a missing row and loadMessages refuses to
    // write into one, so activating before the header lands renders an empty chat
    // view and stops — the blank page, reached from the other side.
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(true);
    openPreviousSession(row);

    await vi.waitFor(() => {
      expect(loadList).toHaveBeenCalled();
    });
    await vi.waitFor(() => {
      const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { id: string } | undefined;
      if (spec?.id !== "c-closed") {
        throw new Error("tab not opened");
      }
    });
    // The tab opens only AFTER the list lands, or it would activate against the
    // same empty store the guard exists for.
    const listOrder = vi.mocked(loadList).mock.invocationCallOrder[0] ?? 0;
    const tabOrder = vi.mocked(openTab).mock.invocationCallOrder[0] ?? 0;
    expect(listOrder).toBeLessThan(tabOrder);
  });

  it("ignores a row with no owning chat", async () => {
    // The adoption path is gone: an unclaimed row was always vibekit's own
    // utility session, and adopting it produced the blank page plus a junk chat.
    // The server no longer emits one; this is the belt-and-braces half.
    openPreviousSession({ ...row, chat_id: "" });
    expect(openTab).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });
});
