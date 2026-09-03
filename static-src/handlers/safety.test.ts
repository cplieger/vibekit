// ---------------------------------------------------------------------------
// Tests for handlers/safety.ts: the defensive Infrastructure-Safety status
// handler raises a transient toast while the gate is active, folds in the
// violated/active properties, and raises nothing when the gate goes idle. toast
// is mocked so we assert the showToast call shape.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import { fireSSE, createBusMock } from "./__test-helpers__/sse-capture.js";

const mockShowToast = vi.fn();
vi.mock("../toast.js", async () => ({
  ...(await import("../__test-helpers__/toast-mock.js")).toastMock(),
  showToast: mockShowToast,
}));
vi.mock("../bus.js", () => createBusMock());

// Import after mocks so the handler registers against the bus mock.
await import("./safety.js");

beforeEach(() => {
  vi.clearAllMocks();
});

describe("safety_status handler", () => {
  it("raises an error-level toast for a blocked gate, including the violated property", () => {
    fireSSE("safety_status", "c1", {
      status: "blocked",
      detail: "fs_write blocked",
      tool_id: "fs_write",
      blocked_properties: ["no public S3 buckets"],
    });
    expect(mockShowToast).toHaveBeenCalledTimes(1);
    const call = mockShowToast.mock.calls[0] ?? [];
    expect(String(call[0])).toContain("no public S3 buckets"); // message carries the constraint
    expect(call[1]).toBe("error"); // level
  });

  // The toast vocabulary has no warning level, so a gate that has stopped nothing
  // takes the info default rather than being promoted to a failure.
  it("raises an info toast for evaluating (monitor-mode would-violate)", () => {
    fireSSE("safety_status", "c1", {
      status: "evaluating",
      detail: "Would violate: no public buckets",
    });
    const call = mockShowToast.mock.calls[0] ?? [];
    expect(call[1]).toBe("info");
    expect(String(call[0])).toContain("Would violate");
  });

  it("raises nothing for idle, which is the all-clear", () => {
    fireSSE("safety_status", "c1", { status: "idle" });
    expect(mockShowToast).not.toHaveBeenCalled();
  });

  it("folds the last formalized properties into a status that carries none of its own", () => {
    fireSSE("safety_properties", "c1", {
      properties: [{ description: "encrypt EBS volumes", enabled: true }],
      reason: "formalized",
    });
    fireSSE("safety_status", "c1", { status: "evaluating" });
    const call = mockShowToast.mock.calls[0] ?? [];
    expect(String(call[0])).toContain("encrypt EBS volumes");
  });

  it("ignores an event with an empty chat id", () => {
    fireSSE("safety_status", "", { status: "blocked" });
    expect(mockShowToast).not.toHaveBeenCalled();
  });
});
