// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { withAsyncFeedback } from "./async-button.js";

function makeButton(html = "Click me"): HTMLButtonElement {
  const btn = document.createElement("button");
  btn.type = "button";
  btn.innerHTML = html;
  document.body.replaceChildren(btn);
  return btn;
}

describe("withAsyncFeedback", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("shows pending state immediately and disables the button", async () => {
    const btn = makeButton();
    let resolveFn: (() => void) | undefined;
    const work = new Promise<void>((res) => {
      resolveFn = res;
    });
    const promise = withAsyncFeedback(btn, () => work);

    // Synchronously after the call, button should be in pending state.
    expect(btn.dataset["asyncStatus"]).toBe("pending");
    expect(btn.disabled).toBe(true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
    expect(btn.querySelector(".btn-async-spinner")).not.toBeNull();

    resolveFn!();
    await promise;
  });

  it("transitions to success when fn resolves; reverts after resetMs", async () => {
    const btn = makeButton("<span>Original</span>");
    const promise = withAsyncFeedback(btn, () => Promise.resolve());
    await promise;

    expect(btn.dataset["asyncStatus"]).toBe("success");
    expect(btn.querySelector(".btn-async-glyph")).not.toBeNull();
    // Should still be a checkmark (polyline path), not an x.
    expect(btn.innerHTML).toContain("polyline");

    vi.advanceTimersByTime(1200);
    expect(btn.dataset["asyncStatus"]).toBeUndefined();
    expect(btn.disabled).toBe(false);
    expect(btn.innerHTML).toBe("<span>Original</span>");
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });

  it("transitions to error when fn throws; reverts after resetMs", async () => {
    const btn = makeButton("Original");
    await withAsyncFeedback(btn, () => Promise.reject(new Error("fail")));

    expect(btn.dataset["asyncStatus"]).toBe("error");
    // Error glyph is an X (path with M…L).
    expect(btn.innerHTML).toContain("M18 6L6 18");

    vi.advanceTimersByTime(1200);
    expect(btn.dataset["asyncStatus"]).toBeUndefined();
    expect(btn.disabled).toBe(false);
    expect(btn.innerHTML).toBe("Original");
  });

  it("ignores re-entrant clicks while pending", async () => {
    const btn = makeButton();
    const fn = vi.fn(() => new Promise<void>((res) => setTimeout(res, 100)));

    const first = withAsyncFeedback(btn, fn);
    const second = withAsyncFeedback(btn, fn);
    const third = withAsyncFeedback(btn, fn);

    // Only the first invocation should have run fn.
    expect(fn).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(100);
    await Promise.all([first, second, third]);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("preserves original disabled state after reset", async () => {
    const btn = makeButton();
    btn.disabled = true; // edge case: button started disabled
    await withAsyncFeedback(btn, () => Promise.resolve());
    vi.advanceTimersByTime(1200);
    expect(btn.disabled).toBe(true); // restored
  });

  it("respects custom resetMs", async () => {
    const btn = makeButton();
    await withAsyncFeedback(btn, () => Promise.resolve(), { resetMs: 50 });
    expect(btn.dataset["asyncStatus"]).toBe("success");
    vi.advanceTimersByTime(49);
    expect(btn.dataset["asyncStatus"]).toBe("success");
    vi.advanceTimersByTime(2);
    expect(btn.dataset["asyncStatus"]).toBeUndefined();
  });

  it("with keepLabel renders spinner alongside the original content", async () => {
    const btn = makeButton("Clone");
    let resolveFn: (() => void) | undefined;
    const work = new Promise<void>((res) => {
      resolveFn = res;
    });
    const promise = withAsyncFeedback(btn, () => work, { keepLabel: true });

    expect(btn.querySelector(".btn-async-spinner")).not.toBeNull();
    expect(btn.textContent).toContain("Clone");

    resolveFn!();
    await promise;
  });

  it("when button is removed from DOM mid-flight, no error and no glyph", async () => {
    const btn = makeButton();
    const work = Promise.resolve();
    btn.remove();
    await expect(withAsyncFeedback(btn, () => work)).resolves.toBeUndefined();
    // Detached button gets cleaned up: aria-busy removed, asyncStatus cleared.
    expect(btn.dataset["asyncStatus"]).toBeUndefined();
    expect(btn.getAttribute("aria-busy")).toBeNull();
  });
});
