// ---------------------------------------------------------------------------
// Tests for handlers/turn.ts: the ERROR_ROUTES classification table plus the
// turn_ended and error SSE handlers.
//
// These drive the REAL handlers (via the bus capture) and the REAL store, and
// assert observable outcomes: the rendered turn-summary text, the cleared
// thinking flag, the drained queued prompt, and the error routing. Sibling
// subsystems (notify, failure-notice, send-state, chat-commands, git) stay
// mocked because a call into them is a command at the handler's boundary.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  setSessions,
  setActive,
  get,
  recordSteerQueued,
  recordSteerSent,
  steerCount,
  steerMarks,
  appendMessage,
  setAgentStatus,
  tabStatusFor,
  relatchTurnVerdict,
  upsertHeader,
  noteTruncatedSnapshot,
  isTruncatedSnapshot,
  noteLiveTurnMessage,
  liveTurnMessage,
  setTurnFailed,
  dropSteers,
  pendingSteerCarry,
} from "../store.js";
import { noteRunLive, noteRunSettled } from "../run-store.js";
import type { ChatHeader, Session } from "../types.js";
import type { TurnOutcome } from "../wire/types.gen.js";
import { severityOf } from "../turn-severity.js";
import type * as TurnRail from "../turn-rail.js";
import type * as ApiClient from "../api-client.js";
import type * as ChatActions from "../actions/chat.js";

type TurnRailModule = typeof TurnRail;

// The single-chat GET, so the run arm's refusal can be asserted where it MATTERS: the
// live-turn marker only earns its keep at the next newest-page load, which is where
// dropping it deletes the reply still streaming. Spread the real surface and replace one
// fetcher, so every other consumer in this graph keeps the module it had.
//
// Through `vi.hoisted` because `run-store.js` is statically imported below and imports
// api-client, so the mocker resolves this factory during linking — above this file's own
// top-level initializers, where a plain `const` is still in its temporal dead zone.
const { mockApiGetTyped } = vi.hoisted(() => ({ mockApiGetTyped: vi.fn() }));
vi.mock("../api-client.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ApiClient>()),
  apiGetTyped: mockApiGetTyped,
}));

// scroll.ts touches DOM elements at import; use the shared mock.
vi.mock(
  "../scroll.js",
  async () => (await import("../__test-helpers__/scroll-mock.js")).scrollMock,
);
const mockCollapseSettled = vi.fn();
const mockHasPendingDecision = vi.fn(() => false);
const mockDropTurnDecisions = vi.fn();
vi.mock("../decision-dock.js", () => ({
  pushDecision: vi.fn(),
  collapseSettledDecision: mockCollapseSettled,
  hasPendingDecision: mockHasPendingDecision,
  dropTurnDecisions: mockDropTurnDecisions,
}));

vi.mock("../attachments.js", () => ({
  addAttachment: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: composer-state.ts is in
  // this graph now (the tab projection reaches it), and it imports the rest.
  addAttachmentTo: vi.fn(),
  attachmentGeneration: vi.fn(() => 0),
  takeAttachments: vi.fn(() => []),
  stashAttachments: vi.fn(),
  flushAttachments: vi.fn(),
  restoreAttachments: vi.fn(),
  dropAttachments: vi.fn(),
  seedAttachments: vi.fn(),
  adoptRemoteAttachments: vi.fn(),
  _resetAttachmentsForTest: vi.fn(),
}));

const mockSetAgentDown = vi.fn();
const mockClearAgentDown = vi.fn();
vi.mock("../send-state.js", () => ({
  setAgentDown: mockSetAgentDown,
  clearAgentDown: mockClearAgentDown,
  setSSEStatus: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: steer-resend.js is in this graph
  // (the settled arm fires the boundary resend) and imports the name for its
  // give-up path, which no case here reaches.
  reportSendRefused: vi.fn(),
}));

// The boundary resend is driven for REAL in this file — the settled arm is its one
// firing point, so mocking it would leave the feature's whole trigger unpinned — and
// only its two outward calls are replaced. `sendPromptTo` is what a resent turn IS,
// and `clearSteers` would otherwise POST for every boundary in the file.
const mockSendPromptTo = vi.fn((_chatID: string, _text: string, _opts?: { messageID?: string }) =>
  Promise.resolve<"sent" | "failed">("sent"),
);
vi.mock("../chat-commands.js", () => ({
  sendPromptTo: mockSendPromptTo,
  switchModel: vi.fn(),
}));
const mockClearSteers = vi.fn(() => Promise.resolve(true));
vi.mock("../actions/chat.js", async (importOriginal) => ({
  ...(await importOriginal<typeof ChatActions>()),
  clearSteers: { dispatch: mockClearSteers },
}));

const mockReportFailure = vi.fn();
vi.mock("../failure-notice.js", () => ({
  reportFailure: mockReportFailure,
  // Present-but-undefined so real-ESM linking succeeds: actions/chat.js is in this
  // graph and imports the name, and Browser Mode links for real rather than
  // reading properties off a namespace object. No path under test calls it.
  clearFailure: undefined,
}));

// There is no isPermissionNeededEnabled to mock: the permission ask has no
// per-kind switch, so the three ask handlers notify unconditionally and only the
// master gate inside notifyIfHidden applies.
// A HOLDER rather than a constant, so the agent-finished notification can be driven
// in its own block while staying off for every other case in this file — the
// permission-class block below depends on that contrast to be non-vacuous.
//
// Through `vi.hoisted` because the factory below CLOSES OVER it: the mocker resolves
// the factory above this file's own top-level initializers, so a plain `const` is in
// its temporal dead zone at that moment and the whole file dies in module linking
// with a generic "there was an error when mocking a module".
const { mockNotifyIfHidden, notifyGate } = vi.hoisted(() => ({
  mockNotifyIfHidden: vi.fn(),
  notifyGate: { agentFinished: false },
}));
vi.mock("../notify.js", () => ({
  notifyIfHidden: mockNotifyIfHidden,
  setBadge: vi.fn(),
  isAgentFinishedEnabled: () => notifyGate.agentFinished,
  NOTIFY_TITLE: "vibekit",
}));

const mockOpenSetting = vi.fn();
vi.mock("../settings-highlight.js", () => ({ openSetting: mockOpenSetting }));

// The sign-in CTA's destination. Mocked because a call into it is a command at the
// handler's boundary, and the real module wires a whoami poll at import.
const mockShowLoginModal = vi.fn();
vi.mock("../modals.js", () => ({ showLoginModal: mockShowLoginModal }));

vi.mock("../git.js", () => ({ refreshGitBadge: vi.fn() }));

// Only the two FETCHING functions are replaced, and only for their fetch. turn.ts
// fires refreshTurnRail fire-and-forget on every turn frame, and it was the one
// module in this graph still reaching api-client: the real one issues
// GET /api/chats/{id}/turns, which the page sends at its own base URL, so each frame
// left a request in flight for the window teardown to abort and print as an
// unhandled AbortError. The count varied run to run (0-9 across the suite)
// because it was a race between the request failing and the file finishing, which
// is why it never failed a test and never stayed fixed either. Spreading the real
// module keeps the rest of the rail's behaviour (railRows, observeTurns) honest.
vi.mock("../turn-rail.js", async (importOriginal) => ({
  ...(await importOriginal<TurnRailModule>()),
  loadTurnRail: vi.fn(() => Promise.resolve()),
  refreshTurnRail: vi.fn(() => Promise.resolve()),
}));

// Capture SSE handlers via shared helper.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());
import { refreshTurnRail } from "../turn-rail.js";

// Import after mocks so turn.ts registers its handlers against the bus mock.
const { ERROR_ROUTES } = await import("./turn.js");
// After the mocks for the same reason: this pulls api-client into the graph, and the
// window merge under test is the REAL one — mocking the loader would leave the run arm's
// refusal asserted against a fake that re-implements the very merge it protects.
const { loadMessages } = await import("../store-load.js");
// After the mocks for the same reason turn.ts is: the cue module imports ../notify.js,
// so a STATIC import here links it against the real module before the mocker is ready
// and the whole file dies in module linking.
const { forgetDeferredCue, hasDeferredCue } = await import("../agent-finished-cue.js");
// After the mocks for the same reason: it reaches chat-commands and actions/chat, both
// replaced above. The REAL module, because the settled arm is the resend's one firing
// point and a mock would leave the trigger unpinned; `forgetSteerResend` is how each
// case resets the per-chat slot it holds.
const { forgetSteerResend, noteBoundaryDrop } = await import("../steer-resend.js");

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: "seeded",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    messages: [],
    message_count: 0,
    has_more: false,
    thinking: false,
    working_label: "Thinking",
    ...over,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  // The armed slot is per chat and the module is cached, so the reset is its own
  // forget rather than a re-import.
  for (const id of ["chat-1", "chat-2"]) {
    forgetSteerResend(id);
  }
  mockSendPromptTo.mockResolvedValue("sent");
  // `mockReset` is on, so an implementation set at construction is gone by now. A null
  // answer is what a 404 gives, which is what every case that does not stub a window wants.
  mockApiGetTyped.mockResolvedValue(null);
  setSessions([]);
  // A messages container with one assistant message — the turn_ended handler
  // appends the turn-summary as a sibling of the last assistant message.
  document.body.innerHTML = '<div id="messages"><div class="message assistant"></div></div>';
});

describe("ERROR_ROUTES", () => {
  // A route carries a SURFACE and an optional in-app remedy, and nothing else. There
  // is deliberately no turn-scoped field: whether a failure finalized a turn is a
  // property of the emission, so the server states it per frame — see the two
  // no-turn cases in the "error handler" block below for what a per-code answer cost.
  const expectedRoutes: [
    string,
    {
      surface: string;
      action?:
        | { kind: "setting"; tab: string; control: string; label: string }
        | { kind: "sign-in"; label: string };
    },
  ][] = [
    ["agent_not_found", { surface: "toast" }],
    // A routed error that also names a Settings control: the payload carries a
    // .kiro/agents path, so the toast carries a jump to Custom instructions.
    [
      "agent_config_error",
      {
        surface: "toast",
        action: {
          kind: "setting",
          tab: "instructions",
          control: "steering-input",
          label: "Open custom instructions",
        },
      },
    ],
    // The runtime is running UNAUTHENTICATED, so the session opened and
    // everything behind it will fail. The only fix is signing in, and there is no
    // Settings control for that — which is why the action is a discriminated
    // union rather than a Settings jump with a stretched meaning.
    // The server marks this one turn-scoped AND it carries an action, which is the
    // pair that keeps the suppression honest: the turn it failed holds the reason
    // inline, and the toast is still raised because Sign in is reachable from
    // nowhere else on screen.
    [
      "auth_token_unavailable",
      {
        surface: "toast",
        action: { kind: "sign-in", label: "Sign in" },
      },
    ],
    ["rate_limit", { surface: "toast" }],
    ["compaction_failed", { surface: "toast" }],
    // The four failed-ATTEMPT codes. Each ends the turn and each leaves a
    // promptable chat behind, which is why none of them reaches the send button:
    // an alert icon on the control whose job is to send claims the chat is dead,
    // and it is not. The reason lands on a toast and on the turn's own divider.
    ["switch_failed", { surface: "toast" }],
    ["prompt_failed", { surface: "toast" }],
    // A pick refused before it reached the wire: same surface as switch_failed,
    // which is the other half of choosing a model.
    ["model_not_served", { surface: "toast" }],
    // Empty-turn recovery could not respawn or resend. Routed explicitly rather
    // than left to the unknown-code fallthrough, on the one error whose meaning is
    // "the automatic repair failed".
    ["recovery_failed", { surface: "toast" }],
    // The ONE code that earns the send button's alert face: kiro-cli could not be
    // spawned, so there is no ACP connection behind this chat to send to. Every
    // other code here happened to a live agent.
    ["bridge_start_failed", { surface: "agent-down" }],
    // The chat runs, just not in the requested mode, and one click on the mode
    // pill fixes it — so it reports without touching the send button.
    ["mode_not_applied", { surface: "toast" }],
    // The chat runs, it just will not ask before writing, and one click on the
    // supervised switch fixes it — the same shape as its mode sibling. Mapped
    // rather than left to the fallthrough: the generic failure surface would claim
    // the turn failed when the turn is fine.
    ["supervised_not_applied", { surface: "toast" }],
  ];

  it.each(expectedRoutes)("routes %s to the expected surface and action", (code, expected) => {
    expect(ERROR_ROUTES[code as keyof typeof ERROR_ROUTES]).toEqual(expected);
  });

  it("contains exactly the codes this table claims to route", () => {
    expect(Object.keys(ERROR_ROUTES).sort()).toEqual(expectedRoutes.map(([c]) => c).sort());
  });

  it("returns undefined for codes not in the table", () => {
    expect(ERROR_ROUTES["unknown_code" as keyof typeof ERROR_ROUTES]).toBeUndefined();
    expect(ERROR_ROUTES["" as keyof typeof ERROR_ROUTES]).toBeUndefined();
  });
});

describe("turn_ended turn summary → store", () => {
  // The handler no longer writes DOM; it stamps the turn's summary metadata
  // onto the last assistant message via setTurnSummary. The renderer then
  // projects it into a keyed .turn-footer, and the text formatting is covered
  // by fundamentals/turn-footer.test.ts. Here we assert the handler→store wire.
  function seedWithAssistant(): void {
    setSessions([
      makeSession("chat-1", {
        messages: [{ id: "a1", role: "assistant", ts: 1, content: "hi" }],
        message_count: 1,
      }),
    ]);
    setActive("chat-1");
  }

  it("stamps credits + elapsed onto the last assistant message", () => {
    seedWithAssistant();
    fireSSE("turn_ended", "chat-1", { credits_delta: 1.5, elapsed_ms: 2000 });
    const m = get("chat-1")?.messages[0];
    expect(m?.turn_credits).toBe(1.5);
    expect(m?.turn_elapsed_ms).toBe(2000);
  });

  it("stamps changed_files onto the last assistant message", () => {
    seedWithAssistant();
    fireSSE("turn_ended", "chat-1", {
      changed_files: {
        "a.ts": { lines_added: 5, lines_removed: 2 },
        "b.ts": { lines_added: 1, lines_removed: 0 },
      },
    });
    const m = get("chat-1")?.messages[0];
    expect(Object.keys(m?.changed_files ?? {})).toEqual(["a.ts", "b.ts"]);
  });

  it("stamps nothing when there are neither credits nor elapsed nor files", () => {
    seedWithAssistant();
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    const m = get("chat-1")?.messages[0];
    expect(m?.turn_credits).toBeUndefined();
    expect(m?.turn_elapsed_ms).toBeUndefined();
    expect(m?.changed_files).toBeUndefined();
    expect(m?.turn_model).toBeUndefined();
  });

  it("stamps the model that served the turn", () => {
    seedWithAssistant();
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", model: "sonnet-4" });
    expect(get("chat-1")?.messages[0]?.turn_model).toBe("sonnet-4");
  });

  // The server omits the field when it cannot name a model, and a blank string
  // would read as an attributed turn. Both absences have to leave it undefined.
  it("leaves the model undefined when the payload names none", () => {
    seedWithAssistant();
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", model: "" });
    expect(get("chat-1")?.messages[0]?.turn_model).toBeUndefined();
  });
});

describe("turn_ended side effects", () => {
  it("clears the thinking flag on the chat", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(get("chat-1")?.thinking).toBe(false);
  });

  // The server's own liveness statement, set FALSE because a closer ran: the record
  // is final and the carrier's `message_appended` echo is already on its way. It has
  // to be cleared here or `turnLive` keeps reporting the turn live off a stale
  // `turn_open: true` until the next refetch, and the newest turn reads `running`
  // instead of the outcome that just arrived.
  //
  // Written at the CALL SITE rather than inside `clearTurnState`, deliberately —
  // that function also runs on `transport:gap`, where dropping the last server
  // statement at the exact moment `thinking` is also cleared is the gap-path flash
  // the field exists to remove.
  it("marks the server's turn_open statement closed", () => {
    setSessions([makeSession("chat-1", { thinking: true, turn_open: true })]);
    setActive("chat-1");
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(get("chat-1")?.turn_open).toBe(false);
  });

  // The withheld-output note's teardown, at the door that ends the turn. The cap
  // sends only the TAIL of a big in-flight turn and the note says so; once the turn
  // is over `message_appended` has delivered the whole message, so a note left
  // standing claims output is still coming for a turn that finished. The clear is
  // inside `clearTurnState`, so the GAP door gets it too.
  it("clears the capped-snapshot markers on turn end", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    noteTruncatedSnapshot("chat-1", "m1");
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(true);

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(isTruncatedSnapshot("chat-1", "m1")).toBe(false);
  });

  // KAS clears its steering buffer at every turn boundary, and on the ordinary
  // path — every steer injected — it sends no steer_cleared because there was
  // nothing left to drop. So the handler has to clear locally or a delivered
  // chip would outlive the turn it belonged to.
  it("clears the chat's steers on turn end", () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    expect(steerCount("chat-1")).toBe(1);

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(steerCount("chat-1")).toBe(0);
  });

  // NO active-chat gate here, deliberately, and this is the store half of the
  // reported symptom: the reader leaves the tab, the turn ends server-side, and
  // the dock they come back to is empty. It is RIGHT to be empty — KAS clears its
  // steering buffer at every boundary, so a row still waiting was never read and
  // can never post — which is exactly why the record has to survive it.
  it("clears them for a background (non-active) chat too", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(steerCount("chat-1")).toBe(0);
  });

  // A boundary drop is not a deletion: "I sent this and the agent never read it"
  // is the one fact about a steer the reader could not learn any other way, so
  // each waiting row leaves the dock as a `dropped: true` mark carrying its text.
  // The mark is the RECORD; the resend below is what carries the text forward.
  // Characterized on a BACKGROUND chat because that is the trigger the reader
  // described.
  it("promotes each waiting steer of a background chat as undelivered", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    appendMessage("chat-1", { id: "u-1", role: "user", ts: 1, content: "go" });
    appendMessage("chat-1", {
      id: "a-1",
      role: "assistant",
      ts: 2,
      content: "",
      blocks: [{ type: "text", text: "partial" }],
    });
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });

    expect(steerCount("chat-1")).toBe(0);
    expect(steerMarks("chat-1")).toEqual([
      {
        id: "steer-1",
        text: "first",
        origin: "user",
        dropped: true,
        anchor: { msgID: "a-1", blockIndex: 1 },
      },
      {
        id: "steer-2",
        text: "second",
        origin: "user",
        dropped: true,
        anchor: { msgID: "a-1", blockIndex: 1 },
      },
    ]);
  });

  // ---------------------------------------------------------------------------
  // THE UNREAD MESSAGE IS SENT AS THE NEXT TURN, and this arm is its one firing
  // point: both boundary origins converge on the settled `turn_ended` frame. The
  // join, precedence and retry ladder are steer-resend.test.ts's; these cases own
  // that the trigger fires with the right payload, and not for another turn's end.
  // ---------------------------------------------------------------------------

  // ORIGIN 1: the turn ended on its own with the message still unread. No
  // `steer_cleared` came, so this arm is both the capture and the fire.
  it("sends an unread steer as a new turn when the turn ends on its own", async () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "actually target main", origin: "user" });

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[0]).toBe("chat-1");
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("actually target main");
  });

  // ORIGIN 2: the reader pressed stop. KAS's `cancel()` drains its buffer and emits
  // `steering_cleared` from inside the handler, and vibekit's `turn_ended` follows —
  // so the clear frame CAPTURES and this arm fires the same slot. ONE mechanism, and
  // this case is what proves the two do not double-send.
  it("sends it once when a manual stop cleared the buffer first", async () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "stop and do this", origin: "user" });
    // The clear frame's own capture, spelled the way handlers/steer.ts spells it.
    noteBoundaryDrop("chat-1", pendingSteerCarry("chat-1", ["steer-1"]));
    dropSteers("chat-1", ["steer-1"]);
    expect(mockSendPromptTo).not.toHaveBeenCalled();

    fireSSE("turn_ended", "chat-1", { stop_reason: "cancelled", outcome: "cancelled" });

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("stop and do this");
  });

  // Several unread messages are ONE new turn, joined by a blank line in the order
  // they were typed — not N turns, which would make the agent answer each in
  // isolation, and not a re-sort.
  it("concatenates several unread steers into one new turn", async () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "first", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-2", text: "second", origin: "user" });
    recordSteerQueued("chat-1", { id: "steer-3", text: "third", origin: "user" });

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });

    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });
    expect(mockSendPromptTo.mock.calls[0]?.[1]).toBe("first\n\nsecond\n\nthird");
  });

  // THE LOOP GUARD, and it is structural rather than a counter: a turn opened by a
  // resend ends with an empty dock, so the capture arms nothing and nothing fires.
  it("opens nothing further when the resent turn ends with nothing pending", async () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    await vi.waitFor(() => {
      expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
    });

    // The resent turn's own end.
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).toHaveBeenCalledTimes(1);
  });

  // A turn that ended with everything read has nothing to carry, and this is the
  // common case, so it must cost no POST at all.
  it("sends nothing when the agent read everything", async () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
    expect(mockClearSteers).not.toHaveBeenCalled();
  });

  // A row still SENDING is excluded: its own POST is still resolving, and submit.ts
  // already converts a `no_turn` refusal of it into a prompt — so resending it here
  // would send one message twice.
  it("leaves a still-sending steer to its own POST", async () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");
    recordSteerSent("chat-1", "m-1", "still in flight");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  // turn_ended is the only moment the set of turns changes, so it is the only
  // moment the rail re-reads its session-wide index — including the FIRST turn
  // of a chat that was empty when it was activated, whose marker exists nowhere
  // until this fires. The id has to be the frame's, and the rail only adopts a
  // result for the chat it was pointed at, so this and the activation-time
  // pointing are two halves of one thing.
  it("re-reads the rail's index for the chat the frame names", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(refreshTurnRail).toHaveBeenCalledWith("chat-1");
  });

  // The dot's headline promise is "your background chat finished", and the signal
  // it used to rest on — the agent's own `completed` status — only arrives when
  // the model calls update_session_information. A turn that ended without one
  // fell to `idle`, so the promise held only sometimes. turn_ended always
  // arrives, which is why the latch lives on this handler.
  it("latches done for a background chat whose agent never declared completed", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", outcome: "completed" });
    expect(get("chat-1")?.agent_status).toBeUndefined();
    expect(tabStatusFor(get("chat-1"))).toBe("done");
  });

  it("latches DONE for a CANCELLED turn, because a turn ended here", () => {
    // The hollow `idle` ring means the chat has not initiated (user ruling,
    // 2026-09-04), so a chat the reader watched work and then stopped may not
    // paint it — that reads as "nothing has ever happened here". `done` is the
    // transport's verdict that a turn FINISHED, not the agent's claim that it
    // succeeded, which is why it is the honest answer for a stop.
    //
    // This REPLACES the earlier case here, which asserted `idle` on the grounds
    // that a green dot over-claims success. That argument lost to the ring's own
    // meaning; `runStatusFor` had already made the same call for a run's dot.
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "cancelled", outcome: "cancelled" });
    expect(tabStatusFor(get("chat-1"))).toBe("done");
  });

  it("latches done for the chat the reader is watching too", () => {
    // The dot has to be able to turn GREEN in front of the reader. It could not
    // until 2026-08: `done` meant "finished while you were away", so the active tab
    // with the page in front of you was skipped and its dot fell back to hollow
    // `idle` at the exact moment its turn completed — the one state that says "I am
    // done" was the one state you could never watch happen. web-terminal-kiro
    // latches its own `done` in the engine, focus-blind, so this is the same rule.
    // Nothing is lost on the attention side: attention.ts acknowledges a cue on the
    // watched chat as it observes it, so the title count and favicon still ignore
    // this one (attention-wiring.test.ts pins that half).
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", outcome: "completed" });
    expect(tabStatusFor(get("chat-1"))).toBe("done");
  });

  // The OUTCOME decides, not the stop reason, and this is what that buys: a turn
  // that streamed an answer and then failed used to latch `done` — everything but
  // a cancel did — so a failure the reader was not watching showed a green
  // "finished" dot. The server now says how the turn ended and the dot follows it.
  it("latches FAILED, not done, for a turn that failed after streaming", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "error", outcome: "failed" });
    expect(tabStatusFor(get("chat-1"))).toBe("failed");
  });

  it("latches failed for a refusal, which produced no work", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "refusal", outcome: "refused" });
    expect(tabStatusFor(get("chat-1"))).toBe("failed");
  });

  it("still prefers the agent's own verdict where it lands", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    setAgentStatus("chat-1", "waiting_on_user", "over to you");

    // A finished turn that left a question behind is a chat that WANTS something,
    // not a chat that is done, and the agent is the only thing that knows which.
    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", outcome: "completed" });
    expect(tabStatusFor(get("chat-1"))).toBe("waiting");
  });

  // Every ask BLOCKS its turn, so a turn that has ended is not waiting on one.
  // What is left in the queue is an abandoned card (cmdCancel already cleared the
  // server's own pending set), and `input` outranks every other state — so the
  // chat claimed it needed a decision indefinitely.
  it("discards the turn's abandoned asks before re-deriving the dot", () => {
    setSessions([makeSession("chat-1")]);
    setActive("chat-1");

    fireSSE("turn_ended", "chat-1", { stop_reason: "cancelled" });
    expect(mockDropTurnDecisions).toHaveBeenCalledWith("chat-1");
  });

  it("latches FAILED for an interrupted turn, which a fault nobody chose stopped", () => {
    // The mapping this handler used to get wrong, and the one that made the live
    // page and the next reload of it disagree: `interrupted` latched nothing here
    // while every other surface in the app already read it as a fault, so a turn a
    // dropped connection killed showed idle's hollow ring until the reader
    // refreshed, at which point the header seed painted a solid failed dot.
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "interrupted", outcome: "interrupted" });
    expect(tabStatusFor(get("chat-1"))).toBe("failed");
  });

  it("clears no verdict for a turn it cannot grade", () => {
    // The handler routes through `applyLatch(outcomeLatch(p.outcome))`, and the empty
    // answer that table returns writes NEITHER field — so a frame the wire could not
    // grade leaves the verdict the previous turn latched standing. A local mapping is
    // free to blank one instead, which is the direction that loses a failure the reader
    // has not seen yet: `setThinking(false)` clears no latch either, so this handler is
    // the only thing on the path that could.
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    setTurnFailed("chat-1");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(tabStatusFor(get("chat-1"))).toBe("failed");
  });
});

// ---------------------------------------------------------------------------
// A frame reporting ANOTHER turn's end, keyed on the frame's own two markers.
//
// `TurnEndedPayload` carries no turn identity, so `superseded` and `workflow_step`
// ARE the identity: applying the chat-scoped effects to either retracted the
// liveness of a prompt that had just landed, which is what painted the
// unreadable-end notice over it seconds before the real reply.
//
// The outcome is deliberately `completed` in every case here, and that is the whole
// inversion: the gate used to key on `outcome === "unknown"`, which both misses a
// displaced turn that ended cleanly and settles nothing for a `closerWireEnd` whose
// stop reason was merely unmeasured. `unknown` is now an ordinary settled outcome
// and is pinned as one in the table below.
// ---------------------------------------------------------------------------

describe("turn_ended reporting another turn's end", () => {
  /** A chat whose own prompt is in flight: both liveness inputs set, one assistant
   *  message for the summary to land on. */
  function seedLive(): void {
    setSessions([
      makeSession("chat-1", {
        thinking: true,
        turn_open: true,
        messages: [{ id: "a1", role: "assistant", ts: 1, content: "hi" }],
        message_count: 1,
      }),
    ]);
    setActive("chat-1");
  }

  it("retracts neither liveness input for a displaced turn", () => {
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      superseded: true,
    });
    expect(get("chat-1")?.thinking).toBe(true);
    expect(get("chat-1")?.turn_open).toBe(true);
  });

  it("latches no verdict for the tab dot on a displaced turn", () => {
    // `outcomeLatch("completed")` maps to "done", so an ungated latch turns a chat
    // whose replacement turn is running green.
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      superseded: true,
    });
    expect(get("chat-1")?.turn_done).toBeUndefined();
    expect(tabStatusFor(get("chat-1"))).toBe("working");
  });

  it("does not promote an unread steer as undelivered", () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      superseded: true,
    });
    expect(steerCount("chat-1")).toBe(1);
    expect(steerMarks("chat-1")).toEqual([]);
  });

  // And it sends nothing: the replacement turn is running right now, so the agent can
  // still read that steer. Resending it here would open a turn against a live one and
  // duplicate a message that is about to be delivered.
  it("sends no resend for a displaced turn's end", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      superseded: true,
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  it("retires no pending ask", () => {
    // The sweep keeps only RUN-scoped asks, so ungated it retires the asks of a turn
    // that is still running — a step turn closing on a launching chat that holds its
    // own live turn, which is the case `handlers/run.ts` guards its copy against.
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      workflow_step: true,
    });
    expect(mockDropTurnDecisions).not.toHaveBeenCalled();
  });

  it("stamps no summary onto the newest assistant message", () => {
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      workflow_step: true,
      credits_delta: 1.5,
      elapsed_ms: 2000,
      changed_files: { "a.ts": { lines_added: 5, lines_removed: 2 } },
    });
    const m = get("chat-1")?.messages[0];
    expect(m?.turn_credits).toBeUndefined();
    expect(m?.turn_elapsed_ms).toBeUndefined();
    expect(m?.changed_files).toBeUndefined();
  });

  it("keeps the live-turn window open on a run frame while retracting thinking", () => {
    // The run arm's ONE effect and the four it refuses: `turn_open` is the newest-page
    // window's own statement and has one writer, and the live-turn message marker is what
    // stops the next `loadMessages` deleting the reply still streaming.
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      workflow_step: true,
    });
    expect(get("chat-1")?.thinking).toBe(false);
    expect(get("chat-1")?.turn_open).toBe(true);
  });

  it("re-derives the dot from the resident transcript with no step carrier resident", () => {
    // What the arm refuses is `applyLatch(outcomeLatch(p.outcome))`: the frame reports a
    // FAILED step, and with no carrier of its own resident the retained `completed` is the
    // source of the verdict. Where a step DID persist a carrier the re-derivation may land
    // the step's verdict, which is the answer a reload gives too.
    setSessions([
      makeSession("chat-1", {
        thinking: true,
        messages: [{ id: "m1", role: "assistant", ts: 1, turn_outcome: "completed" } as never],
        message_count: 1,
      }),
      makeSession("chat-2"),
    ]);
    setActive("chat-2");
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "error",
      outcome: "failed",
      workflow_step: true,
    });
    expect(tabStatusFor(get("chat-1"))).toBe("done");
  });

  it("leaves an unread steer waiting on a run frame", () => {
    // The fourth gated write the run arm refuses, beside the three above. `dropSteers`
    // promotes each unread steer as "not delivered", and KAS clears its steering buffer
    // at the CHAT's turn boundary — so a run's turn ending says nothing about it, and
    // promoting here reports a steer as dropped while the agent can still read it.
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      workflow_step: true,
    });
    expect(steerCount("chat-1")).toBe(1);
    expect(steerMarks("chat-1")).toEqual([]);
  });

  // The fifth: a run's step ending says nothing about the chat's own turn, which may
  // be live right now, so resending would post a prompt into a running turn.
  it("sends no resend for a run's turn end", async () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      workflow_step: true,
    });
    await new Promise((r) => setTimeout(r, 0));
    expect(mockSendPromptTo).not.toHaveBeenCalled();
  });

  it("reads BOTH markers as displaced, so the run arm's retraction does not run", () => {
    // `scopeOf` keys on `superseded` FIRST, and that order is the safe one: a replacement
    // turn is running right now, so the run arm's `retractStaleThinking` would clear the
    // `thinking` that turn just set. A step turn a prompt displaced reports both.
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      superseded: true,
      workflow_step: true,
    });
    expect(get("chat-1")?.thinking, "the run arm would have cleared this").toBe(true);
    expect(get("chat-1")?.turn_open).toBe(true);
  });

  it("still runs the effects that are OUTSIDE the gate", () => {
    // The gate covers seven effects, not the handler: this chat's turn index changed
    // whoever the turn belonged to, and a frame arriving at all proves an agent is behind
    // the chat. Without this row every case above passes just as well for a handler that
    // returns early on either marker.
    seedLive();
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      superseded: true,
    });
    expect(refreshTurnRail).toHaveBeenCalledWith("chat-1");
    expect(mockClearAgentDown).toHaveBeenCalled();
  });

  it("leaves a concurrent turn's unpersisted reply alive across the next window load", async () => {
    // The run arm's refusal asserted where it COSTS something. `clearLiveTurnMessage` is
    // harmless at the frame itself; what it breaks is the next newest-page load, which
    // reads that marker to keep the one message the server's answer structurally cannot
    // carry (the buffer is unflushed until the chat's own turn_ended). So the damage lands
    // on a fetch, seconds later, as the streaming reply disappearing.
    setSessions([
      makeSession("chat-1", {
        thinking: true,
        turn_open: true,
        messages: [
          { id: "u1", role: "user", ts: 1, content: "go" },
          { id: "live-1", role: "assistant", ts: 2, content: "half a repl" },
        ],
        message_count: 1,
      }),
    ]);
    setActive("chat-1");
    noteLiveTurnMessage("chat-1", "live-1");

    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      workflow_step: true,
    });
    expect(liveTurnMessage("chat-1"), "the marker survives the run frame").toBe("live-1");

    // The page the server can actually serve: the chat file holds the prompt and nothing
    // else. `turn_open: true` keeps this off the heal arm, which is a different door and
    // is licensed to drop the marker.
    mockApiGetTyped.mockResolvedValue({
      chat: { id: "chat-1", name: "seeded", message_count: 1 },
      messages: [{ id: "u1", role: "user", ts: 1, content: "go" }],
      has_more: false,
      draft: "",
      turn_open: true,
      turn_offset: undefined,
      turn_segment_closed: undefined,
      live_turn: undefined,
    });
    await loadMessages("chat-1");

    expect(get("chat-1")?.messages.map((m) => m.id)).toEqual(["u1", "live-1"]);
  });

  // The NEGATIVE CONTROL for all of them: without it each passes just as well when the
  // handler stops applying these effects for every frame.
  it("applies every one of them for a frame carrying neither marker", () => {
    seedLive();
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });
    fireSSE("turn_ended", "chat-1", {
      stop_reason: "end_turn",
      outcome: "completed",
      credits_delta: 1.5,
      elapsed_ms: 2000,
      changed_files: { "a.ts": { lines_added: 5, lines_removed: 2 } },
    });
    expect(get("chat-1")?.thinking).toBe(false);
    expect(get("chat-1")?.turn_open).toBe(false);
    expect(tabStatusFor(get("chat-1"))).toBe("done");
    expect(mockDropTurnDecisions).toHaveBeenCalledWith("chat-1");
    expect(steerCount("chat-1")).toBe(0);
    const m = get("chat-1")?.messages[0];
    expect(m?.turn_credits).toBe(1.5);
    expect(m?.turn_elapsed_ms).toBe(2000);
    expect(Object.keys(m?.changed_files ?? {})).toEqual(["a.ts"]);
  });
});

// ---------------------------------------------------------------------------
// The three producers of the turn verdict, side by side.
//
// A chat's dot can be set from three places, and they must not disagree: this
// handler on a LIVE turn_ended, `relatchTurnVerdict` re-deriving from the loaded
// TRANSCRIPT, and `latchFieldsFor` (through `upsertHeader`) deriving from the chat
// HEADER a fresh browser rebuilds every Session from. All three read one table
// now; each used to spell the mapping out for itself, and the live one graded
// `interrupted` differently from the other two.
//
// This is the only file that can hold the comparison: it is the only one carrying
// the mock set the live handler's module graph needs.
// ---------------------------------------------------------------------------

describe("the three turn-verdict producers agree", () => {
  /** outcome -> the dot the LIVE handler paints. Hardcoded, never derived from
   *  `outcomeLatch`: an expectation computed by the code under test passes for any
   *  mapping, including the one that shipped the defect. */
  const liveCases: [TurnOutcome, string][] = [
    // A turn that ended: `done` unless it BROKE. Not one of these six may reach
    // `idle`, because the hollow ring means the chat has not initiated.
    ["completed", "done"],
    ["failed", "failed"],
    ["refused", "failed"],
    ["interrupted", "failed"],
    ["cancelled", "done"],
    // `unknown` is IN this table now, and its row is the inversion: the live door used
    // to decline it, because the gate keyed on the outcome rather than on the frame's
    // markers. All three doors grade it `done` — never `failed`, which would invent a
    // failure the wire never reported.
    ["unknown", "done"],
    // `running` is the one outcome that latches nothing, and it is also the one
    // that cannot reach a turn_ended in practice — the server stamps it only on
    // the API's live turn projection. So this row keeps the handler from claiming
    // a verdict for a turn that has not ended, and the chat is not left hollow
    // either: a live turn is `working` through `thinking`.
    ["running", "idle"],
  ];

  beforeEach(() => {
    setSessions([]);
  });

  for (const [outcome, want] of liveCases) {
    it(`paints ${want} live for a turn that ended ${outcome}`, () => {
      setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
      setActive("chat-2");

      fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", outcome });
      expect(tabStatusFor(get("chat-1"))).toBe(want);
    });
  }

  it("paints idle live for a turn_ended carrying no outcome at all", () => {
    // A build older than the field, or a turn the server could not grade. Neither
    // is evidence of a verdict.
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(tabStatusFor(get("chat-1"))).toBe("idle");
  });

  for (const [outcome] of liveCases) {
    it(`reaches one dot from all three doors for ${outcome}`, () => {
      // LIVE: the turn_ended handler, on a chat mid-turn.
      setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
      setActive("chat-2");
      fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn", outcome });
      const live = tabStatusFor(get("chat-1"));

      // TRANSCRIPT: the same outcome persisted on the newest message, re-derived
      // the way a message load does.
      setSessions([
        makeSession("chat-1", {
          messages: [{ id: "m1", role: "assistant", ts: 1, turn_outcome: outcome } as never],
        }),
      ]);
      relatchTurnVerdict("chat-1");
      const transcript = tabStatusFor(get("chat-1"));

      // HEADER: a chat this client has never seen live, rebuilt from the projection
      // `GET /api/chats` serves — the door a brand-new browser session comes through.
      setSessions([]);
      upsertHeader({
        id: "chat-1",
        name: "chat-1",
        message_count: 1,
        last_turn_outcome: outcome,
        usage: { context_pct: 0, context_size: 0, credits: 0, turns: 0, last_turn_ms: 0 },
      } as unknown as ChatHeader);
      const header = tabStatusFor(get("chat-1"));

      expect({ live, transcript, header }).toStrictEqual({
        live,
        transcript: live,
        header: live,
      });
    });
  }
});

describe("error handler", () => {
  // The turn lifecycle and the error PROSE are two different questions. The handler
  // used to clear `thinking` for every code, and `thinking` is what the renderer
  // reads to decide whether an assistant bubble subscribes to its own deltas — so a
  // `.kiro/agents` typo, which fires `agent_config_error` at session construction,
  // froze the whole first turn at its first streamed chunk.
  it("leaves the turn running for a routed error and reports it", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "rate_limit", message: "slow down" });
    expect(get("chat-1")?.thinking).toBe(true);
    // `false` is the turn-scoped flag: a rate-limit notice ends no turn, so it has
    // no transcript row to duplicate and keeps its toast whatever is on screen.
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "slow down", undefined, false);
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  it.each([
    "agent_not_found",
    "agent_config_error",
    "rate_limit",
    "compaction_failed",
    "mode_not_applied",
    "auth_token_unavailable",
  ])("keeps thinking set for %s, which says nothing about this turn", (code) => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code, message: "something is wrong elsewhere" });
    expect(get("chat-1")?.thinking).toBe(true);
  });

  // A routed error reaches a surface for EVERY chat, not only the one on screen: a
  // toast claims no shared control, unlike the send button below. failure-notice is
  // what names the chat, so the chat id arriving here is the whole contract.
  it("reports a BACKGROUND chat's config error, with its own remedy", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");
    fireSSE("error", "chat-1", { code: "agent_config_error", message: "bad agent front matter" });
    expect(get("chat-1")?.thinking).toBe(true);
    expect(mockReportFailure).toHaveBeenCalledWith(
      "chat-1",
      "bad agent front matter",
      expect.objectContaining({ label: "Open custom instructions" }),
      false,
    );
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  // A route that names a setting carries a working in-app jump, and one whose route
  // does not carries no affordance at all.
  it("gives agent_config_error a toast action that opens the named control", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "agent_config_error", message: "bad agent json" });

    const action = mockReportFailure.mock.calls[0]?.[2] as
      { label: string; onClick: () => void } | undefined;
    expect(action?.label).toBe("Open custom instructions");
    action?.onClick();
    expect(mockOpenSetting).toHaveBeenCalledWith("instructions", "steering-input");
  });

  it("passes no toast action for a routed error that names none", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "compaction_failed", message: "nope" });
    expect(mockReportFailure.mock.calls[0]?.[2]).toBeUndefined();
    expect(mockOpenSetting).not.toHaveBeenCalled();
  });

  // D106. Before this the auth failure existed only as one server log line and a
  // JSON-RPC error to KAS, and KAS's answer to that error is to run
  // unauthenticated — the chat opens and every turn fails with nothing on screen
  // saying the runtime is signed out.
  it("routes the auth failure to a toast carrying the sign-in CTA", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", {
      code: "auth_token_unavailable",
      message: "kiro-cli: refresh token expired",
      turn_scoped: true,
    });

    expect(mockReportFailure).toHaveBeenCalledWith(
      "chat-1",
      // kiro-cli's own reason travels through: it names which leg of the login
      // chain is dead, and no wording invented client-side is more specific.
      "kiro-cli: refresh token expired",
      expect.objectContaining({ label: "Sign in" }),
      // TURN-SCOPED, off the FRAME, and still raised: the remedy is the half an
      // inline row cannot offer, so failure-notice.ts never suppresses an
      // action-bearing notice. This is the code where both conjuncts are true at
      // once, which is what makes that clause real rather than defensive.
      true,
    );
    const action = mockReportFailure.mock.calls[0]?.[2] as
      { label: string; onClick: () => void } | undefined;
    action?.onClick();
    expect(mockShowLoginModal).toHaveBeenCalledTimes(1);
    // Not a Settings jump: the login modal is not in Settings at all.
    expect(mockOpenSetting).not.toHaveBeenCalled();
    // And not the send button: it is not one send that is broken.
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  // The 2026-08 routing change, and the assertion the user's complaint reduces
  // to: a throttle / 5xx / capacity failure goes to the toast, carrying the
  // server's prose VERBATIM (no `code: ` prefix — the code is machine vocabulary
  // in front of a human sentence), and it does NOT touch the send button.
  it.each(["prompt_failed", "recovery_failed", "switch_failed", "model_not_served"])(
    "routes %s to the toast and leaves the send button alone",
    (code) => {
      setSessions([makeSession("chat-1", { thinking: true })]);
      setActive("chat-1");
      fireSSE("error", "chat-1", { code, message: "boom", turn_scoped: true });
      expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "boom", undefined, true);
      expect(mockSetAgentDown).not.toHaveBeenCalled();
    },
  );

  // ---------------------------------------------------------------------------
  // TURN-SCOPEDNESS COMES OFF THE FRAME, NOT OFF THE CODE, and these two cases are
  // the defect that made it move. The flag decides whether failure-notice.ts drops
  // the toast for the chat the reader is already looking at, and it used to be a
  // per-code entry in ERROR_ROUTES — so every `prompt_failed` was read as having a
  // turn. Three of the five server emitters behind `prompt_failed` and
  // `recovery_failed` open no turn at all (a held bridge slot, a zero epoch, a
  // failed recovery respawn), and for those the toast is the ONLY surface: no turn
  // card, no footer mark, and the prompt POST already acked at admission. Under the
  // per-code flag they were suppressed on the active chat and reported nowhere.
  // ---------------------------------------------------------------------------

  it.each(["prompt_failed", "recovery_failed"])(
    "reports a %s that opened NO turn, even on the chat in front of you",
    (code) => {
      setSessions([makeSession("chat-1", { thinking: true })]);
      setActive("chat-1");
      // No `turn_scoped` on the frame: the emitter finalized nothing, so there is
      // no inline row for the toast to be a duplicate of.
      fireSSE("error", "chat-1", { code, message: "The prompt could not start." });
      expect(mockReportFailure).toHaveBeenCalledWith(
        "chat-1",
        "The prompt could not start.",
        undefined,
        false,
      );
    },
  );

  it.each(["prompt_failed", "recovery_failed"])(
    "suppresses a %s that DID finalize a turn, because that turn says it",
    (code) => {
      setSessions([makeSession("chat-1", { thinking: true })]);
      setActive("chat-1");
      fireSSE("error", "chat-1", { code, message: "at capacity", turn_scoped: true });
      expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "at capacity", undefined, true);
    },
  );

  it("reads a frame carrying no turn_scoped field as NOT turn-scoped", () => {
    // The compatibility direction, and the one that decides which way this fails
    // safe. A server that predates the field, or an emitter that forgets it, must
    // leave the failure REPORTED rather than trusting an inline row that is not
    // there. `false` is therefore the answer for an absent field and for an
    // explicit `false` alike.
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "compaction_failed", message: "nope" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "nope", undefined, false);
  });

  // NO error code touches the turn lifecycle: the server ends every turn exactly
  // once, so an error is a report.
  it.each([
    "prompt_failed",
    "bridge_start_failed",
    "rate_limit",
    "auth_token_unavailable",
    "mystery_code",
  ])("leaves the turn lifecycle alone for %s, whatever its surface", (code) => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code, message: "something happened" });
    expect(get("chat-1")?.thinking).toBe(true);
    expect(tabStatusFor(get("chat-1"))).not.toBe("failed");
  });

  // The one code that DOES earn the button's alert face: kiro-cli could not be
  // spawned, so this chat has no ACP connection behind it and the icon is a true
  // statement rather than a claim about one attempt.
  it("routes bridge_start_failed to the send button, not the toast", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "bridge_start_failed", message: "spawn failed" });
    expect(mockSetAgentDown).toHaveBeenCalledWith("spawn failed");
    expect(mockReportFailure).not.toHaveBeenCalled();
  });

  // A BACKGROUND chat's failure now reaches the user, which is the hole the old
  // routing left: the prose was dropped for every non-active chat, so a failed
  // background turn had nothing but a tab dot. A toast claims no shared control,
  // so it is safe to raise from any chat; the send button still is not.
  it("reports a background chat's failure and spares its send button", () => {
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");
    fireSSE("error", "chat-1", {
      code: "prompt_failed",
      message: "at capacity",
      turn_scoped: true,
    });
    // Turn-scoped, and raised anyway: chat-2 is on screen, so chat-1's own card is
    // not, and the toast is the only surface that can report at all.
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "at capacity", undefined, true);

    mockReportFailure.mockClear();
    fireSSE("error", "chat-1", { code: "bridge_start_failed", message: "spawn failed" });
    expect(mockSetAgentDown).not.toHaveBeenCalled();
  });

  it("falls through unknown codes to the toast", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "mystery_code", message: "huh" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "huh");
  });

  // An empty message is the only time machine vocabulary beats nothing.
  it("uses the code when an unknown error carries no message", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "mystery_code", message: "" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "mystery_code");
  });
});

// D103: the protected approval floor, at the client's notification site. There
// is no per-kind switch left, so all three turn-blocking asks reach
// notifyIfHidden — which is where the master switch is checked. The
// isAgentFinishedEnabled mock returns false, so a turn_ended notification would
// NOT fire; that contrast is what makes these assertions non-vacuous.
describe("the permission-class asks always notify", () => {
  it.each([
    ["permission_needed", { request_id: 1, options: [] }, "Permission needed"],
    ["elicitation_needed", { request_id: 2 }, "Input requested by a tool"],
    ["user_input_needed", { request_id: 3, options: [] }, "The agent has a question"],
  ])("%s notifies with no per-kind gate", (event, payload, body) => {
    fireSSE(event, "chat-1", payload);
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("vibekit", body);
  });

  it("uses the turn-approval wording when the ask carries files", () => {
    fireSSE("permission_needed", "chat-1", {
      request_id: 4,
      options: [],
      files: [{ path: "a.go", action_id: "act-1" }],
    });
    expect(mockNotifyIfHidden).toHaveBeenCalledWith("vibekit", "Review this turn's changes");
  });
});

// ---------------------------------------------------------------------------
// THE OFF-SCREEN NOTIFICATION, per outcome.
//
// It gated on `stop_reason !== "cancelled"` and then said `Agent finished`
// whatever had happened, so a turn that failed, was interrupted or was refused
// pushed a claim of success to a reader who was not looking — the one channel
// they had, saying the opposite of the truth. It reads the SEVERITY now.
//
// A distinct chat id per case, deliberately: the handler dedups within 2000ms per
// chat, so seven frames on one id would measure the dedup window rather than the
// mapping.
// ---------------------------------------------------------------------------

describe("the agent-finished notification reads the severity", () => {
  /** outcome -> the notification body, or "" for a turn that says nothing.
   *
   *  Hardcoded rather than derived through `severityOf`/`defaultFailureReason`: an
   *  expectation computed by the code under test passes for any mapping at all,
   *  including the one that shipped the defect. */
  const cases: [TurnOutcome, string][] = [
    ["completed", "seeded: Agent finished"],
    ["failed", "seeded: The agent reported an error and the turn stopped."],
    ["interrupted", "seeded: The turn was interrupted before the agent finished."],
    ["refused", "seeded: The model declined to continue."],
    // STOPPED. A cancel is what the reader asked for, and an end vibekit could not
    // read reports nothing about success, so neither earns a notification.
    ["cancelled", ""],
    ["unknown", ""],
    // `running` cannot reach a turn_ended in practice; the row keeps the handler from
    // claiming a verdict for a turn that has not ended.
    ["running", ""],
  ];

  beforeEach(() => {
    notifyGate.agentFinished = true;
  });
  afterEach(() => {
    notifyGate.agentFinished = false;
  });

  for (const [outcome, want] of cases) {
    it(`says ${want === "" ? "nothing" : `"${want}"`} for a turn that ended ${outcome}`, () => {
      const chatID = `notify-${outcome}`;
      setSessions([makeSession(chatID)]);
      fireSSE("turn_ended", chatID, { stop_reason: "end_turn", outcome });
      if (want === "") {
        expect(mockNotifyIfHidden).not.toHaveBeenCalled();
        return;
      }
      expect(mockNotifyIfHidden).toHaveBeenCalledWith("vibekit", want);
    });
  }

  it("never claims a broken turn finished", () => {
    // The property behind the rows above, and the direction the defect ran in: the
    // wording matters less than never saying `Agent finished` over a failure.
    for (const [outcome] of cases) {
      if (severityOf(outcome) !== "broken") {
        continue;
      }
      mockNotifyIfHidden.mockClear();
      const chatID = `broken-${outcome}`;
      setSessions([makeSession(chatID)]);
      fireSSE("turn_ended", chatID, { stop_reason: "end_turn", outcome });
      const body = String(mockNotifyIfHidden.mock.calls[0]?.[1] ?? "");
      expect(body, `${outcome} notified nothing at all`).not.toBe("");
      expect(body, `${outcome} claimed the agent finished`).not.toContain("Agent finished");
    }
  });

  it("covers a broken outcome, or the property above passes vacuously", () => {
    expect(cases.filter(([o]) => severityOf(o) === "broken").length).toBeGreaterThan(0);
  });

  it("still notifies nothing at all when the master switch is off", () => {
    // The gate the plan required to survive the rewrite: severity decides WHAT is
    // said, never WHETHER the user has asked to be told.
    notifyGate.agentFinished = false;
    setSessions([makeSession("gate-off")]);
    fireSSE("turn_ended", "gate-off", { stop_reason: "end_turn", outcome: "failed" });
    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  it("keeps the 2s dedup window, which an SSE replay burst needs", () => {
    setSessions([makeSession("dedup")]);
    fireSSE("turn_ended", "dedup", { stop_reason: "end_turn", outcome: "failed" });
    fireSSE("turn_ended", "dedup", { stop_reason: "end_turn", outcome: "failed" });
    expect(mockNotifyIfHidden).toHaveBeenCalledTimes(1);
  });
});

// ---------------------------------------------------------------------------
// THE REPORTED DEFECT: a chat that launched a workflow raised the cue at its own
// turn's end, which is when `run_workflow` returned rather than when the work was
// done — so an off-screen reader was told the agent had finished up to forty
// minutes early. `handlers/turn.ts` hands ONE fact to `agent-finished-cue.ts` now
// (the turn ended, and this is what a cue for it would say) and makes no decision
// about whether to raise it.
//
// `../run-store.js` is NOT mocked in this file, so the live-run inventory is the
// real one and the cases drive it directly. A distinct chat id per case for the
// same reason the block above uses one: the dedup window is per chat.
// ---------------------------------------------------------------------------

describe("the notification waits for the work, not just the turn", () => {
  beforeEach(() => {
    notifyGate.agentFinished = true;
  });
  afterEach(() => {
    notifyGate.agentFinished = false;
    for (const id of liveRunsSeeded) {
      noteRunSettled(id);
    }
    liveRunsSeeded.length = 0;
  });

  const liveRunsSeeded: string[] = [];
  function seedLiveRun(workflowID: string, chatID: string, executing = true): void {
    liveRunsSeeded.push(workflowID);
    noteRunLive(workflowID, chatID, executing);
  }

  it("says nothing when a run this chat launched is still live", () => {
    setSessions([makeSession("defer-clean")]);
    seedLiveRun("wf-defer-clean", "defer-clean");

    fireSSE("turn_ended", "defer-clean", { stop_reason: "end_turn", outcome: "completed" });

    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  // The withhold is about the reader's attention rather than the turn's verdict, so a
  // broken turn that launched a still-running run makes the same false claim.
  it("says nothing for a BROKEN turn while a run is live", () => {
    setSessions([makeSession("defer-broken")]);
    seedLiveRun("wf-defer-broken", "defer-broken");

    fireSSE("turn_ended", "defer-broken", { stop_reason: "end_turn", outcome: "failed" });

    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  // A run stopped on a person is exactly what a "finished" notification must not
  // claim is over, and it is the case the narrow store-eviction predicate answers
  // the wrong way.
  it("says nothing while the run is merely PAUSED", () => {
    setSessions([makeSession("defer-parked")]);
    seedLiveRun("wf-defer-parked", "defer-parked", false);

    fireSSE("turn_ended", "defer-parked", { stop_reason: "end_turn", outcome: "completed" });

    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
  });

  it("notifies immediately when nothing is outstanding", () => {
    setSessions([makeSession("defer-none")]);

    fireSSE("turn_ended", "defer-none", { stop_reason: "end_turn", outcome: "completed" });

    expect(mockNotifyIfHidden).toHaveBeenCalledWith("vibekit", "seeded: Agent finished");
  });

  // One busy conversation must not mute the rest of the workspace.
  it("notifies when the live run belongs to another chat", () => {
    setSessions([makeSession("defer-other")]);
    seedLiveRun("wf-defer-other", "some-other-chat");

    fireSSE("turn_ended", "defer-other", { stop_reason: "end_turn", outcome: "completed" });

    expect(mockNotifyIfHidden).toHaveBeenCalledWith("vibekit", "seeded: Agent finished");
  });

  // A manual or scheduled launch is parentless, so its lease names no chat and its
  // outcome travels on its own push rather than on a chat's.
  it("notifies when the live run is PARENTLESS", () => {
    setSessions([makeSession("defer-parentless")]);
    seedLiveRun("wf-defer-parentless", "");

    fireSSE("turn_ended", "defer-parentless", { stop_reason: "end_turn", outcome: "completed" });

    expect(mockNotifyIfHidden).toHaveBeenCalledWith("vibekit", "seeded: Agent finished");
  });

  // The switch decides WHETHER the reader is told; the deferral decides WHEN. A cue
  // must not be parked for a channel that is off, or switching it back on would
  // deliver a notification about work that finished while it was off.
  it("parks nothing at all when the master switch is off", () => {
    notifyGate.agentFinished = false;
    setSessions([makeSession("defer-gate-off")]);
    seedLiveRun("wf-defer-gate-off", "defer-gate-off");

    fireSSE("turn_ended", "defer-gate-off", { stop_reason: "end_turn", outcome: "completed" });

    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
    expect(hasDeferredCue("defer-gate-off")).toBe(false);
  });

  // The two silences are different states and only one of them is a deferral: a turn
  // that says nothing has nothing to park, so it must leave no cue behind that a
  // later settle could fire.
  it("parks nothing for a turn that says nothing", () => {
    setSessions([makeSession("defer-cancelled")]);
    seedLiveRun("wf-defer-cancelled", "defer-cancelled");

    fireSSE("turn_ended", "defer-cancelled", { stop_reason: "end_turn", outcome: "cancelled" });

    expect(mockNotifyIfHidden).not.toHaveBeenCalled();
    expect(hasDeferredCue("defer-cancelled")).toBe(false);
  });

  // The positive half of the withhold: a cue IS parked, so the release effect the
  // composition root installs has something to fire. Without this the two silent
  // cases above pass equally for a handler that dropped the notification entirely.
  it("parks the cue rather than dropping it", () => {
    setSessions([makeSession("defer-parked-cue")]);
    seedLiveRun("wf-defer-parked-cue", "defer-parked-cue");

    fireSSE("turn_ended", "defer-parked-cue", { stop_reason: "end_turn", outcome: "completed" });

    expect(hasDeferredCue("defer-parked-cue")).toBe(true);
    forgetDeferredCue("defer-parked-cue");
  });
});

describe("decision_settled handler", () => {
  it("hands the settled request to the dock, kind and attribution intact", () => {
    fireSSE("decision_settled", "chat-1", {
      kind: "user_input",
      settled_by: "unattended",
      request_id: 42,
    });
    // The handler routes and nothing else: the dock owns the queue, so the
    // arguments arriving unchanged IS the contract.
    expect(mockCollapseSettled).toHaveBeenCalledWith("chat-1", "user_input", 42, "unattended");
  });
});
