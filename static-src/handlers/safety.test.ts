// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for handlers/safety.ts: the defensive Infrastructure-Safety status
// handler surfaces a transient banner while the gate is active, folds in the
// violated/active properties, and clears on idle / turn end. banner-stack is
// mocked so we assert the showBanner / clearBannerCodes call shapes.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

const mockShowBanner = vi.fn();
const mockClearBannerCodes = vi.fn();
vi.mock("../banner-stack.js", () => ({
  showBanner: (...args: unknown[]) => mockShowBanner(...args),
  clearBannerCodes: (...args: unknown[]) => mockClearBannerCodes(...args),
}));
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so the handler registers against the bus mock.
await import("./safety.js");

beforeEach(() => {
  vi.clearAllMocks();
});

describe("safety_status handler", () => {
  it("shows an error-level banner for a blocked gate, including the violated property", () => {
    fireSSE("safety_status", "c1", {
      status: "blocked",
      detail: "fs_write blocked",
      tool_id: "fs_write",
      blocked_properties: ["no public S3 buckets"],
    });
    expect(mockShowBanner).toHaveBeenCalledTimes(1);
    const call = mockShowBanner.mock.calls[0] ?? [];
    expect(call[0]).toBe("c1"); // chatID
    expect(call[1]).toBe("safety_status"); // code
    expect(String(call[2])).toContain("no public S3 buckets"); // message carries the constraint
    expect(call[3]).toBe("error"); // level
    expect(call[4]).toBe(true); // dismissible
  });

  it("shows a warning banner for evaluating (monitor-mode would-violate)", () => {
    fireSSE("safety_status", "c1", {
      status: "evaluating",
      detail: "Would violate: no public buckets",
    });
    const call = mockShowBanner.mock.calls[0] ?? [];
    expect(call[3]).toBe("warning");
    expect(String(call[2])).toContain("Would violate");
  });

  it("clears the banner (not shows) on idle", () => {
    fireSSE("safety_status", "c1", { status: "idle" });
    expect(mockShowBanner).not.toHaveBeenCalled();
    expect(mockClearBannerCodes).toHaveBeenCalledWith("c1", ["safety_status"]);
  });

  it("folds the last formalized properties into a status banner that carries none of its own", () => {
    fireSSE("safety_properties", "c1", {
      properties: [{ description: "encrypt EBS volumes", enabled: true }],
      reason: "formalized",
    });
    fireSSE("safety_status", "c1", { status: "evaluating" });
    const call = mockShowBanner.mock.calls[0] ?? [];
    expect(String(call[2])).toContain("encrypt EBS volumes");
  });

  it("ignores an event with an empty chat id", () => {
    fireSSE("safety_status", "", { status: "blocked" });
    expect(mockShowBanner).not.toHaveBeenCalled();
  });

  it("clears the transient banner on turn end", () => {
    fireSSE("turn_ended", "c1", { stop_reason: "end_turn" });
    expect(mockClearBannerCodes).toHaveBeenCalledWith("c1", ["safety_status"]);
  });
});
