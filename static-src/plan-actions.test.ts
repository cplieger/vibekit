// Tests for the plan-handoff core: the size cap, the Plan→executing mode
// switch, and the defer-delete-until-sent behaviour of runPlan.
import { describe, it, expect, vi, beforeEach } from "vitest";

// Plain module mocks. runPlan only reads `.current_mode_id` off the session
// and only calls `.dispatch` on the two actions, so shallow stubs suffice.
vi.mock("./store.js", () => ({ get: vi.fn() }));
vi.mock("./actions/chat.js", () => ({ setMode: { dispatch: vi.fn() } }));
vi.mock("./actions/messages.js", () => ({ runPlan: { dispatch: vi.fn() } }));
vi.mock("./banner-stack.js", () => ({ showBanner: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiPutOrError: vi.fn(), apiDelete: vi.fn() }));

import { runPlan } from "./plan-actions.js";
import { get } from "./store.js";
import { setMode } from "./actions/chat.js";
import { runPlan as runPlanAction } from "./actions/messages.js";
import { showBanner } from "./banner-stack.js";
import { apiDelete } from "./api-client.js";
import type { Session } from "./types.js";

const getMock = vi.mocked(get);
const setModeDispatch = vi.mocked(setMode.dispatch);
const runPlanDispatch = vi.mocked(runPlanAction.dispatch);
const showBannerMock = vi.mocked(showBanner);
const apiDeleteMock = vi.mocked(apiDelete);

function sessionInMode(mode: string): Session {
  return { current_mode_id: mode } as unknown as Session;
}

beforeEach(() => {
  // mockReset wipes implementations before each test; re-seed the defaults.
  getMock.mockReturnValue(sessionInMode("vibe"));
  runPlanDispatch.mockResolvedValue("sent");
});

describe("runPlan size cap", () => {
  it("rejects a plan over the 256 KB draft cap with a banner and no send", async () => {
    const oversize = "x".repeat(256 * 1024 + 1);
    const result = await runPlan("chat1", oversize);
    expect(result).toBe("too_large");
    expect(showBannerMock).toHaveBeenCalledWith(
      "chat1",
      "plan_run_too_large",
      expect.stringContaining("256 KB"),
      "error",
      true,
    );
    expect(runPlanDispatch).not.toHaveBeenCalled();
  });

  it("sends a plan exactly at the cap", async () => {
    const atCap = "x".repeat(256 * 1024);
    const result = await runPlan("chat1", atCap);
    expect(result).toBe("sent");
    expect(runPlanDispatch).toHaveBeenCalledTimes(1);
  });
});

describe("runPlan mode switch (Plan → executing)", () => {
  it("switches out of Plan mode before sending, then sends", async () => {
    getMock.mockReturnValue(sessionInMode("plan"));
    const result = await runPlan("chat1", "do the work");
    expect(setModeDispatch).toHaveBeenCalledWith({ chatID: "chat1", modeID: "vibe" });
    expect(runPlanDispatch).toHaveBeenCalledWith({ chatID: "chat1", content: "do the work" });
    // set_mode must be dispatched before the prompt so the turn runs in the
    // executing mode.
    expect(setModeDispatch.mock.invocationCallOrder[0]!).toBeLessThan(
      runPlanDispatch.mock.invocationCallOrder[0]!,
    );
    expect(result).toBe("sent");
  });

  it("does not switch mode when the chat is already in an executing mode", async () => {
    getMock.mockReturnValue(sessionInMode("vibe"));
    await runPlan("chat1", "do the work");
    expect(setModeDispatch).not.toHaveBeenCalled();
    expect(runPlanDispatch).toHaveBeenCalledTimes(1);
  });
});

describe("runPlan draft deletion (defer until sent)", () => {
  it("deletes the draft only once the send is confirmed sent", async () => {
    runPlanDispatch.mockResolvedValue("sent");
    const result = await runPlan("chat1", "do the work");
    expect(result).toBe("sent");
    expect(apiDeleteMock).toHaveBeenCalledTimes(1);
  });

  it("keeps the draft when the send is queued (durable copy)", async () => {
    runPlanDispatch.mockResolvedValue("queued");
    const result = await runPlan("chat1", "do the work");
    expect(result).toBe("queued");
    expect(apiDeleteMock).not.toHaveBeenCalled();
  });

  it("keeps the draft and reports failed when the send is rejected", async () => {
    runPlanDispatch.mockResolvedValue(null);
    const result = await runPlan("chat1", "do the work");
    expect(result).toBe("failed");
    expect(apiDeleteMock).not.toHaveBeenCalled();
  });
});

describe("runPlan guards", () => {
  it("returns failed for blank content without dispatching", async () => {
    const result = await runPlan("chat1", "   ");
    expect(result).toBe("failed");
    expect(runPlanDispatch).not.toHaveBeenCalled();
    expect(setModeDispatch).not.toHaveBeenCalled();
  });

  it("returns failed for an empty chat id", async () => {
    const result = await runPlan("", "do the work");
    expect(result).toBe("failed");
    expect(runPlanDispatch).not.toHaveBeenCalled();
  });
});
