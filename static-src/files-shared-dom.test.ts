// @vitest-environment happy-dom
import { describe, it, expect, vi } from "vitest";
import { errorRow } from "./files-shared.js";

describe("errorRow", () => {
  it("renders a plain error row without a button when onRetry is omitted", () => {
    const row = errorRow("Something went wrong");
    expect(row.className).toBe("fb-row");
    expect(row.querySelector("span.fb-meta")?.textContent).toBe("Something went wrong");
    expect(row.querySelector("button")).toBeNull();
  });

  it("appends a Retry button that calls onRetry when provided", () => {
    const spy = vi.fn();
    const row = errorRow("Load failed", spy);
    const btn = row.querySelector("button");
    expect(btn).not.toBeNull();
    expect(btn!.textContent).toBe("Retry");
    expect(btn!.className).toBe("btn-small");
    btn!.click();
    expect(spy).toHaveBeenCalledOnce();
  });
});
