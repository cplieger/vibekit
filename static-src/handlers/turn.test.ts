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

import { vi, describe, it, expect, beforeEach } from "vitest";
import {
  setSessions,
  setActive,
  get,
  recordSteerQueued,
  steerCount,
  setAgentStatus,
  tabStatusFor,
} from "../store.js";
import type { Session } from "../types.js";
import type * as TurnRail from "../turn-rail.js";

type TurnRailModule = typeof TurnRail;

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
const mockNotifyIfHidden = vi.fn();
vi.mock("../notify.js", () => ({
  notifyIfHidden: mockNotifyIfHidden,
  setBadge: vi.fn(),
  isAgentFinishedEnabled: () => false,
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
  setSessions([]);
  // A messages container with one assistant message — the turn_ended handler
  // appends the turn-summary as a sibling of the last assistant message.
  document.body.innerHTML = '<div id="messages"><div class="message assistant"></div></div>';
});

describe("ERROR_ROUTES", () => {
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
    ["auth_token_unavailable", { surface: "toast", action: { kind: "sign-in", label: "Sign in" } }],
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

  it("clears them for a background (non-active) chat too", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one", origin: "user" });

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(steerCount("chat-1")).toBe(0);
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

  it("latches nothing for a CANCELLED turn, which finished nothing", () => {
    // The same line the "Agent finished" notification already draws: a cancelled
    // turn is not a finished one, and a green "turn finished" dot would be a claim
    // about work the user stopped.
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "cancelled", outcome: "cancelled" });
    expect(tabStatusFor(get("chat-1"))).toBe("idle");
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

  it("latches neither for an outcome nothing could read", () => {
    // `unknown` is a turn whose end vibekit had to infer. Claiming either a green
    // finish or a red failure would be inventing a verdict the wire did not give.
    setSessions([makeSession("chat-1", { thinking: true }), makeSession("chat-2")]);
    setActive("chat-2");

    fireSSE("turn_ended", "chat-1", { stop_reason: "who_knows", outcome: "unknown" });
    expect(tabStatusFor(get("chat-1"))).toBe("idle");
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
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "slow down", undefined);
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
    });

    expect(mockReportFailure).toHaveBeenCalledWith(
      "chat-1",
      // kiro-cli's own reason travels through: it names which leg of the login
      // chain is dead, and no wording invented client-side is more specific.
      "kiro-cli: refresh token expired",
      expect.objectContaining({ label: "Sign in" }),
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
      fireSSE("error", "chat-1", { code, message: "boom" });
      expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "boom", undefined);
      expect(mockSetAgentDown).not.toHaveBeenCalled();
    },
  );

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
    fireSSE("error", "chat-1", { code: "prompt_failed", message: "at capacity" });
    expect(mockReportFailure).toHaveBeenCalledWith("chat-1", "at capacity", undefined);

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
