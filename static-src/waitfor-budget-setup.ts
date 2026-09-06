// Raises `vi.waitFor`'s default failure bound for the whole suite.
//
// `vi.waitFor` defaults to 1000ms and vitest exposes no config option for it, so
// the default is the only place a suite-wide answer can live. The reason it needs
// one is in `__test-helpers__/frame-budget.ts`: a browser running this suite
// delivers animation frames at 1Hz partway through the run, so a poll waiting on
// anything frame-driven — a ResizeObserver delivery, a focus move, a repaint —
// cannot settle inside one second however correct the code is.
//
// Patching the default rather than the ~90 call sites is deliberate. A call site
// that omits a timeout is asking for "the suite's normal bound", and that is
// exactly the thing being corrected; editing each one would leave the next test
// author to rediscover this, since a fresh `vi.waitFor` would again get 1000ms.
// A call that names its own timeout is untouched, so a test that deliberately
// bounds a wait tightly keeps its meaning.
//
// This widens a FAILURE bound only. Every consumer polls on the product's own
// output, so a working path returns on its first satisfied check and pays
// nothing; only a genuine failure waits longer before reporting.
import { vi } from "vitest";
import { FRAME_BUDGET_MS } from "./__test-helpers__/frame-budget.js";

type WaitFor = typeof vi.waitFor;

const original: WaitFor = vi.waitFor.bind(vi);

vi.waitFor = ((callback: Parameters<WaitFor>[0], options?: Parameters<WaitFor>[1]) => {
  if (options === undefined) {
    return original(callback, { timeout: FRAME_BUDGET_MS });
  }
  // A bare number is the timeout; anything else already states its own.
  if (typeof options === "number") {
    return original(callback, { timeout: options });
  }
  return original(callback, options);
}) as WaitFor;
