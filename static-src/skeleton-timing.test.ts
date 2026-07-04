// @vitest-environment happy-dom
//
// Tests for the chat-load skeleton anti-flicker (deferSkeleton). Two layers:
//   1. The helper's contract in isolation (fake timers + spies).
//   2. A faithful reproduction of chat.ts activateChatView's exact wiring
//      against a real container and the real chatSkeleton(), driven by a load
//      promise that resolves before/after the 150ms show-delay.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { deferSkeleton } from "./skeleton-timing.js";
import { chatSkeleton } from "./skeleton.js";

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("deferSkeleton contract", () => {
  it("never calls show when cancelled before the delay elapses", () => {
    const teardown = vi.fn();
    const show = vi.fn(() => teardown);
    const cancel = deferSkeleton(show);

    cancel();
    vi.advanceTimersByTime(1000);

    expect(show).not.toHaveBeenCalled();
    expect(teardown).not.toHaveBeenCalled();
  });

  it("calls show once after the delay and teardown on a later cancel", () => {
    const teardown = vi.fn();
    const show = vi.fn(() => teardown);
    const cancel = deferSkeleton(show);

    vi.advanceTimersByTime(150);
    expect(show).toHaveBeenCalledTimes(1);
    expect(teardown).not.toHaveBeenCalled();

    cancel();
    expect(teardown).toHaveBeenCalledTimes(1);
  });

  it("defaults to a 150ms delay (149ms: not yet, 150ms: shown)", () => {
    const show = vi.fn(() => vi.fn());
    deferSkeleton(show);

    vi.advanceTimersByTime(149);
    expect(show).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("honours a custom delay", () => {
    const show = vi.fn(() => vi.fn());
    deferSkeleton(show, 500);

    vi.advanceTimersByTime(499);
    expect(show).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(show).toHaveBeenCalledTimes(1);
  });

  it("is idempotent: repeated cancel runs teardown at most once", () => {
    const teardown = vi.fn();
    const show = vi.fn(() => teardown);
    const cancel = deferSkeleton(show);

    vi.advanceTimersByTime(150);
    cancel();
    cancel();
    cancel();

    expect(teardown).toHaveBeenCalledTimes(1);
  });
});

describe("deferSkeleton wired like chat.ts activateChatView", () => {
  // Mirrors chat.ts exactly: show() creates the real chatSkeleton, appends it
  // to the messages container, and returns a teardown that removes it; the
  // load promise's .then() calls cancel() (as chat.ts does before its ok/retry
  // handling).
  function openChat(container: HTMLElement, loadMs: number, ok = true): Promise<boolean> {
    const cancel = deferSkeleton(() => {
      const skel = chatSkeleton();
      container.appendChild(skel);
      return () => {
        skel.remove();
      };
    });
    return new Promise<boolean>((resolve) => {
      setTimeout(() => {
        resolve(ok);
      }, loadMs);
    }).then((res) => {
      cancel();
      return res;
    });
  }

  const hasSkeleton = (c: HTMLElement): boolean => c.querySelector(".skeleton-msg-group") !== null;

  it("(a) fast load (<150ms) never appends the skeleton", async () => {
    const container = document.createElement("div");
    const done = openChat(container, 50);

    // Load resolves at 50ms and cancels the still-pending 150ms show.
    await vi.advanceTimersByTimeAsync(50);
    expect(hasSkeleton(container)).toBe(false);

    // Advance well past 150ms — the show timer was cleared, nothing appears.
    await vi.advanceTimersByTimeAsync(500);
    expect(hasSkeleton(container)).toBe(false);

    await done;
  });

  it("(b) slow load (>150ms) appends the skeleton then removes it on completion", async () => {
    const container = document.createElement("div");
    const done = openChat(container, 300);

    // At 150ms the show timer fires and the skeleton is appended.
    await vi.advanceTimersByTimeAsync(150);
    expect(hasSkeleton(container)).toBe(true);

    // At 300ms the load resolves; cancel() removes the shown skeleton.
    await vi.advanceTimersByTimeAsync(150);
    expect(hasSkeleton(container)).toBe(false);

    await done;
  });

  it("(c) removes the shown skeleton on a failed load too (retry path)", async () => {
    const container = document.createElement("div");
    const done = openChat(container, 300, false);

    await vi.advanceTimersByTimeAsync(150);
    expect(hasSkeleton(container)).toBe(true);

    // chat.ts calls cancel() before the !ok retry branch, so the skeleton is
    // gone regardless of the resolved value.
    await vi.advanceTimersByTimeAsync(150);
    expect(hasSkeleton(container)).toBe(false);
    await expect(done).resolves.toBe(false);
  });
});
