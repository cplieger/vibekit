// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/turn.ts: the ERROR_ROUTES classification table plus the
// turn_ended and error SSE handlers.
//
// These drive the REAL handlers (via the bus capture) and the REAL store, and
// assert observable outcomes: the rendered turn-summary text, the cleared
// thinking flag, the drained queued prompt, and the error routing. Sibling
// subsystems (notify, banner-stack, send-state, chat-commands, git) stay
// mocked because a call into them is a command at the handler's boundary.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { setSessions, setActive, get, recordSteerQueued, steerCount } from "../store.js";
import type { Session } from "../types.js";

// scroll.ts touches DOM elements at import; use the shared mock.
vi.mock(
  "../scroll.js",
  async () => (await import("../__test-helpers__/scroll-mock.js")).scrollMock,
);
vi.mock("../decision-dock.js", () => ({ pushDecision: vi.fn() }));

vi.mock("../attachments.js", () => ({ addAttachment: vi.fn() }));

const mockSetLastError = vi.fn();
const mockClearLastError = vi.fn();
vi.mock("../send-state.js", () => ({
  setLastError: mockSetLastError,
  clearLastError: mockClearLastError,
  setSSEStatus: vi.fn(),
}));

vi.mock("../notify.js", () => ({
  notifyIfHidden: vi.fn(),
  setBadge: vi.fn(),
  isAgentFinishedEnabled: () => false,
  isPermissionNeededEnabled: () => false,
  NOTIFY_TITLE: "vibekit",
}));

vi.mock("../git.js", () => ({ refreshGitBadge: vi.fn() }));

const mockShowBanner = vi.fn();
vi.mock("../banner-stack.js", () => ({ showBanner: mockShowBanner, onTurnEnded: vi.fn() }));

// Capture SSE handlers via shared helper.
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so turn.ts registers its handlers against the bus mock.
const { ERROR_ROUTES } = await import("./turn.js");

function makeSession(id: string, over: Partial<Session> = {}): Session {
  return {
    id,
    name: "seeded",
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
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
  const expectedRoutes: [string, { surface: string; level: string; dismissible: boolean }][] = [
    ["agent_not_found", { surface: "banner", level: "error", dismissible: true }],
    ["agent_config_error", { surface: "banner", level: "error", dismissible: false }],
    ["rate_limit", { surface: "banner", level: "warning", dismissible: true }],
    ["compaction_failed", { surface: "banner", level: "error", dismissible: true }],
    ["switch_failed", { surface: "send-error", level: "error", dismissible: false }],
    ["bridge_start_failed", { surface: "send-error", level: "error", dismissible: false }],
    ["prompt_failed", { surface: "send-error", level: "error", dismissible: false }],
    // The chat runs, just not in the requested mode, and one click on the mode
    // pill fixes it — so a dismissible warning banner rather than a send error.
    ["mode_not_applied", { surface: "banner", level: "warning", dismissible: true }],
    // A pick refused before it reached the wire: the send that carried it is what
    // failed, same surface as switch_failed.
    ["model_not_served", { surface: "send-error", level: "error", dismissible: false }],
    // Empty-turn recovery could not respawn or resend. It was routed NOWHERE
    // before, so it reached setLastError with no level and no banner on the one
    // error whose meaning is "the automatic repair failed".
    ["recovery_failed", { surface: "send-error", level: "error", dismissible: false }],
  ];

  it.each(expectedRoutes)(
    "routes %s to the expected surface/level/dismissible",
    (code, expected) => {
      expect(ERROR_ROUTES[code as keyof typeof ERROR_ROUTES]).toEqual(expected);
    },
  );

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
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });
    expect(steerCount("chat-1")).toBe(1);

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(steerCount("chat-1")).toBe(0);
  });

  it("clears them for a background (non-active) chat too", () => {
    setSessions([makeSession("chat-1"), makeSession("chat-2")]);
    setActive("chat-2");
    recordSteerQueued("chat-1", { id: "steer-1", text: "one" });

    fireSSE("turn_ended", "chat-1", { stop_reason: "end_turn" });
    expect(steerCount("chat-1")).toBe(0);
  });
});

describe("error handler", () => {
  it("clears thinking and routes a banner-class error to showBanner", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "rate_limit", message: "slow down" });
    expect(get("chat-1")?.thinking).toBe(false);
    expect(mockShowBanner).toHaveBeenCalledWith(
      "chat-1",
      "rate_limit",
      "slow down",
      "warning",
      true,
    );
    expect(mockSetLastError).not.toHaveBeenCalled();
  });

  it("routes a send-error-class error to setLastError", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "prompt_failed", message: "boom" });
    expect(get("chat-1")?.thinking).toBe(false);
    expect(mockSetLastError).toHaveBeenCalledWith("prompt_failed: boom");
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("falls through unknown codes to the send-button blocker", () => {
    setSessions([makeSession("chat-1", { thinking: true })]);
    setActive("chat-1");
    fireSSE("error", "chat-1", { code: "mystery_code", message: "huh" });
    expect(get("chat-1")?.thinking).toBe(false);
    expect(mockSetLastError).toHaveBeenCalledWith("mystery_code: huh");
  });
});
