// The three interactive asks share one answer path with a THREE-valued outcome.
// What these pin is that "somebody else answered first" is not a failure: it
// must not reach the error notification, because decision-dock.ts already
// explains it with attribution and a second toast reads as a retry prompt.
//
// The server half is internal/command/validate.go's errAlreadyAnswered, served
// as 409 {"error":"already_answered"} once hub.TakePendingPerm loses the race.

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({
  send: vi.fn(),
}));

vi.mock("../store.js", () => ({
  // Present-but-undefined so real-ESM linking succeeds: another module in this
  // graph imports the name, and Browser Mode links for real rather than reading
  // properties off a namespace object. `undefined` is what the node runner gave
  // these, so no path under test changes behavior.
  activeSession: undefined,
  getActive: undefined,
  getActiveId: undefined,
  setCurrentMode: undefined,
  setTurnDone: undefined,
  setTurnFailed: undefined,
  get: () => ({ id: "c1", model: "m1" }),
  setThinking: vi.fn(),
  recordSteerQueued: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));

import { send as transportSend } from "../transport.js";
import { error as toastError } from "../toast.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { respondPermission, respondElicitation, respondUserInput } from "./chat.js";

const mockSend = vi.mocked(transportSend);
const mockToastError = vi.mocked(toastError);

/** One dispatch per ask kind, so a rule proved for permissions is proved for
 *  all three rather than assumed to generalise. */
const asks = [
  {
    name: "permission",
    dispatch: () => respondPermission.dispatch({ chatID: "c1", requestID: 7, optionID: "allow" }),
  },
  {
    name: "elicitation",
    dispatch: () => respondElicitation.dispatch({ chatID: "c1", requestID: 7, action: "decline" }),
  },
  {
    name: "user input",
    dispatch: () => respondUserInput.dispatch({ chatID: "c1", requestID: 7, action: "dismissed" }),
  },
] as const;

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockToastError.mockReset();
});

describe("answering an ask: the three outcomes", () => {
  for (const ask of asks) {
    it(`${ask.name}: a 409 already_answered is 'superseded' and raises NO error toast`, async () => {
      mockSend.mockResolvedValue({ ok: false, status: 409, error: "already_answered" });
      const result = await ask.dispatch();
      expect(result).toBe("superseded");
      // The whole point: the dock owns this explanation, so this layer is silent.
      expect(mockToastError).not.toHaveBeenCalled();
    });

    it(`${ask.name}: a real failure yields null and DOES raise an error toast`, async () => {
      mockSend.mockResolvedValue({ ok: false, status: 500, error: "bridge died" });
      // The framework's contract: a failed dispatch resolves null (it does not
      // reject) and fires the error notification. So "no toast" is the only
      // observable difference between a superseded answer and a broken one.
      await expect(ask.dispatch()).resolves.toBeNull();
      expect(mockToastError).toHaveBeenCalled();
    });

    it(`${ask.name}: a landed answer is 'answered'`, async () => {
      mockSend.mockResolvedValue({ ok: true, status: 200 });
      await expect(ask.dispatch()).resolves.toBe("answered");
      expect(mockToastError).not.toHaveBeenCalled();
    });
  }

  // A 409 that is NOT the already-answered sentinel is a different condition
  // (the idempotency middleware answers 409 "request already in progress" for a
  // truly concurrent duplicate), so it must stay an error rather than being
  // swallowed by a status-only match.
  it("a 409 with a different body is still a failure", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "request already in progress" });
    await expect(
      respondPermission.dispatch({ chatID: "c1", requestID: 8, optionID: "allow" }),
    ).resolves.toBeNull();
    expect(mockToastError).toHaveBeenCalled();
  });

  it("does not report send-state: that surface belongs to the prompt button", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await respondPermission.dispatch({ chatID: "c1", requestID: 9, optionID: "allow" });
    const opts = mockSend.mock.calls[0]?.[1];
    expect(opts?.reportSendState).toBe(false);
  });
});
