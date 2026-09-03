// Focused test for createPlannerSession — the ?agent=planner share shortcut's
// server-side handoff. chat.ts has a heavy import graph, so every direct
// dependency is mocked at the first hop; the store mock is stateful for
// activeId so getActiveId() returns the id createSession set. The assertion:
// createPlannerSession dispatches chat.set_mode with modeID "plan" for the new
// chat (mirrors role-picker's selectMode).
import { describe, it, expect, vi, beforeEach } from "vitest";

// The creating actions answer with the SERVER's chat header now, so their mocks
// have to as well: a stub returning undefined would make every createSession below
// take the refused branch and open no tab, which is the opposite of what these
// cases are about. A FIXED id per action rather than a counter, so an assertion
// names the id it means instead of depending on how many tests ran before it.
const { setModeDispatch, forkDispatch, createDispatch, submitPromptMock, messagesEl } = vi.hoisted(
  () => {
    const serverHeader = (id: string): unknown => ({
      id,
      name: "New conversation",
      model: "auto",
      usage: {
        context_pct: 0,
        context_size: 0,
        credits: 0,
        turn_count: 0,
        last_turn_ms: 0,
        has_real_data: false,
      },
      created_at: 0,
      updated_at: 0,
      message_count: 0,
    });
    // The widened creating reply: the chat, the tab the coordinator opened for
    // it, and the version that open committed. The fork's subject carries the
    // PARENT the server nested it under — resolving it is no longer the client's.
    const serverCreated = (id: string, tabID: string, parent = ""): unknown => ({
      chat: serverHeader(id),
      subject: { id: tabID, kind: "chat", ref: id, parent, pinned: false, owns: true },
      version: 3,
    });
    return {
      setModeDispatch: vi.fn(),
      // Both creating dispatches are called WITH a payload, so the fakes declare
      // that parameter: without it the mock's calls tuple is empty and the cases
      // below cannot read the argument they exist to assert on.
      forkDispatch: vi.fn(async (_payload?: Record<string, unknown>) =>
        serverCreated("c-forked", "tb_forked", "tb_parent"),
      ),
      createDispatch: vi.fn(async (_payload?: Record<string, unknown>) =>
        serverCreated("c-created", "tb_created"),
      ),
      submitPromptMock: vi.fn(),
      messagesEl: document.createElement("div"),
    };
  },
);

let activeId = "";

vi.mock("./store.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  // A real spy, not the present-but-undefined placeholder it used to be: seeding a
  // created chat calls it to recompute the DERIVED usage.context_size, which is a
  // function of the model and so cannot ride the server's header.
  setModel: vi.fn(),
  getActiveId: () => activeId,
  getActive: vi.fn(() => undefined),
  get: vi.fn(() => undefined),
  getSessions: vi.fn(() => []),
  // The per-row strip effect's tracked read; inert until a case aims it.
  watchSession: vi.fn(() => undefined),
  setActive: vi.fn((id: string) => {
    activeId = id;
  }),
  upsertHeader: vi.fn(),
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
  // The activation refetch gate. Defaults TRUE — refetch, the shape every case
  // outside the gate describe was written against; the gate describe drives
  // both verdicts explicitly. The predicate's own truth table (residency ×
  // loadedEpoch × syncEpoch) is store.test.ts's subject; what THIS suite owns
  // is the routing each verdict produces.
  transcriptStale: vi.fn(() => true),
}));
vi.mock("./store-load.js", () => ({ loadList: vi.fn(), loadMessages: vi.fn() }));
vi.mock("./banner-stack.js", () => ({ ensureBound: vi.fn() }));
vi.mock("./chat-commands.js", () => ({ sendPromptTo: vi.fn() }));
vi.mock("./tabs.js", () => ({
  // `openTab` resolves with its OUTCOME: every open is a round trip through
  // `open_tab`, and the callers that branch (History's reopen) read the string.
  openTab: vi.fn(() => Promise.resolve("opened")),
  // The adoption path: creating commands paint their tab from the reply.
  adoptSubject: vi.fn(),
  activateTab: vi.fn(),
  // The lookup that replaced `hasTab(chatID)`: a chat id is no longer a tab id, so
  // "" means no tab is open for that chat.
  tabIdFor: vi.fn(() => ""),
  // The registry seam: which chat refs hold an open tab. Empty means the strip
  // wires no row effects; the tooltip suite below aims it at one chat.
  openChatRefs: vi.fn(() => [] as string[]),
  getActiveTabId: vi.fn(() => ""),
  renameTab: vi.fn(),
  setTabStatus: vi.fn(),
  setTabTooltip: vi.fn(),
}));
vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));
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
  // Needed even though no case below reaches it: activateChatView's success
  // branch calls it, so omitting it leaves a TypeError waiting for whichever
  // future test does exercise that path.
  loadTurnRail: vi.fn(),
  pointTurnRail: vi.fn(),
  fadeInTranscript: vi.fn(),
  // The multiplexer's surface: activation mounts transcript furniture into the
  // active view (null here — the mock has no view, so callers fall back to
  // $.messages), and a tab close disposes the chat's view.
  activeTranscriptView: vi.fn(() => null),
  transcriptViewFor: vi.fn(() => null),
  disposeChatView: vi.fn(),
}));
vi.mock("./attachments.js", () => ({ addAttachment: vi.fn() }));
// The composer's per-chat state owns real DOM (the textarea) and a debounced
// action dispatch; chat.ts only has to call its save/restore pair in the right
// order, which the mock records.
vi.mock("./composer-state.js", () => ({
  saveComposerState: vi.fn(),
  restoreComposerState: vi.fn(),
  retargetComposer: vi.fn(),
  seedComposerState: vi.fn(),
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
// transport.ts owns the SSE connection and the command POST; chat.ts reaches it
// for exactly one thing, the create gesture's correlation id. Mocked at the first
// hop like every other dependency here, or this suite opens an EventSource. The
// stub keeps the `op-` prefix the server's ValidIdent gate accepts, because the
// assertions below read it.
vi.mock("./transport.js", () => ({ newOpID: () => "op-test" }));
vi.mock("./actions/chat.js", () => ({
  deleteChat: { dispatch: vi.fn() },
  restoreChat: { dispatch: vi.fn() },
  setMode: { dispatch: setModeDispatch },
  forkChat: { dispatch: forkDispatch },
  createChat: { dispatch: createDispatch },
}));

import * as chatModule from "./chat.js";
import {
  activateChatView,
  closeChatTab,
  createPlannerSession,
  openTangentChat,
  openPreviousSession,
  installStoreSubscribers,
} from "./chat.js";
import {
  openTab,
  adoptSubject,
  activateTab,
  tabIdFor,
  setTabTooltip,
  openChatRefs,
} from "./tabs.js";
import { addAttachment } from "./attachments.js";
import { dropDecisions } from "./decision-dock.js";
import { get, watchSession, removeChat, setActive, upsertHeader, clearTurnDone } from "./store.js";
import { transcriptStale } from "./store.js";
import { loadList, loadMessages } from "./store-load.js";
import {
  loadTurnRail,
  pointTurnRail,
  fadeInTranscript,
  setLoadMore,
  disposeChatView,
} from "./messages.js";
import { seedComposerState } from "./composer-state.js";
import { info } from "./toast.js";
import { isRetentionEnabled } from "./retention.js";
import { skeletonTiming } from "@cplieger/ui-primitives/skeleton";
// `closeChat` is deliberately not imported: the command was retired, and the
// process teardown a chat-tab close performs is `close_tab`'s, server-side.
import { deleteChat } from "./actions/chat.js";

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(get).mockReturnValue(undefined);
  vi.mocked(isRetentionEnabled).mockReturnValue(false);
  vi.mocked(loadMessages).mockResolvedValue(true);
  vi.mocked(transcriptStale).mockReturnValue(true);
  activeId = "";
});

describe("createPlannerSession", () => {
  it("dispatches chat.set_mode with modeID 'plan' for the newly created chat", async () => {
    await createPlannerSession();
    expect(setModeDispatch).toHaveBeenCalledTimes(1);
    const arg = setModeDispatch.mock.calls[0]?.[0] as { chatID: string; modeID: string };
    expect(arg.modeID).toBe("plan");
    // The id is the SERVER's, so the dispatch has to wait for the create's reply.
    // Detaching it would send set_mode to whatever chat was active before.
    expect(arg.chatID).toBe("c-created");
  });

  // The create is a round trip, so it can be refused. Nothing may be addressed to
  // a chat that does not exist.
  it("dispatches nothing when the create is refused", async () => {
    createDispatch.mockResolvedValueOnce(null);
    await createPlannerSession();
    expect(setModeDispatch).not.toHaveBeenCalled();
  });

  // The op id is a DISPATCH ARGUMENT and there is exactly one per gesture. Minted
  // inside the action's run() it would be fresh per retry attempt and the server
  // would mint a second chat for one click.
  it("passes an op id with the create", async () => {
    await createPlannerSession();
    const arg = createDispatch.mock.calls[0]?.[0] as { opID: string };
    expect(arg.opID).toMatch(/^op-/);
  });
});

// ---------------------------------------------------------------------------
// createSession is ASYNC because the chat id is the SERVER's, and every caller has
// to await it or explicitly detach. These pin the class, so that a site converted
// to a bare `void` fails here rather than in production: the failure a `void` at a
// dependent site produces is silent — the work lands on the previously active chat,
// or nowhere at all on a first-ever visit.
// ---------------------------------------------------------------------------

describe("createSession is async, and what that means for its callers", () => {
  it("does not set the active chat until the server has answered", async () => {
    let resolveCreate: (h: unknown) => void = () => undefined;
    createDispatch.mockReturnValueOnce(
      new Promise((res) => {
        resolveCreate = res;
      }),
    );
    const pending = chatModule.createSession();

    // The window a bare `void` would leave a dependent caller reading.
    expect(activeId).toBe("");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();

    resolveCreate({
      chat: {
        id: "c-late",
        name: "New conversation",
        model: "auto",
        usage: {
          context_pct: 0,
          context_size: 0,
          credits: 0,
          turn_count: 0,
          last_turn_ms: 0,
          has_real_data: false,
        },
        created_at: 0,
        updated_at: 0,
        message_count: 0,
      },
      subject: {
        id: "tb_late",
        kind: "chat",
        ref: "c-late",
        parent: "",
        pinned: false,
        owns: true,
      },
      version: 9,
    });
    await expect(pending).resolves.toBe("c-late");
    expect(activeId).toBe("c-late");
    expect(vi.mocked(adoptSubject)).toHaveBeenCalledTimes(1);
    expect(vi.mocked(activateTab)).toHaveBeenCalledWith("tb_late");
  });

  // A refused create opens nothing and returns "". Opening a tab anyway is the
  // window this whole stage removes: an id no server can resolve.
  it("opens no tab and returns the empty id when the create is refused", async () => {
    createDispatch.mockResolvedValueOnce(null);
    await expect(chatModule.createSession()).resolves.toBe("");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(vi.mocked(activateTab)).not.toHaveBeenCalled();
    expect(activeId).toBe("");
  });

  // The tab rides the CREATE's reply: the coordinator opened it under the same
  // lock that wrote the record, so dispatching `open_tab` afterwards was a whole
  // round trip to learn `created: false`. The adoption replaced it.
  it("adopts the tab from the create's reply instead of dispatching a second open", async () => {
    await chatModule.createSession();
    expect(vi.mocked(openTab)).not.toHaveBeenCalled();
    const [subject, name] = vi.mocked(adoptSubject).mock.calls[0] ?? [];
    expect(subject).toMatchObject({ id: "tb_created", kind: "chat", ref: "c-created" });
    expect(name).toBe("New conversation");
    expect(vi.mocked(activateTab)).toHaveBeenCalledWith("tb_created");
  });

  // The initial prompt rides INSIDE the create, which is why app.ts can detach that
  // one site: the send happens in the continuation, addressed to the created chat.
  it("sends an initial prompt to the chat it created, not to the one that was active", async () => {
    activeId = "c-previous";
    await chatModule.createSession("do the thing");
    expect(submitPromptMock).toHaveBeenCalledWith("c-created", "do the thing");
  });

  it("seeds the row from the SERVER's header rather than a local guess", async () => {
    await chatModule.createSession();
    expect(vi.mocked(upsertHeader).mock.calls.at(-1)?.[0]).toMatchObject({ id: "c-created" });
  });
});

// ---------------------------------------------------------------------------
// Attaching a BATCH. The plural signature is the structural fix for the concurrent
// -create hazard the async boundary introduced: every caller was already a loop, and
// N iterations each finding no active chat would each ask for one.
// ---------------------------------------------------------------------------

describe("attaching several paths at once", () => {
  it("creates ONE chat for the whole batch", async () => {
    await chatModule.attachPathsToActiveChat(["/w/a.ts", "/w/b.ts", "/w/c.ts"]);

    expect(createDispatch).toHaveBeenCalledTimes(1);
    expect(vi.mocked(addAttachment).mock.calls.flat()).toEqual(["/w/a.ts", "/w/b.ts", "/w/c.ts"]);
  });

  it("creates nothing when a chat is already active", async () => {
    activeId = "c-live";
    await chatModule.attachPathsToActiveChat(["/w/a.ts"]);

    expect(createDispatch).not.toHaveBeenCalled();
    expect(vi.mocked(addAttachment)).toHaveBeenCalledWith("/w/a.ts");
  });

  it("attaches nothing when the create is refused", async () => {
    createDispatch.mockResolvedValueOnce(null);
    await chatModule.attachPathsToActiveChat(["/w/a.ts"]);

    expect(vi.mocked(addAttachment)).not.toHaveBeenCalled();
  });

  it("does not ask for a chat for an empty batch", async () => {
    await chatModule.attachPathsToActiveChat([]);

    expect(createDispatch).not.toHaveBeenCalled();
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
      expect(seedComposerState).toHaveBeenCalledWith("c-empty");
    });
    expect(loadMessages).toHaveBeenCalledWith("c-empty");
  });

  it("seeds nothing when the fetch fails", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    vi.mocked(loadMessages).mockResolvedValue(false);
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
    expect(seedComposerState).not.toHaveBeenCalled();
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
    expect(seedComposerState).not.toHaveBeenCalled();
  });

  // The skip this branch used to carry is GONE, and its absence is the assertion:
  // a brand-new chat is a server record before its tab opens, so the GET has
  // something to answer and there is no id the server has never seen.
  it("fetches even a brand-new chat, because the server already has it", async () => {
    await createPlannerSession();
    const id = (vi.mocked(upsertHeader).mock.calls.at(-1)?.[0] as { id: string }).id;
    expect(id).toBe("c-created");
    vi.mocked(get).mockReturnValue(emptyChat());
    activate();
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-empty");
    });
  });
});

// ---------------------------------------------------------------------------
// The timeline rail on a chat with no messages. The rail is a module singleton,
// so activation has to hand it the chat it is activating; only the loaded branch
// used to, which left a brand-new chat wearing the previous chat's timeline —
// markers over a conversation the reader had not spoken in yet.
// ---------------------------------------------------------------------------

describe("the timeline rail of a chat with no messages", () => {
  function emptyChat(): never {
    return {
      id: "c-empty",
      model: "",
      message_count: 0,
      messages: [],
      usage: { context_size: 0 },
    } as never;
  }

  /** Drive the restore and wait for the activation.
   *
   *  AWAITED, because opening a tab is a round trip: `openPreviousSession` runs
   *  `activateChatView` in the open's continuation, so nothing the activation does
   *  has happened yet when the call returns. */
  async function activate(): Promise<void> {
    openPreviousSession({ chat_id: "c-empty", session_id: "s1", title: "t", updated_at: 1 });
    await vi.waitFor(() => {
      expect(setActive).toHaveBeenCalledWith("c-empty");
    });
  }

  it("is pointed at the chat being activated", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    await activate();
    expect(pointTurnRail).toHaveBeenCalledWith("c-empty");
  });

  it("is pointed at a brand-new chat too, which has no turns to fetch", async () => {
    // Every New chat click lands here. Pointing costs no request, and skipping
    // it is what left the previous chat's markers on screen.
    vi.mocked(get).mockReturnValue(emptyChat());
    await activate();
    expect(pointTurnRail).toHaveBeenCalledWith("c-empty");
  });

  it("is not fetched, because a chat with no messages has no turns", async () => {
    vi.mocked(get).mockReturnValue(emptyChat());
    await activate();
    expect(loadTurnRail).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The scroller is the second per-chat view singleton and it leaked the same way
// the rail did. Two of its elements belong to whichever chat was open when the
// reader last scrolled: the resume control ("Latest") and the "Load older
// messages" button, which is an unkeyed child of #messages that the transcript's
// keyed reconcile leaves in place. Only the loaded branch used to hand the
// scroller its next chat, so a New chat click opened a tab wearing both.
// ---------------------------------------------------------------------------

describe("the scroller on a chat switch", () => {
  function chat(messages: unknown[]): never {
    return {
      id: "c-1",
      model: "",
      message_count: messages.length,
      messages,
      usage: { context_size: 0 },
    } as never;
  }

  /** Drive the restore and wait for the activation, which runs in the OPEN's
   *  continuation now that opening a tab is a round trip. */
  async function activate(): Promise<void> {
    openPreviousSession({ chat_id: "c-1", session_id: "s1", title: "t", updated_at: 1 });
    await vi.waitFor(() => {
      expect(setActive).toHaveBeenCalledWith("c-1");
    });
  }

  it("re-keys the rail before the transcript repaints", async () => {
    // setActive re-derives activeSession, which repaints synchronously — and
    // the paint is what parks the outgoing view and attaches the incoming one
    // (the scroller's per-view state is the multiplexer's now, not this
    // module's). What activation still owns is the rail: pointing it AFTER
    // setActive would leave one frame of the previous chat's markers over the
    // new chat's transcript.
    vi.mocked(get).mockReturnValue(chat([]));
    await activate();
    const rail = vi.mocked(pointTurnRail).mock.invocationCallOrder[0] ?? 0;
    const active = vi.mocked(setActive).mock.invocationCallOrder[0] ?? 0;
    expect(rail).toBeGreaterThan(0);
    expect(rail).toBeLessThan(active);
  });

  it("points the rail on a chat that has messages, without waiting for the fetch", async () => {
    // The loaded branch used to re-key the view furniture only after its own
    // load resolved, so between the switch and the response the reader saw the
    // previous chat's timeline over this chat's transcript.
    vi.mocked(get).mockReturnValue(chat([{ id: "m1", role: "user" }]));
    await activate();
    expect(pointTurnRail).toHaveBeenCalledWith("c-1");
    expect(loadMessages).toHaveBeenCalledWith("c-1");
  });

  it("points the rail for a chat the store holds no row for", () => {
    // activateChatView returns early on a missing row and renders an empty view.
    // The re-point sits above that return, or the blank page keeps the markers.
    //
    // Called by NAME rather than pulled off the spec a door passed: a chat tab's
    // activation hook is the factory's, registered once by the composition root,
    // so `activateChatView` IS that hook and reaching it any other way would test
    // a second definition of it.
    vi.mocked(get).mockReturnValue(undefined);
    activateChatView("c-1");
    expect(pointTurnRail).toHaveBeenCalledWith("c-1");
  });
});

// ---------------------------------------------------------------------------
// The loading skeleton. It is a PLACEHOLDER, so the one thing it may never do is
// paint beside the content it stands in for — and that is what every switch back
// to a loaded chat looked like: setActive repaints the whole transcript
// synchronously, then the refresh fetch armed a skeleton that appended a shimmer
// underneath the real turns until the response landed.
// ---------------------------------------------------------------------------

describe("the transcript's loading skeleton", () => {
  function chat(messages: unknown[]): never {
    return {
      id: "c-1",
      model: "",
      message_count: Math.max(messages.length, 1),
      messages,
      usage: { context_size: 0 },
    } as never;
  }

  async function activate(): Promise<void> {
    openPreviousSession({ chat_id: "c-1", session_id: "s1", title: "t", updated_at: 1 });
    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-1");
    });
  }

  it("is not armed for a chat whose transcript is already in the store", async () => {
    vi.mocked(get).mockReturnValue(chat([{ id: "m1", role: "user" }]));
    await activate();
    expect(skeletonTiming).not.toHaveBeenCalled();
  });

  it("is armed for a chat with history the store has not fetched yet", async () => {
    // message_count says the conversation exists, messages says nothing of it is
    // here — the one state a placeholder is for.
    vi.mocked(get).mockReturnValue(chat([]));
    await activate();
    expect(skeletonTiming).toHaveBeenCalledTimes(1);
  });

  it("does not fade the transcript in when no skeleton was painted", async () => {
    // The fade exists to cover the swap OUT of a skeleton. A load that settles
    // inside the show delay paints none, and fading then would put a flicker on
    // an open that is instant today.
    vi.mocked(get).mockReturnValue(chat([]));
    await activate();
    await vi.waitFor(() => {
      expect(seedComposerState).toHaveBeenCalledWith("c-1");
    });
    expect(fadeInTranscript).not.toHaveBeenCalled();
  });

  it("fades the transcript in when a skeleton was painted", async () => {
    // Drive the show callback the way the 150ms timer would, so the swap this
    // covers is the real one rather than a flag set by the test.
    vi.mocked(skeletonTiming).mockImplementationOnce((show) => {
      show();
      return { commit: vi.fn(), cancel: vi.fn() };
    });
    vi.mocked(get).mockReturnValue(chat([]));
    await activate();
    await vi.waitFor(() => {
      expect(fadeInTranscript).toHaveBeenCalledTimes(1);
    });
  });
});

describe("closeChatTab is the one client-local teardown", () => {
  // `closeChatTab` IS the tab's teardown, exported and registered once by the
  // composition root, so it is called by name rather than pulled off a spec a
  // door passed. There is no retention branch and no provenance flag left in
  // it: the process teardown AND the retention-off record delete are both the
  // server's `close_tab` operation, so this runs the identical local cleanup
  // whoever closed the tab and whatever retention says — `delete_chat`
  // survives as History's delete and is never dispatched from a close.

  it("cleans up locally with retention ENABLED, and dispatches nothing", () => {
    vi.mocked(get).mockReturnValue({ message_count: 3 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(true);
    closeChatTab("c-closed");
    expect(removeChat).toHaveBeenCalledWith("c-closed");
    expect(dropDecisions).toHaveBeenCalledWith("c-closed");
    // The chat's transcript view — active or parked — runs the real per-view
    // dispose, BEFORE the store row goes (the removal's repaint must not park
    // a view this close is about to throw away).
    expect(disposeChatView).toHaveBeenCalledWith("c-closed");
    const disposeOrder = vi.mocked(disposeChatView).mock.invocationCallOrder[0] ?? 0;
    const removeOrder = vi.mocked(removeChat).mock.invocationCallOrder[0] ?? 0;
    expect(disposeOrder).toBeLessThan(removeOrder);
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });

  it("cleans up locally with retention DISABLED, and still dispatches nothing", () => {
    // 0 = ephemeral: the record is gone by design — deleted INSIDE the server's
    // close operation, exactly once, wherever the gesture happened. A second
    // delete from here would race the one that already ran.
    vi.mocked(get).mockReturnValue({ message_count: 3 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(false);
    closeChatTab("c-ephemeral");
    expect(removeChat).toHaveBeenCalledWith("c-ephemeral");
    expect(deleteChat.dispatch).not.toHaveBeenCalled();
  });

  it("removes a zero-message chat like any other", () => {
    // The old message_count === 0 skip predated the coordinator writing the
    // record before its tab; the server deletes zero-message chats like any
    // other now, and the client has no branch to mirror.
    vi.mocked(get).mockReturnValue({ message_count: 0 } as never);
    vi.mocked(isRetentionEnabled).mockReturnValue(false);
    closeChatTab("c-empty");
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
    activateChatView("c-open");
    expect(clearTurnDone).not.toHaveBeenCalled();
  });

  it("drops the chat's unanswered asks on close, in every retention mode", () => {
    for (const retention of [true, false]) {
      vi.clearAllMocks();
      vi.mocked(get).mockReturnValue({ message_count: 3 } as never);
      vi.mocked(isRetentionEnabled).mockReturnValue(retention);
      closeChatTab("c-closed");
      expect(dropDecisions).toHaveBeenCalledWith("c-closed");
    }
  });
});

// ---------------------------------------------------------------------------
// The tangent: a real chat opened as a sub-tab of the one it came from, whose
// context is the parent's REAL context via a session fork.
// ---------------------------------------------------------------------------

describe("openTangentChat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    activeId = "";
    vi.mocked(get).mockReturnValue({ model: "parent-model" } as never);
  });

  // ASYNC now, and the ORDER inverted with it: the fork is what mints the chat
  // id, so the sub-tab cannot exist until the reply lands — and the reply is
  // what carries it. TANGENT PARITY with createSession: the tab is adopted from
  // the fork's response, the second POST is gone, and the subject's PARENT is
  // the server's nesting decision rather than a client lookup.
  it("adopts the sub-tab from the fork's reply, parent and all, with no second POST", async () => {
    await openTangentChat("c-parent");
    expect(vi.mocked(openTab)).not.toHaveBeenCalled();
    const [subject, name] = vi.mocked(adoptSubject).mock.calls[0] ?? [];
    expect(subject).toMatchObject({
      id: "tb_forked",
      kind: "chat",
      ref: "c-forked",
      parent: "tb_parent",
      owns: true,
    });
    expect(name).toBe("New conversation");
    expect(vi.mocked(activateTab)).toHaveBeenCalledWith("tb_forked");
  });

  // The fork is what persists the chat AND what carries the context, so it must
  // name the parent — the server has nothing to fork without it. It no longer
  // carries a chat id: that is what the reply brings back.
  it("dispatches chat.fork naming the parent, with an op id and no chat id", async () => {
    await openTangentChat("c-parent");
    expect(forkDispatch).toHaveBeenCalledTimes(1);
    const arg = forkDispatch.mock.calls[0]?.[0] as {
      parentChatID: string;
      opID: string;
      chatID?: string;
    };
    expect(arg.parentChatID).toBe("c-parent");
    expect(arg.opID).toMatch(/^op-/);
    expect(arg.chatID).toBeUndefined();
  });

  // A selection chooses nothing once the whole conversation is inherited, so
  // there is no seeded prompt any more. This is the assertion that fails if the
  // old selection-seeding path is reintroduced.
  it("seeds no prompt: the fork carries the context, not a quoted phrase", async () => {
    await openTangentChat("c-parent");
    expect(submitPromptMock).not.toHaveBeenCalled();
  });

  // Model, mode and effort are ALL the server's to copy off the parent record now,
  // and the row is seeded from the header the fork returned rather than from a
  // local guess — which is invariant 2 applied to creation.
  it("seeds the row from the server's header and leaves mode to the server", async () => {
    vi.mocked(get).mockReturnValue({ model: "parent-model", current_mode_id: "plan" } as never);
    await openTangentChat("c-parent");
    expect(vi.mocked(upsertHeader).mock.calls.at(-1)?.[0]).toMatchObject({ id: "c-forked" });
    expect(setModeDispatch).not.toHaveBeenCalled();
  });

  // A refused fork opens nothing: there is no chat to open a tab for, and opening
  // one under a guessed id is the window this whole stage removes.
  it("opens no tab when the fork is refused", async () => {
    forkDispatch.mockResolvedValueOnce(null);
    await openTangentChat("c-parent");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(vi.mocked(activateTab)).not.toHaveBeenCalled();
  });

  it("does nothing when the parent is unknown", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    await openTangentChat("c-parent");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
    expect(forkDispatch).not.toHaveBeenCalled();
  });

  it("does nothing for an empty parent id", async () => {
    await openTangentChat("");
    expect(vi.mocked(adoptSubject)).not.toHaveBeenCalled();
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
  /** The opaque id the projection minted for chat `c1`. Every writer in the row
   *  effect is id-keyed because the DOM row is, and a chat id is no longer that
   *  id — so ONE `tabIdFor` lookup per row is what the effect reuses. */
  const TAB_ID = "tb_c1";

  function driveEffect(over: Record<string, unknown>): void {
    const s = { id: "c1", name: "Fix the parser", ...over };
    // One open tab for c1; its row effect reads the chat through watchSession.
    vi.mocked(openChatRefs).mockReturnValue(["c1"]);
    vi.mocked(watchSession).mockReturnValue(s as never);
    vi.mocked(tabIdFor).mockReturnValue(TAB_ID);
    installStoreSubscribers();
  }

  it("composes the mode and what the agent says it is doing", () => {
    driveEffect({ current_mode_id: "plan", agent_status_text: "reading the parser" });
    expect(setTabTooltip).toHaveBeenCalledWith(TAB_ID, "Plan · reading the parser");
  });

  it("gives the mode alone when the agent has declared nothing", () => {
    // The separator is emitted only when both halves exist, so a quiet chat's
    // tooltip is a mode rather than a mode with a dangling middot.
    driveEffect({ current_mode_id: "plan" });
    expect(setTabTooltip).toHaveBeenCalledWith(TAB_ID, "Plan");
  });

  it("gives the activity alone before the chat has a session", () => {
    // A chat with no bridge yet has no mode id, which is every brand-new chat.
    driveEffect({ current_mode_id: "", agent_status_text: "reading the parser" });
    expect(setTabTooltip).toHaveBeenCalledWith(TAB_ID, "reading the parser");
  });

  it("clears the tooltip when there is neither", () => {
    driveEffect({ current_mode_id: "" });
    expect(setTabTooltip).toHaveBeenCalledWith(TAB_ID, "");
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

describe("a tab whose chat this device's store does not hold", () => {
  // Measured after a forced restart: a TRUNCATED /api/chats answer left several
  // open tabs with no store row, and activateChatView's early return left the
  // pane holding whatever the previous chat had put there — no error, no retry,
  // no self-heal, until the reader reloaded the page. (The server half of that
  // truncation is internal/chat's shared-scan ownership.)
  function loadedChat(): never {
    return {
      id: "c-missing",
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

  it("says so instead of leaving a blank pane", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(false);

    activateChatView("c-missing");

    expect(messagesEl.textContent).toContain("This conversation isn't loaded yet.");
    expect(messagesEl.querySelector("button")?.textContent).toBe("Retry");
  });

  it("re-reads the chat list once and activates the chat when it arrives", async () => {
    // The store answers absent for the first activation and populated once the
    // re-read has landed, which is exactly what a truncated boot list followed by
    // a complete one produces.
    vi.mocked(get).mockReturnValueOnce(undefined).mockReturnValue(loadedChat());
    vi.mocked(loadList).mockResolvedValue(true);

    activateChatView("c-missing");

    await vi.waitFor(() => {
      expect(loadMessages).toHaveBeenCalledWith("c-missing");
    });
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(messagesEl.textContent).not.toContain("This conversation isn't loaded yet.");
  });

  it("does not loop when the re-read still does not produce the chat", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(true);

    activateChatView("c-missing");

    await vi.waitFor(() => {
      expect(loadList).toHaveBeenCalledTimes(1);
    });
    // A second heal would mean a second read. The affordance stays, which is the
    // honest end state for a chat the server does not report.
    await Promise.resolve();
    expect(loadList).toHaveBeenCalledTimes(1);
    expect(messagesEl.textContent).toContain("This conversation isn't loaded yet.");
  });

  it("clears a previous activation's failure box", async () => {
    vi.mocked(get).mockReturnValue(undefined);
    vi.mocked(loadList).mockResolvedValue(false);

    activateChatView("c-missing");
    activateChatView("c-missing");

    expect(messagesEl.querySelectorAll(".load-error")).toHaveLength(1);
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
    // The tab is what makes it reachable again, and the chat id is its REF: an id
    // is opaque and server-minted, so the door names the subject instead.
    const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { ref: string; name: string };
    expect(spec.ref).toBe("c-closed");
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
      const spec = vi.mocked(openTab).mock.calls.at(-1)?.[0] as { ref: string } | undefined;
      if (spec?.ref !== "c-closed") {
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
    await openPreviousSession({ ...row, chat_id: "" });
    expect(openTab).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });

  it("a 404 reopen answers 'gone': ephemeral notice, NO activation", async () => {
    // Retention is off and a close DELETED the conversation after History
    // listed it. The open's outcome keeps the 404 distinct from a network
    // failure, so this arm can say the truth — the chat was ephemeral — and
    // must not activate: an activation here is an empty transcript over a dead
    // active pointer.
    vi.mocked(get).mockReturnValue(closedChat());
    vi.mocked(openTab).mockResolvedValue("not-found");

    await expect(openPreviousSession(row)).resolves.toBe("gone");

    expect(vi.mocked(info)).toHaveBeenCalledWith(
      "That conversation was ephemeral (retention is off) and is gone.",
    );
    // activateChatView never ran: no rail pointing, no fetch.
    expect(pointTurnRail).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });

  it("a network failure answers 'failed', which is NOT the ephemeral face", async () => {
    // A failed fetch must never read as "deleted": the row stays, the framework
    // toast has already spoken, and nothing here claims the chat is gone.
    vi.mocked(get).mockReturnValue(closedChat());
    vi.mocked(openTab).mockResolvedValue("failed");

    await expect(openPreviousSession(row)).resolves.toBe("failed");

    expect(vi.mocked(info)).not.toHaveBeenCalled();
    expect(pointTurnRail).not.toHaveBeenCalled();
    expect(loadMessages).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// The activation refetch gate. activateChatView used to refetch unconditionally;
// now the transcript-staleness verdict routes it: stale → the fetch path, with
// the rail FORCED behind the successful load (the heal re-stamps the session
// fresh, so the rail's own gate can no longer see the verdict); fresh → zero
// message fetches, the view furniture re-wired, and the rail left to its own
// record (its count arm may still fetch — that decision is turn-rail.test.ts's
// subject, not this module's).
// ---------------------------------------------------------------------------

describe("activateChatView routes on the staleness verdict", () => {
  function loadedChat(id: string): never {
    return {
      id,
      name: "seeded",
      model: "",
      messages: [{ id: "m1", role: "user", ts: 1 }],
      message_count: 1,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never;
  }

  function emptyChat(id: string): never {
    return {
      id,
      name: "seeded",
      model: "",
      messages: [],
      message_count: 0,
      has_more: false,
      usage: { context_size: 1 },
      draft: "",
    } as never;
  }

  it("a fresh window activates with ZERO message fetches", () => {
    vi.mocked(get).mockReturnValue(loadedChat("c-fresh"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-fresh");

    expect(loadMessages).not.toHaveBeenCalled();
    // The per-chat view furniture is still re-wired: the load-more hook is
    // activation's, not the fetch's. (The landing position is the
    // multiplexer's now — a resident view restores its own.)
    expect(setLoadMore).toHaveBeenCalled();
    // The rail is handed the chat WITHOUT force: its own record decides.
    expect(loadTurnRail).toHaveBeenCalledWith("c-fresh");
  });

  it("a stale window refetches messages, then forces the rail behind the load", async () => {
    vi.mocked(get).mockReturnValue(loadedChat("c-stale"));
    vi.mocked(transcriptStale).mockReturnValue(true);

    activateChatView("c-stale");

    expect(loadMessages).toHaveBeenCalledWith("c-stale");
    // The rail fetch is sequenced behind the messages fetch resolving.
    expect(loadTurnRail).not.toHaveBeenCalled();
    await vi.mocked(loadMessages).mock.results[0]?.value;
    expect(loadTurnRail).toHaveBeenCalledWith("c-stale", { force: true });
  });

  it("a fresh EMPTY chat skips the draft fetch too", () => {
    // The empty branch's GET exists only to adopt the server-held draft, and a
    // window this device already fetched yielded it; zero fetches means zero.
    vi.mocked(get).mockReturnValue(emptyChat("c-empty-fresh"));
    vi.mocked(transcriptStale).mockReturnValue(false);

    activateChatView("c-empty-fresh");

    expect(loadMessages).not.toHaveBeenCalled();
    expect(loadTurnRail).not.toHaveBeenCalled();
  });

  it("a stale EMPTY chat fetches its record for the draft", () => {
    vi.mocked(get).mockReturnValue(emptyChat("c-empty-stale"));
    vi.mocked(transcriptStale).mockReturnValue(true);

    activateChatView("c-empty-stale");

    expect(loadMessages).toHaveBeenCalledWith("c-empty-stale");
  });
});
