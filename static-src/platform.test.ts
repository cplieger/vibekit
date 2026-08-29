// Tests for platform.ts guardDuplicateActivation: the duplicate-pointer-dispatch
// guard in front of activation handlers.
//
// Events are real PointerEvent / MouseEvent objects carrying the pointerType and
// detail the browser stamps on each activation shape (a pointer click has
// detail > 0 and a concrete pointerType; a keyboard activation has detail 0 and
// an empty pointerType). The guard's clock is its injectable `now` parameter, so
// arrival times are hand-cranked numbers rather than races against real time —
// an event's own timeStamp is read-only and cannot be aimed.
import { describe, expect, it, vi } from "vitest";
import { guardDuplicateActivation } from "./platform.js";

/** A pointer-initiated click: what a mouse/touch/pen press dispatches. */
function pointerClick(pointerType: "mouse" | "touch" | "pen"): PointerEvent {
  return new PointerEvent("click", { pointerType, detail: 1 });
}

/** A keyboard activation: Enter/Space on a button synthesizes a click with
 *  detail 0 and no pointerType. */
function keyboardClick(): PointerEvent {
  return new PointerEvent("click", { pointerType: "", detail: 0 });
}

/** A guarded spy on a hand-cranked clock: `at(t, e)` delivers `e` at time t. */
function harness(): {
  fn: ReturnType<typeof vi.fn>;
  at: (time: number, e: MouseEvent) => void;
} {
  let t = 0;
  const fn = vi.fn();
  const guarded = guardDuplicateActivation(fn, () => t);
  return {
    fn,
    at(time: number, e: MouseEvent): void {
      t = time;
      guarded(e);
    },
  };
}

describe("guardDuplicateActivation", () => {
  it("absorbs a duplicate mouse dispatch 40 ms after the accepted click", () => {
    const { fn, at } = harness();
    const accepted = pointerClick("mouse");
    at(0, accepted);
    at(40, pointerClick("mouse"));
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn).toHaveBeenCalledWith(accepted);
  });

  it("dispatches a second mouse click 60 ms after the first", () => {
    const { fn, at } = harness();
    at(0, pointerClick("mouse"));
    at(60, pointerClick("mouse"));
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("dispatches a click landing exactly on the 50 ms window boundary", () => {
    // The window is exclusive at its end: a duplicate is STRICTLY inside it.
    const { fn, at } = harness();
    at(0, pointerClick("mouse"));
    at(50, pointerClick("mouse"));
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("never filters keyboard activations, even 40 ms apart", () => {
    const { fn, at } = harness();
    at(0, keyboardClick());
    at(40, keyboardClick());
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("never filters a keyboard activation arriving 40 ms after a pointer click", () => {
    const { fn, at } = harness();
    at(0, pointerClick("mouse"));
    at(40, keyboardClick());
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("does not treat a different pointer type as a duplicate", () => {
    // A ghost dispatch replays ONE physical gesture, so it arrives with the
    // same pointerType; mouse-then-touch is two inputs, not a duplicate.
    const { fn, at } = harness();
    at(0, pointerClick("mouse"));
    at(40, pointerClick("touch"));
    expect(fn).toHaveBeenCalledTimes(2);
  });

  it("dispatches every deliberate click in a 100 ms series", () => {
    const { fn, at } = harness();
    for (const t of [0, 100, 200, 300, 400]) {
      at(t, pointerClick("mouse"));
    }
    expect(fn).toHaveBeenCalledTimes(5);
  });

  it("anchors the ghost window on the accepted activation, not the absorbed duplicate", () => {
    const { fn, at } = harness();
    at(0, pointerClick("mouse"));
    at(40, pointerClick("mouse")); // absorbed; must NOT restart the window
    at(80, pointerClick("mouse")); // 80 ms past the ACCEPTED click → dispatched
    expect(fn).toHaveBeenCalledTimes(2);
  });
});
