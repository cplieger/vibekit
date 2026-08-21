// @vitest-environment happy-dom
// Table-driven tests for platform.ts guardAction debounce behavior.
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { guardAction } from "./platform.js";

describe("guardAction", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const cases = [
    {
      name: "first call fires immediately",
      ms: 400,
      calls: [{ advanceMs: 0 }],
      expectedCalls: 1,
    },
    {
      name: "second call within window is suppressed",
      ms: 400,
      calls: [{ advanceMs: 0 }, { advanceMs: 200 }],
      expectedCalls: 1,
    },
    {
      name: "call after window expires fires again",
      ms: 400,
      calls: [{ advanceMs: 0 }, { advanceMs: 401 }],
      expectedCalls: 2,
    },
    {
      name: "call exactly at window boundary is suppressed",
      ms: 400,
      calls: [{ advanceMs: 0 }, { advanceMs: 399 }],
      expectedCalls: 1,
    },
    {
      // The window is exclusive at its end: the guard suppresses a call that
      // lands INSIDE it, and 400ms after the last fire is no longer inside.
      name: "call exactly ms after the last fire is allowed through",
      ms: 400,
      calls: [{ advanceMs: 0 }, { advanceMs: 400 }],
      expectedCalls: 2,
    },
    {
      name: "custom ms parameter is respected",
      ms: 1000,
      calls: [{ advanceMs: 0 }, { advanceMs: 500 }, { advanceMs: 1001 }],
      expectedCalls: 2,
    },
    {
      name: "multiple calls after window each fire",
      ms: 400,
      calls: [{ advanceMs: 0 }, { advanceMs: 500 }, { advanceMs: 1000 }],
      expectedCalls: 3,
    },
    {
      name: "rapid burst only fires once",
      ms: 400,
      calls: [
        { advanceMs: 0 },
        { advanceMs: 50 },
        { advanceMs: 100 },
        { advanceMs: 150 },
        { advanceMs: 200 },
      ],
      expectedCalls: 1,
    },
    {
      name: "default ms is 400 when not specified",
      ms: undefined,
      calls: [{ advanceMs: 0 }, { advanceMs: 399 }, { advanceMs: 401 }],
      expectedCalls: 2,
    },
  ] as const;

  for (const tc of cases) {
    it(tc.name, () => {
      expect.assertions(1);
      const fn = vi.fn();
      const guarded = tc.ms === undefined ? guardAction(fn) : guardAction(fn, tc.ms);

      let elapsed = 0;
      for (const call of tc.calls) {
        const advance = call.advanceMs - elapsed;
        if (advance > 0) {
          vi.advanceTimersByTime(advance);
        }
        elapsed = call.advanceMs;
        guarded();
      }

      expect(fn).toHaveBeenCalledTimes(tc.expectedCalls);
    });
  }
});
