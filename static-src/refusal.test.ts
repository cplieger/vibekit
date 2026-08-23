// ---------------------------------------------------------------------------
// Tests for refusal.ts — the model-refusal callout (kiro-cli 2.13 contract).
// syncRefusal(wrap, m) mounts/removes the callout off m.refusal; the Rewind
// CTA routes through the injected handler (messages.ts owns the flow) and the
// switch CTA dispatches chat.switch_model with the recommended model. Store
// and the action are mocked; assertions are on the rendered DOM + dispatches.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

vi.mock("./store.js", () => ({ getActive: vi.fn() }));
vi.mock("./actions/chat.js", () => ({
  switchModel: { dispatch: vi.fn() },
}));

import { getActive } from "./store.js";
import { switchModel } from "./actions/chat.js";
import { syncRefusal, setRefusalRewindHandler } from "./refusal.js";
import type { Message, RefusalInfo, Session } from "./types.js";

const mockGetActive = vi.mocked(getActive);
const mockSwitch = vi.mocked(switchModel.dispatch);

function refusedMsg(refusal?: RefusalInfo): Message {
  return {
    id: "m1",
    role: "assistant",
    ts: 0,
    content: "I can't continue this conversation.",
    ...(refusal !== undefined && { refusal }),
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  setRefusalRewindHandler(() => undefined);
});

describe("syncRefusal", () => {
  it("mounts the callout with title, category chip, and rewind CTA", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, refusedMsg({ category: "safety" }));
    const callout = wrap.querySelector(".refusal-callout");
    expect(callout).not.toBeNull();
    expect(callout?.textContent).toContain("The model declined to continue");
    expect(callout?.querySelector(".refusal-chip")?.textContent).toBe("safety");
    const buttons = callout?.querySelectorAll(".refusal-btn") ?? [];
    expect([...buttons].map((b) => b.textContent)).toEqual(["Rewind"]);
  });

  it("omits the chip when no category and adds the switch CTA when a model is recommended", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, refusedMsg({ recommended_model: "model-x" }));
    const callout = wrap.querySelector(".refusal-callout");
    expect(callout?.querySelector(".refusal-chip")).toBeNull();
    const labels = [...(callout?.querySelectorAll(".refusal-btn") ?? [])].map((b) => b.textContent);
    expect(labels).toEqual(["Rewind", "Switch to model-x"]);
  });

  it("does nothing for a non-refusal message and removes a stale callout", () => {
    const wrap = document.createElement("div");
    syncRefusal(wrap, refusedMsg({ category: "safety" }));
    expect(wrap.querySelector(".refusal-callout")).not.toBeNull();
    syncRefusal(wrap, refusedMsg());
    expect(wrap.querySelector(".refusal-callout")).toBeNull();
  });

  it("is idempotent — repeated syncs keep one callout", () => {
    const wrap = document.createElement("div");
    const m = refusedMsg({ category: "safety" });
    syncRefusal(wrap, m);
    syncRefusal(wrap, m);
    expect(wrap.querySelectorAll(".refusal-callout").length).toBe(1);
  });

  it("rewind CTA routes through the injected handler with the message", () => {
    const wrap = document.createElement("div");
    const m = refusedMsg({});
    const onRewind = vi.fn();
    setRefusalRewindHandler(onRewind);
    syncRefusal(wrap, m);
    (wrap.querySelector(".refusal-btn") as HTMLButtonElement).click();
    expect(onRewind).toHaveBeenCalledWith(m);
  });

  it("switch CTA dispatches chat.switch_model for the active chat", () => {
    mockGetActive.mockReturnValue({ id: "c1" } as Session);
    mockSwitch.mockResolvedValue(true);
    const wrap = document.createElement("div");
    syncRefusal(wrap, refusedMsg({ recommended_model: "model-x" }));
    const btn = [...wrap.querySelectorAll<HTMLButtonElement>(".refusal-btn")].find((b) =>
      b.textContent?.startsWith("Switch"),
    );
    btn?.click();
    expect(mockSwitch).toHaveBeenCalledWith({ chatID: "c1", model: "model-x" });
  });

  it("switch CTA is a no-op without an active session", () => {
    mockGetActive.mockReturnValue(undefined);
    const wrap = document.createElement("div");
    syncRefusal(wrap, refusedMsg({ recommended_model: "model-x" }));
    const btn = [...wrap.querySelectorAll<HTMLButtonElement>(".refusal-btn")].find((b) =>
      b.textContent?.startsWith("Switch"),
    );
    btn?.click();
    expect(mockSwitch).not.toHaveBeenCalled();
  });
});
