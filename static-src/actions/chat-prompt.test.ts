// Tests for sendPrompt: the early-ack lifecycle and the three-way 409 split.
//
// The server acks a prompt at ADMISSION ({accepted, message_id} the moment the
// user row is persisted and the slot is held), so the POST is short-lived and
// the action's whole job is mapping the ack and the failure classes:
//   ack            → "sent" (thinking stays true; SSE owns the turn from here)
//   plain 409      → "queued" (a steerable turn is in flight; submit.ts steers)
//   409 "starting" → "starting" (the holder cannot take a steer; thinking is
//                    retracted so the retry is a PROMPT, not a steer, and the
//                    outcome latches are restored — no turn started)
//   anything else  → null (rollback restores thinking + the outcome latches)

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../toast.js", () => ({
  info: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
  errorWithAction: vi.fn(),
  showToast: vi.fn(),
}));

vi.mock("../transport.js", () => ({
  send: vi.fn(),
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  newOpID: vi.fn(() => "op-test"),
}));

const { mockGet } = vi.hoisted(() => ({
  mockGet: vi.fn(() => ({ id: "c1", model: "m1" }) as Record<string, unknown> | undefined),
}));
// The TOTAL store mock, plus the one reader this file drives: `get` is what the
// rollback reads the latched outcome off, so each case sets its own return.
// Browser Mode links real ESM, so every name any module in this graph imports has
// to be present — which is what the shared helper is for, and what the
// hand-listed factory that used to live here got wrong the moment store.ts gained
// an export.
vi.mock("../store.js", async () => ({
  ...(await import("../__test-helpers__/store-mock.js")).storeMock,
  get: mockGet,
  setThinking: vi.fn(),
  setTurnFailed: vi.fn(),
  setTurnDone: vi.fn(),
  recordSteerQueued: vi.fn(),
  setModel: vi.fn(),
  setSupervisedMode: vi.fn(),
  removeChat: vi.fn(),
  reinsertSession: vi.fn(),
  indexOfSession: () => 0,
  // Present-but-inert so real-ESM linking succeeds: the tab projection widened
  // this graph and these names are imported somewhere in it. No case here calls
  // them.
  getSessions: vi.fn(() => []),
  tabStatusFor: vi.fn(() => ""),
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Present-but-inert so real-ESM linking succeeds. The tab projection widened
  // this graph: `apiGetTyped` is how tabs-sync reads `GET /api/tabs`, and other
  // modules reached through it import `apiGet`. Nothing here calls either.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { setThinking, setTurnFailed, setTurnDone, recordSteerQueued } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { sendPrompt } from "./chat.js";

const mockSend = vi.mocked(transportSend);

const args = { chatID: "c1", text: "hello", messageID: "m1", model: "model-1" };

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
  mockGet.mockReturnValue({ id: "c1", model: "m1" });
});

describe("sendPrompt — the ack is the whole POST", () => {
  it("returns 'sent' on the admission ack alone, with thinking left on for SSE to clear", async () => {
    // Ack-only fake: the server answers {accepted, message_id} at admission and
    // the turn runs on its own goroutine — nothing else arrives on this POST.
    mockSend.mockResolvedValue({
      ok: true,
      status: 200,
      body: { accepted: true, message_id: "m1" },
    });

    const result = await sendPrompt.dispatch(args);

    expect(result).toBe("sent");
    expect(mockSend).toHaveBeenCalledTimes(1);
    // The turn is running server-side; turn_ended (SSE) clears thinking, so the
    // success path must not touch it after the optimistic set.
    const calls = vi.mocked(setThinking).mock.calls;
    expect(calls).toContainEqual(["c1", true]);
    expect(calls).not.toContainEqual(["c1", false]);
  });

  it("dispatches at the standard API timeout — the POST no longer spans the turn", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch(args);

    const opts = mockSend.mock.calls[0]?.[1] as { timeoutMs?: number } | undefined;
    expect(opts?.timeoutMs).toBe(30_000);
  });

  it("sets thinking optimistically", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });
    await sendPrompt.dispatch({ ...args, messageID: "m2" });
    expect(setThinking).toHaveBeenCalledWith("c1", true);
  });
});

describe("sendPrompt — the three-way 409 split", () => {
  it("returns 'queued' on a plain 409 WITHOUT enqueuing (steering is submit.ts's job)", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "in-flight" });
    const result = await sendPrompt.dispatch(args);
    expect(result).toBe("queued");
    // The action stays a PURE send: on 409 it reports and does nothing else.
    // submit.ts owns what a busy chat means (it steers into the running turn),
    // and the steer chip is written only by the server's own frame — so an
    // action that recorded one here would put a chip on screen for a message
    // KAS may yet refuse.
    expect(recordSteerQueued).not.toHaveBeenCalled();
    // A steerable turn is in flight, so thinking correctly stays on.
    expect(vi.mocked(setThinking).mock.calls).not.toContainEqual(["c1", false]);
  });

  it("returns 'starting' on 409 reason:'starting' — a VALUE, never error-prose matching", async () => {
    // The error text is deliberately the same prose as the plain 409's: only
    // the lifted `reason` field distinguishes the two, which is the contract.
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "in-flight", reason: "starting" });

    const result = await sendPrompt.dispatch(args);

    expect(result).toBe("starting");
    expect(recordSteerQueued).not.toHaveBeenCalled();
  });

  it("retracts the optimistic thinking on 'starting', so the retry is a prompt and not a steer", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy", reason: "starting" });

    await sendPrompt.dispatch(args);

    expect(vi.mocked(setThinking).mock.calls).toContainEqual(["c1", true]);
    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
  });

  it("restores the outcome latches on 'starting' — no turn started", async () => {
    // The refused send must be state-neutral on the glance surfaces: a red
    // tab dot from the previous turn's failure stands until the holder's own
    // turn actually opens (setThinking(true) at that event clears it on every
    // device together). Leaving the optimistic clear in place erased the
    // failure mark with a send that did not happen.
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_failed: true });
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy", reason: "starting" });

    await sendPrompt.dispatch(args);

    expect(setTurnFailed).toHaveBeenCalledWith("c1");
    expect(setTurnDone).not.toHaveBeenCalled();
  });

  it("restores nothing on 'starting' when there was nothing latched", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1" });
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy", reason: "starting" });

    await sendPrompt.dispatch(args);

    expect(setTurnFailed).not.toHaveBeenCalled();
    expect(setTurnDone).not.toHaveBeenCalled();
  });

  it("leaves the latches cleared on 'queued' — the live turn's open is the truth", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_failed: true });
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy" });

    await sendPrompt.dispatch(args);

    expect(setTurnFailed).not.toHaveBeenCalled();
    expect(setTurnDone).not.toHaveBeenCalled();
  });

  it("treats a 409 with any OTHER reason as the plain queue signal", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 409, error: "busy", reason: "elsewhere" });
    expect(await sendPrompt.dispatch(args)).toBe("queued");
  });
});

// The optimistic write is `setThinking(chatID, true)`, and that call CLEARS the
// two outcome latches — starting a turn is what invalidates the previous turn's
// verdict. So a rollback that restored `thinking: false` alone erased a failure
// or a finished-while-away mark the reader had not seen yet, and the erasing
// event was an ordinary rejected prompt: a 400, a 413, a dead POST.
describe("sendPrompt — rollback restores what the optimistic write cleared", () => {
  it("fails (null) on a pre-ack network death — the POST is short now, so no echo rescue", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, code: "network", error: "unreachable" });

    expect(await sendPrompt.dispatch(args)).toBeNull();
    // Rollback cleared thinking: no turn started. submit.ts owns the
    // text-restore + id-reuse discipline on this result.
    expect(vi.mocked(setThinking).mock.calls).toContainEqual(["c1", false]);
  });

  it("fails (null) on a 5xx — the server spoke, and what it said was not an ack", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 500, error: "boom" });
    expect(await sendPrompt.dispatch(args)).toBeNull();
  });

  it("re-latches a failure the rejected send wiped", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_failed: true });
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "bad request" });

    await sendPrompt.dispatch({ ...args, messageID: "m3" });

    expect(setThinking).toHaveBeenCalledWith("c1", true);
    expect(setThinking).toHaveBeenLastCalledWith("c1", false);
    expect(setTurnFailed).toHaveBeenCalledWith("c1");
  });

  it("re-latches a finished-while-away mark the same way", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_done: true });
    mockSend.mockResolvedValue({ ok: false, status: 413, error: "too large" });

    await sendPrompt.dispatch({ ...args, messageID: "m4" });

    expect(setTurnDone).toHaveBeenCalledWith("c1");
  });

  it("re-latches nothing when there was nothing latched", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 400, error: "bad request" });

    await sendPrompt.dispatch({ ...args, messageID: "m5" });

    expect(setTurnFailed).not.toHaveBeenCalled();
    expect(setTurnDone).not.toHaveBeenCalled();
  });

  it("re-latches nothing on a send that succeeded", async () => {
    mockGet.mockReturnValue({ id: "c1", model: "m1", turn_failed: true });
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    await sendPrompt.dispatch({ ...args, messageID: "m6" });

    // The turn started, so the cleared verdict is correctly gone: restoring it
    // here would paint a failure over live work.
    expect(setTurnFailed).not.toHaveBeenCalled();
  });
});
