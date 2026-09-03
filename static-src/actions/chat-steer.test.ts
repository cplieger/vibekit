// Tests for steerChat: the POST-body chip confirmation and the reason lift.
//
// The steer POST's 200 carries the authoritative `steer_id`, and the custom
// runner adopts it onto the optimistic dock row (`recordSteerQueued`) so the
// chip confirms on the POST's own round trip — the SSE `steer_queued` used to
// be the ONLY confirmation, so a dropped stream left every sent steer stuck at
// "Sending". A refusal lifts the envelope's `reason` into the ActionError's
// code (submit.ts converts `no_turn` back into a prompt), rolls the row back,
// and raises no toast (`error: false` — submit.ts owns the failure surface).

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

// The TOTAL store mock. It already carries the steer projection this action
// drives — steerIDFor (the real derived-id shape), recordSteerSent,
// recordSteerQueued, forgetSteer — so no overrides are needed.
vi.mock("../store.js", async () => ({
  ...(await import("../__test-helpers__/store-mock.js")).storeMock,
}));

vi.mock("../api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
  // Present-but-inert so real-ESM linking succeeds. Nothing here calls either.
  apiGet: vi.fn(),
  apiGetTyped: vi.fn(),
}));

import { send as transportSend } from "../transport.js";
import { error as toastError } from "../toast.js";
import { recordSteerSent, recordSteerQueued, forgetSteer } from "../store.js";
import { resetActionFramework } from "./__test-helpers__/action-test-setup.js";
import { IDEMPOTENCY_COMMAND_FIELD } from "./index.js";
import { steerChat } from "./chat.js";

const mockSend = vi.mocked(transportSend);

const args = { chatID: "c1", text: "also check the logs", messageID: "m1" };

beforeEach(() => {
  resetActionFramework();
  mockSend.mockReset();
});

describe("steerChat — the POST body confirms the chip", () => {
  it("adopts the reply's steer_id onto the dock row", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { steer_id: "kas-7" } });

    await steerChat.dispatch(args);

    expect(recordSteerQueued).toHaveBeenCalledWith("c1", {
      id: "kas-7",
      text: "also check the logs",
      // `user` is a FACT on this path rather than a guess: this is the reply to
      // this device's own POST, and the server has just recorded the same id in
      // the ledger its own `steer_queued` frame is stamped from.
      origin: "user",
    });
  });

  it("leaves the chip pending when the reply carries no body — the SSE frame covers an older server", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    await steerChat.dispatch(args);

    expect(recordSteerQueued).not.toHaveBeenCalled();
  });

  it("ignores a non-string steer_id rather than adopting garbage", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200, body: { steer_id: 7 } });

    await steerChat.dispatch(args);

    expect(recordSteerQueued).not.toHaveBeenCalled();
  });
});

describe("steerChat — a refusal", () => {
  it("lifts the envelope reason into the outcome's code, keeping the server's words", async () => {
    mockSend.mockResolvedValue({
      ok: false,
      status: 409,
      error: "no turn is running",
      reason: "no_turn",
    });

    const outcome = await steerChat.dispatch(args).outcome;

    expect(outcome).toMatchObject({
      status: "error",
      error: { code: "no_turn", message: "no turn is running" },
    });
  });

  it("falls back to the transport code when the envelope carried no reason", async () => {
    mockSend.mockResolvedValue({ ok: false, status: 0, error: "unreachable", code: "network" });

    const outcome = await steerChat.dispatch(args).outcome;

    expect(outcome).toMatchObject({ status: "error", error: { code: "network" } });
  });

  it("un-draws the optimistic row and raises no toast — submit.ts owns the surface", async () => {
    mockSend.mockResolvedValue({
      ok: false,
      status: 409,
      error: "no turn is running",
      reason: "no_turn",
    });

    await steerChat.dispatch(args).outcome;

    expect(forgetSteer).toHaveBeenCalledWith("c1", "steer-m1");
    expect(toastError).not.toHaveBeenCalled();
  });
});

describe("steerChat — the command on the wire", () => {
  it("draws the optimistic row at dispatch, before the POST answers", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    await steerChat.dispatch(args);

    expect(recordSteerSent).toHaveBeenCalledWith("c1", "m1", "also check the logs");
  });

  it("sends a typed steer command carrying the idempotency key, with transport's own failure notice off", async () => {
    mockSend.mockResolvedValue({ ok: true, status: 200 });

    await steerChat.dispatch(args);

    const [cmd, opts] = mockSend.mock.calls[0] ?? [];
    expect(cmd).toMatchObject({
      type: "steer",
      chat_id: "c1",
      payload: { text: "also check the logs", message_id: "m1" },
    });
    // The key rides the command object under the framework's field name;
    // transport.send lifts it into the Idempotency-Key header.
    const key = (cmd as unknown as Record<string, unknown>)[IDEMPOTENCY_COMMAND_FIELD];
    expect(typeof key).toBe("string");
    expect(key).not.toBe("");
    expect(opts).toMatchObject({ reportSendState: false });
  });
});
